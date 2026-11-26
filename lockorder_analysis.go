// Copyright 2026 The YuniKorn Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checklocks

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// ignoredAround is functionIgnored for fn, or for the function around it when fn is a literal
// called in place; any other literal is analysed as a function of its own.
func (pc *lockOrderContext) ignoredAround(fn *ssa.Function) bool {
	if view := pc.engineView(); view != nil && view.recordable(fn) {
		return pc.functionIgnored(topOf(fn))
	}
	return pc.functionIgnored(fn)
}

// analyzeFunction walks a function and produces its summary, reporting any ordering
// violation it realizes.
//
// The walk is a single pass over the blocks in the order the SSA builder produced them,
// with the lock set merged at every block from the sets that reach it. Loops are handled by
// the outer fixpoint rather than by iterating here: a summary that grows re-runs the
// function, so a class acquired on the second time round a loop is still seen.
func (pc *lockOrderContext) analyzeFunction(fn *ssa.Function, summary *summaryFact, report bool) {
	obj := funcObject(fn)
	var ff funcFact
	if obj != nil && pc.pass.ImportObjectFact(obj, &ff) && ff.BlockingIgnore {
		// Its waits count for no caller: only a declared wait is left of its blocking part.
		defer func() {
			summary.Blocking, summary.BlockingReleased, summary.BlockingNamed = false, nil, false
			summary.ParamCalls = nil
			if pc.witness != nil {
				pc.witness.paths[fn] = slices.DeleteFunc(pc.witness.paths[fn], func(c witnessCandidate) bool { return !c.annotated })
				delete(pc.witness.edges, fn)
			}
			if ff.Blocking {
				summary.addBlocking(nil, true /* named */)
			}
		}()
	}
	if obj != nil && ff.Blocking {
		// A function declared to block is a sink whatever its body does, which is how a
		// wait this analysis cannot see through is named: one reached by an indirect call,
		// or one inside a dependency that is not analyzed.
		summary.addBlocking(nil, true /* named */)
		pc.recordWitnessWait(fn, displayName(fn)+" is declared blocking", fn.Pos(), nil, true /* named */, true /* annotated */)
	}
	if pc.recordState() != nil {
		if view := pc.engineView(); view != nil {
			if reason := view.gaveUpIn(fn); reason != "" {
				if own := view.gaveUp[fn]; own != "" && report && !pc.ignoredAround(fn) &&
					(fn.Parent() == nil || view.gaveUpIn(fn.Parent()) == "") {
					name := "lockorder"
					if pc.check == checkBlocking {
						name = "lockblocking"
					}
					pc.maybeFail(fn.Pos(), "%s skipped this function: checklocks gave up on it (%s)", name, own)
				}
				return
			}
		}
	}
	if fn.Blocks == nil {
		return
	}
	ignored := pc.ignoredAround(fn)

	// Entry state: the classes the function is annotated as already holding. This is what
	// makes an annotated callback work, where the caller took the lock and handed control
	// over through machinery this analysis cannot follow.
	entry := pc.entryLocks(fn)

	// The state reaching each block, built up as the blocks are walked.
	in := make([]*walkState, len(fn.Blocks))
	in[0] = newWalkState(entry)

	for _, b := range blockOrder(fn) {
		if b == fn.Recover {
			// The recover block is a second entry point the SSA builder gives every
			// function that defers: control resumes there after a recovered panic, with
			// no predecessor to say what is held. Its return would run every deferred
			// call a second time against an empty lock set, which says nothing true and
			// erases what the run at the real return established.
			continue
		}
		state := in[b.Index]
		if state == nil {
			// Unreachable from the entry as far as this walk is concerned; start clean
			// rather than skipping, so its calls are still checked.
			state = newWalkState(pc.emptyLocks())
		}
		state = state.fork()
		cur := state.locks
		curInst := state.instances

		for _, instr := range b.Instrs {
			switch t := instr.(type) {
			case *ssa.Defer:
				state.deferred = append(state.deferred, t)
			case *ssa.Go:
				// The acquisitions of the spawned function belong to the new
				// goroutine, so they are not attributed to this one. Breaking a
				// nesting with "go" is the sanctioned fix for an inverted order, and
				// reporting it would punish exactly that fix.
			case ssa.CallInstruction:
				pc.visitCall(fn, t, cur, curInst, summary, report && !ignored)
			default:
				pc.visitChannelOp(fn, instr, cur, summary, report && !ignored)
			}
		}
		// A deferred call runs when the function RETURNS, not at the end of the block it
		// was written in, so it is evaluated at the return against the set held there.
		// Evaluating it where it was written would drop the lock for the rest of the
		// function, which silently disarms the check for the whole "Lock then defer
		// Unlock" idiom.
		//
		// The order matters too: defers run in reverse registration order, so a
		// notification deferred BEFORE the lock is taken runs after the deferred unlock,
		// which is the idiom for notifying listeners safely.
		if _, isReturn := b.Instrs[len(b.Instrs)-1].(*ssa.Return); isReturn {
			exit := cur.fork()
			var run *ssa.RunDefers
			for _, instr := range b.Instrs {
				if r, ok := instr.(*ssa.RunDefers); ok {
					run = r
				}
			}
			exit.atReturn(run)
			exitInst := curInst.fork()
			for i := len(state.deferred) - 1; i >= 0; i-- {
				pc.visitCall(fn, state.deferred[i], exit, exitInst, summary, report && !ignored)
			}
		}

		// Propagate to the successors.
		for _, succ := range b.Succs {
			if in[succ.Index] == nil {
				in[succ.Index] = state.fork()
				continue
			}
			in[succ.Index].merge(state)
		}
	}
}

// walkState is what reaches a block: the classes held, the instances they were taken on,
// and the calls deferred on the way there.
//
// The deferred calls belong to the path rather than to the walk. A return that is reached
// before a defer is registered does not run it, and an early return out of the middle of a
// function is exactly where that matters: treating every defer written anywhere in the body
// as pending at every return puts a lock back that the early path never dropped.
type walkState struct {
	locks     siteLocks
	instances *instanceSet
	deferred  []ssa.CallInstruction
}

// newWalkState returns the state a block starts from, holding the given classes. Nothing is
// carried in for the instances: an annotation names a class, not the instance it was taken
// on.
func newWalkState(locks siteLocks) *walkState {
	return &walkState{locks: locks, instances: newInstanceSet()}
}

// fork copies the state, for the separate paths of a branch.
func (s *walkState) fork() *walkState {
	return &walkState{
		locks:     s.locks.fork(),
		instances: s.instances.fork(),
		deferred:  slices.Clone(s.deferred),
	}
}

// merge folds another state into this one.
//
// The lock and instance sets merge as they do. For the deferred calls the longer list wins:
// two paths into a block have registered a prefix of the same sequence, since a defer
// registered on one path is registered on every path that goes past it, and the longer
// prefix is the one with more still to run.
func (s *walkState) merge(other *walkState) {
	s.locks.merge(other.locks)
	s.instances.merge(other.instances)
	if len(other.deferred) > len(s.deferred) {
		s.deferred = slices.Clone(other.deferred)
	}
}

// blockOrder returns the blocks of a function in reverse postorder, so that a block is
// walked after the blocks that reach it and starts from what they leave held.
//
// The order the SSA builder numbers them in is not that order: a short circuit condition
// puts the blocks that evaluate it AFTER the branch they guard, so walking by index reaches
// the guarded block before anything has flowed into it, starts it with nothing held, and
// loses the lock for the whole of the rest of the function.
//
// Blocks the entry cannot reach are appended at the end, so that their calls are still
// checked, which is what walking every block by index did for them.
func blockOrder(fn *ssa.Function) []*ssa.BasicBlock {
	seen := make([]bool, len(fn.Blocks))
	order := make([]*ssa.BasicBlock, 0, len(fn.Blocks))
	var visit func(b *ssa.BasicBlock)
	visit = func(b *ssa.BasicBlock) {
		if seen[b.Index] {
			return
		}
		seen[b.Index] = true
		for _, succ := range b.Succs {
			visit(succ)
		}
		order = append(order, b)
	}
	visit(fn.Blocks[0])
	slices.Reverse(order)
	for _, b := range fn.Blocks {
		if !seen[b.Index] {
			order = append(order, b)
		}
	}
	return order
}

// visitCall handles one call: the lock operations it performs, and the classes it may
// acquire through its callee.
func (pc *lockOrderContext) visitCall(fn *ssa.Function, call ssa.CallInstruction, cur siteLocks, curInst *instanceSet, summary *summaryFact, report bool) {
	cur.at(fn, call)
	pc.shadowCall(call, cur, report)
	// The engine's additions apply on every exit, except for a call consumed as a lock operation.
	consumed := false
	defer func() {
		pc.shadowGains(fn, call, cur, report)
		cur.after(fn, call, consumed)
	}()
	callee := staticCallee(call)

	// A lock operation changes what is held. Both the standard lock types and a type that
	// wraps its own lock and forwards to it are recognised, since the code bases this is
	// aimed at reach their locks through such a wrapper.
	if obj := calledFunc(callee); obj != nil {
		if report {
			pc.checkWrapper(call, callee, obj)
		}
		// A downgrade leaves the lock held and its class unchanged, so it is no event here.
		if op := lockOpName(obj.Name()); op == opAcquire || op == opRelease {
			recv, ok := receiverOf(call)
			var class string
			if ok {
				class = pc.classOf(recv)
			}
			// A lock method of a standard lock type on an object with no class is still a
			// lock operation, it just does not participate in the order; consuming it here
			// stops it being treated as an ordinary call.
			if class != "" || (ok && isStandardLock(pc.pass, obj)) {
				switch op {
				case opAcquire:
					if class != "" {
						pc.checkAcquire(call.Pos(), class, displayName(callee), cur.held(), summary, report)
						pc.checkHierarchy(call.Pos(), recv, class, displayName(callee), curInst, report)
						pc.shadowReleased(fn, call, cur, report, "acquire")
						// What has been released is recorded as it stands BEFORE the
						// acquisition: taking the lock back closes the gap.
						summary.addAcquire(class, cur.releasedClasses())
						cur.acquire(class)
						pc.shadowAcquire(cur, class)
						curInst.acquire(recv, class)
					}
				case opRelease:
					cur.release(class, isReceiver(fn, recv))
					pc.shadowRelease(fn, call, recv, class, cur, report)
					curInst.release(recv)
				}
				consumed = true
				return
			}
		}
	}

	// A call to a known sink. This is checked before the summaries, since a sink reached
	// by interface dispatch has no callee to consult: the method the interface declares is
	// enough to recognise a client library's round trip.
	name, sinkKey, isSink := blockingCallee(call, callee)
	if isSink {
		pc.shadowReleased(fn, call, cur, report, "sink")
		pc.noteBlocking(call.Pos(), "calling "+name+", which blocks", sinkKey, nil, cur.held(), cur.releasedClasses(), true /* named */, summary, report)
		pc.recordWitnessWait(fn, "calling "+name, call.Pos(), cur.releasedClasses(), true /* named */, false /* annotated */)
	}

	// Any other call contributes the classes its callee may acquire. A call into a
	// function this analysis has no summary for contributes nothing, which is the
	// documented unsoundness of a modular analysis.
	if callee == nil {
		if common := call.Common(); common.Method != nil {
			if tp, ok := types.Unalias(common.Value.Type()).(*types.TypeParam); ok {
				for i := range fn.TypeParams().Len() {
					if fn.TypeParams().At(i) == tp {
						at := strings.TrimPrefix(pc.witnessHop("", call.Pos()), " at ")
						pc.shadowReleased(fn, call, cur, report, "param")
						summary.addParamCall(i, common.Method.Name(), isReceiver(fn, common.Value), cur.releasedClasses(), at)
					}
				}
			}
		}
		return
	}
	if isSelfRecursive(fn, callee) {
		// The fixpoint already accounts for the function's own effects.
		return
	}
	var cs summaryFact
	obj := funcObject(callee)
	if obj == nil {
		// A closure has no fact. It is walked as a function of its own, so a violation
		// inside it is reported there, but its effects do not reach this call site.
		return
	}
	if !pc.pass.ImportObjectFact(obj, &cs) {
		return
	}
	var ff funcFact
	pc.pass.ImportObjectFact(obj, &ff)
	// A callee called on this function's own receiver acts on the same object, so a lock it
	// releases of its receiver's is one of this function's caller's locks too, and that
	// carries into this summary. A callee called on any other object says nothing about the
	// locks of this one, so only this function's own releases carry.
	onReceiver := false
	if recv, ok := receiverOf(call); ok {
		onReceiver = isReceiver(fn, recv)
	}
	pc.shadowReleased(fn, call, cur, report, "call")
	released := cur.releasedClasses()
	ownBlocking, ownWitnesses := cs.Blocking, cs.Witnesses
	var resolved []witnessPath
	if len(cs.ParamCalls) > 0 {
		resolved = pc.instanceSummary(callee, obj, &cs)
	}
	// A sink on the list has been reported already; its own summary is very likely to say
	// it blocks as well, and saying so twice at one call site is noise.
	//
	// A wait the callee's summary NAMES travels however far the callee is: it is a
	// statement about that function. One the callee only inferred from a channel operation
	// travels as far as this analysis can see the whole picture, which is what
	// blockingInScope decides.
	if cs.Blocking && !isSink && (cs.BlockingNamed || pc.blockingInScope(obj)) {
		// The callee released these before it waits, so they are not held across the wait,
		// the same subtraction the acquisitions get.
		reached := released
		if onReceiver {
			reached = unionClasses(released, cs.BlockingReleased)
		}
		// Only a named wait travels to a caller out of scope, so only a path ending at one is
		// shown there.
		namedOnly := !pc.blockingInScope(obj)
		if obj.Pkg() == pc.pass.Pkg {
			pc.recordWitnessEdge(fn, call, callee, obj, released, onReceiver)
			if !ownBlocking {
				pc.recordWitnessPaths(fn, call, callee, resolved, released, onReceiver, namedOnly)
			}
		} else {
			paths := resolved
			if ownBlocking {
				paths = ownWitnesses
			}
			pc.recordWitnessPaths(fn, call, callee, paths, released, onReceiver, namedOnly)
		}
		reported := subtractClasses(cur.held(), cs.BlockingReleased)
		var witness []string
		if report && pc.check == checkBlocking {
			witness = pickWitness(cs.Witnesses, reported, namedOnly)
		}
		pc.noteBlocking(call.Pos(), "calling "+displayName(callee)+", which may block", calleeKey(callee), witness,
			reported, reached, cs.BlockingNamed, summary, report)
	}
	for _, a := range cs.Acquires {
		if ff.Ignore {
			break
		}
		// The callee dropped these before it acquired, so they are not held across the
		// acquisition however it looks from here: the unlock-relock gap.
		pc.checkAcquire(call.Pos(), a.Class, displayName(callee), subtractClasses(cur.held(), a.ReleasedBefore), summary, report)
		if onReceiver {
			summary.addAcquire(a.Class, unionClasses(released, a.ReleasedBefore))
			continue
		}
		summary.addAcquire(a.Class, released)
	}
}

// checkAcquire records the pair and reports if the acquisition breaks the order.
func (pc *lockOrderContext) checkAcquire(pos token.Pos, acquired, via string, heldClasses []string, summary *summaryFact, report bool) {
	for _, held := range heldClasses {
		summary.addPair(held, acquired)
	}
	// The walk is shared with the blocking analyzer, which builds the same summaries and
	// asks its own question; the order is this analyzer's to report on.
	if !report || pc.check != checkOrder {
		return
	}
	for _, held := range heldClasses {
		if !pc.order.violates(held, acquired) {
			continue
		}
		// One callee that acquires a class is one defect however many
		// call sites reach it, so the sites are grouped by what is
		// acquired, through what, and what was held.
		key := "order\x00" + acquired + "\x00" + via + "\x00" + held
		if held == acquired {
			pc.report(key, pos, "acquiring %s (via %s) while holding %s: two locks of one class must not nest", acquired, via, held)
			continue
		}
		pc.report(key, pos, "acquiring %s (via %s) while holding %s: the declared order has %s before %s", acquired, via, held, acquired, held)
	}
}

// entryLocks returns the classes a function is annotated as holding on entry.
//
// The annotations are the ones the checklocks analyzer already reads, so a code base that
// has been annotated for guarded fields gets the ordering check for free: a function
// declared to run with a lock held starts with that lock's class held here.
func (pc *lockOrderContext) entryLocks(fn *ssa.Function) siteLocks {
	if l := pc.recordState(); l != nil {
		return l
	}
	cs := newClassSet()
	cs.pc = pc
	obj := funcObject(fn)
	if obj == nil {
		return cs
	}
	for _, class := range pc.heldOnEntry(obj) {
		cs.enter(class)
	}
	for k, class := range pc.engineEntry(fn) {
		cs.eng[k] = class
	}
	return cs
}

func (pc *lockOrderContext) emptyLocks() siteLocks {
	if l := pc.recordState(); l != nil {
		return l
	}
	cs := newClassSet()
	cs.pc = pc
	return cs
}

// functionIgnored reports whether reporting is suppressed inside a function, by the
// ignore annotation of the analyzer the walk is running for.
func (pc *lockOrderContext) functionIgnored(fn *ssa.Function) bool {
	obj := funcObject(fn)
	if obj == nil {
		return false
	}
	var ff funcFact
	if !pc.pass.ImportObjectFact(obj, &ff) {
		return false
	}
	// Each analyzer has its own annotation: silencing an ordering report is not a statement
	// about waiting under a lock, or the other way round.
	if pc.check == checkBlocking {
		return ff.BlockingIgnore
	}
	return ff.Ignore
}

// blockingInScope reports whether a callee's summary may contribute its blocking bit here.
//
// Waiting inside a dependency is recognised by the sink list, not by walking into it. The
// standard library is full of channel receives that no caller can see or avoid: formatting
// a string reaches one a few layers down, so carrying the bit out of it would make every
// function that logs a line or builds an error blocking, and an analysis that reports
// everything reports nothing. Within the module being analyzed the opposite holds, and
// reaching a wait through three of its own functions is exactly what this is for.
//
// A function that carries the annotation is exempt from this, wherever it lives: saying a
// function blocks is a deliberate statement about it, not an observation of its body.
func (pc *lockOrderContext) blockingInScope(obj *types.Func) bool {
	pkg := obj.Pkg()
	if pkg == nil {
		return false
	}
	if pkg == pc.pass.Pkg {
		return true
	}
	if pc.pass.Module == nil || pc.pass.Module.Path == "" {
		return false
	}
	path := pkg.Path()
	return path == pc.pass.Module.Path || strings.HasPrefix(path, pc.pass.Module.Path+"/")
}

// annotatedBlocking reports whether a function is declared to block.
func (pc *lockOrderContext) annotatedBlocking(obj *types.Func) bool {
	var ff funcFact
	if !pc.pass.ImportObjectFact(obj, &ff) {
		return false
	}
	return ff.Blocking
}

// noteBlocking records that the function may wait here, and reports if a lock is held while
// it does.
//
// The record goes into the summary whichever analyzer is walking, so that the bit and the
// classes released by then travel to the callers of this function in one summary.
//
// witness and what are named in the message but kept out of the key, so the sites of one
// defect group together even where one method prints two ways, as it does when it is
// reached through two interface types. waitKey identifies the wait: two callees print alike.
func (pc *lockOrderContext) noteBlocking(pos token.Pos, what, waitKey string, witness, heldClasses, released []string, named bool, summary *summaryFact, report bool) {
	summary.addBlocking(released, named)
	if !report || pc.check != checkBlocking || len(heldClasses) == 0 {
		return
	}
	held := strings.Join(heldClasses, ", ")
	const format = "%s while holding %s: a wait under a lock stalls every other user of it"
	plain := fmt.Sprintf(format, what, held)
	message, path := plain, strings.Join(witness, " -> ")
	if path != "" {
		message = fmt.Sprintf(format, what+" ("+path+")", held)
	}
	pc.reportPath("blocking\x00"+waitKey+"\x00"+held, pos, path, message, plain)
}

// visitChannelOp handles the channel operations, which are instructions rather than calls.
//
// A send or a receive waits for the other side. A select waits too, unless it has a default
// case, which is what makes the non blocking dispatch idiom safe under a lock; the SSA form
// records exactly that distinction, so it does not have to be inferred from the syntax.
func (pc *lockOrderContext) visitChannelOp(fn *ssa.Function, instr ssa.Instruction, cur siteLocks, summary *summaryFact, report bool) {
	var what string
	switch t := instr.(type) {
	case *ssa.Send:
		what = "a channel send"
	case *ssa.UnOp:
		if t.Op != token.ARROW {
			return
		}
		what = "a channel receive"
	case *ssa.Select:
		if !t.Blocking {
			return
		}
		what = "a select with no default case"
	default:
		return
	}
	cur.at(fn, instr)
	// A channel operation is a wait this analysis inferred rather than one it can name, so
	// it does not travel out of the package it occurs in unless the module says otherwise.
	pc.shadowReleased(fn, instr, cur, report, "chan")
	pc.noteBlocking(instr.Pos(), what, what /* waitKey: its kind is all that identifies it */, nil, cur.held(), cur.releasedClasses(), false /* named */, summary, report)
	pc.recordWitnessWait(fn, what, instr.Pos(), cur.releasedClasses(), false /* named */, false /* annotated */)
}

// analyzePackage computes the summaries of the package's functions to a fixpoint and then
// reports against the settled summaries.
//
// Two phases are needed because a call site consults the summary of its callee: reporting
// while the summaries are still growing would report against a half computed callee and
// produce different diagnostics depending on the order the functions happened to be
// visited in.
func (pc *lockOrderContext) analyzePackage(fns []*ssa.Function) {
	summaries := make(map[*ssa.Function]*summaryFact, len(fns))
	for _, fn := range fns {
		summaries[fn] = &summaryFact{}
	}

	// Phase one: grow the summaries until nothing changes.
	for round := 0; ; round++ {
		changed := false
		for _, fn := range fns {
			before := *summaries[fn]
			next := &summaryFact{}
			next.merge(&before)
			pc.analyzeFunction(fn, next, false)
			if !next.equal(&before) {
				changed = true
			}
			summaries[fn] = next
			pc.exportSummary(fn, next)
		}
		if !changed {
			break
		}
		// The summaries only grow and the class set is finite, so this terminates; the
		// bound is a guard against a bug rather than an expected outcome.
		if round > maxFixpointRounds {
			break
		}
	}

	// The witness names a path through the settled summaries, so it is derived between the
	// phases: phase one would name a path through a callee that is still growing.
	pc.deriveWitnesses(fns, summaries)

	// Phase two: report against the settled summaries.
	for _, fn := range fns {
		pc.analyzeFunction(fn, summaries[fn], true)
	}
	pc.shadowCompare()
	pc.summaryDump(fns, summaries)
}

// maxFixpointRounds bounds the fixpoint. The summaries grow monotonically over a finite set
// of classes, so the loop settles well inside this; it exists so a bug cannot hang a build.
const maxFixpointRounds = 100

// exportSummary publishes a function's summary so dependent packages can consult it.
func (pc *lockOrderContext) exportSummary(fn *ssa.Function, summary *summaryFact) {
	obj := funcObject(fn)
	if obj == nil {
		return
	}
	pc.pass.ExportObjectFact(obj, summary)
}

// witnessCandidate is one path from a function to a wait, ranked against the other paths
// the function has. A path that leaves this package is complete when it is recorded, since
// the callee's own fact carries the rest of it.
type witnessCandidate struct {
	hops     []string
	released []string

	// named says the wait the path ends at is one this analysis can name rather than one
	// it inferred from a channel operation.
	named bool

	// annotated says the path is the +blocking declaration on the function itself.
	annotated bool
}

// witnessEdge is a call whose callee's summary contributed the blocking bit, extended at
// derivation time by the paths the callee settles on.
type witnessEdge struct {
	hop      string
	target   *types.Func
	released []string

	// onReceiver says the callee acts on this function's own receiver, which is the only
	// case in which what the callee releases is released here too.
	onReceiver bool
}

// witnessGraph is what one extra walk records: for every function, the waits reached in its
// own body and the calls that reach one through a callee in this package.
type witnessGraph struct {
	paths map[*ssa.Function][]witnessCandidate
	edges map[*ssa.Function][]witnessEdge
}

// recordWitnessWait records a wait in a function's own body, or the declaration that makes
// the function one, as a path of a single hop.
func (pc *lockOrderContext) recordWitnessWait(fn *ssa.Function, text string, pos token.Pos, released []string, named, annotated bool) {
	if pc.witness == nil {
		return
	}
	pc.witness.paths[fn] = append(pc.witness.paths[fn], witnessCandidate{
		hops:      []string{pc.witnessHop(text, pos)},
		released:  released,
		named:     named,
		annotated: annotated,
	})
}

// recordWitnessEdge records a call that reaches a wait through a callee of this package,
// whose own paths are still being derived.
func (pc *lockOrderContext) recordWitnessEdge(fn *ssa.Function, call ssa.CallInstruction, callee *ssa.Function, obj *types.Func, released []string, onReceiver bool) {
	if pc.witness == nil {
		return
	}
	pc.witness.edges[fn] = append(pc.witness.edges[fn], witnessEdge{
		hop:        pc.witnessHop("calling "+displayName(callee), call.Pos()),
		target:     obj,
		released:   released,
		onReceiver: onReceiver,
	})
}

// recordWitnessPaths records a call into another package, whose fact carries the callee's
// paths whole. All of them cross, since this function's callers rank them by what they hold.
func (pc *lockOrderContext) recordWitnessPaths(fn *ssa.Function, call ssa.CallInstruction, callee *ssa.Function, paths []witnessPath, released []string, onReceiver, namedOnly bool) {
	if pc.witness == nil {
		return
	}
	hop := pc.witnessHop("calling "+displayName(callee), call.Pos())
	for _, p := range paths {
		if namedOnly && !p.Named {
			continue
		}
		pc.witness.paths[fn] = append(pc.witness.paths[fn], witnessExtend(witnessEdge{
			hop:        hop,
			released:   released,
			onReceiver: onReceiver,
		}, witnessCandidate{hops: p.Hops, released: p.Released, named: p.Named}))
	}
}

// pickWitness chooses the path a call site names: the one whose wait still holds the most of
// the classes the message reports, then a named wait, fewer hops, and the hop text. A path
// whose wait holds none of them explains nothing, so no path is named then.
func pickWitness(paths []witnessPath, reported []string, namedOnly bool) []string {
	var best *witnessPath
	for i := range paths {
		p := &paths[i]
		if namedOnly && !p.Named {
			continue
		}
		if best == nil || siteWitnessBetter(p, best, reported) {
			best = p
		}
	}
	if best == nil || countIn(best.Released, reported) == len(reported) {
		return nil
	}
	return best.Hops
}

// siteWitnessBetter reports whether c is preferred over than at a site that reports these
// classes as held.
func siteWitnessBetter(c, than *witnessPath, reported []string) bool {
	if n, m := countIn(c.Released, reported), countIn(than.Released, reported); n != m {
		return n < m
	}
	if c.Named != than.Named {
		return c.Named
	}
	if len(c.Hops) != len(than.Hops) {
		return len(c.Hops) < len(than.Hops)
	}
	return slices.Compare(c.Hops, than.Hops) < 0
}

// countIn returns how many of the classes are in the set.
func countIn(classes, set []string) int {
	return len(classes) - len(subtractClasses(classes, set))
}

// witnessHop renders one hop of a path.
func (pc *lockOrderContext) witnessHop(text string, pos token.Pos) string {
	p := pc.pass.Fset.Position(pos)
	file, line := "?", "?"
	if p.Filename != "" {
		// The package clause names the hop's package: the import path is
		// "command-line-arguments" over a file list, and the directory a checkout.
		file = pc.pass.Pkg.Name() + "/" + filepath.Base(p.Filename)
	}
	if p.Line > 0 {
		line = strconv.Itoa(p.Line)
	}
	return text + " at " + file + ":" + line
}

// deriveWitnesses sets each function's Witnesses from the settled summaries, iterating to a
// fixpoint so that a caller written above its callee, or a cycle of calls, still gets one.
func (pc *lockOrderContext) deriveWitnesses(fns []*ssa.Function, summaries map[*ssa.Function]*summaryFact) {
	graph := &witnessGraph{
		paths: make(map[*ssa.Function][]witnessCandidate),
		edges: make(map[*ssa.Function][]witnessEdge),
	}
	pc.witness = graph
	for _, fn := range fns {
		// The summary is a throwaway: this walk is here for what it records, and the
		// summaries it would produce have settled already.
		pc.analyzeFunction(fn, &summaryFact{}, false)
	}
	pc.witness = nil

	// An edge names its callee's object, which is also what a bound method wrapper and an
	// instance of a generic function resolve to.
	byObj := make(map[*types.Func]*ssa.Function, len(fns))
	for _, fn := range fns {
		if obj := funcObject(fn); obj != nil {
			byObj[obj] = fn
		}
	}

	// Keep the best path per released set, not one per function: a caller ranks by the set
	// after unioning its own releases in, so which callee path it needs is not yet known.
	best := make(map[*ssa.Function]map[string]witnessCandidate, len(fns))
	callers := make(map[*ssa.Function][]*ssa.Function, len(fns))
	for _, fn := range fns {
		keyed := make(map[string]witnessCandidate, len(graph.paths[fn]))
		for _, c := range graph.paths[fn] {
			witnessKeep(keyed, c)
		}
		best[fn] = keyed
		for _, e := range graph.edges[fn] {
			if target, ok := byObj[e.target]; ok && !slices.Contains(callers[target], fn) {
				callers[target] = append(callers[target], fn)
			}
		}
	}
	// A function's paths grow only when a callee's do, so each round extends the callers of
	// what the last one changed. The bound is phase one's, as a guard rather than a limit.
	work := slices.Clone(fns)
	for round := 0; len(work) > 0; round++ {
		var next []*ssa.Function
		for _, fn := range work {
			changed := false
			for _, e := range graph.edges[fn] {
				target, ok := byObj[e.target]
				if !ok {
					continue
				}
				// A call into this function itself would otherwise add keys to the
				// map being read, so the callee's paths are taken as they stand.
				for _, sub := range slices.Collect(maps.Values(best[target])) {
					if witnessKeep(best[fn], witnessExtend(e, sub)) {
						changed = true
					}
				}
			}
			if !changed {
				continue
			}
			for _, caller := range callers[fn] {
				if !slices.Contains(next, caller) {
					next = append(next, caller)
				}
			}
		}
		work = next
		if round > maxFixpointRounds {
			break
		}
	}

	for _, fn := range fns {
		// A closure has no fact to publish on, and a function that does not wait has no
		// path to publish.
		summary := summaries[fn]
		if funcObject(fn) == nil || !summary.Blocking {
			continue
		}
		summary.Witnesses = witnessPaths(best[fn])
		// A fact is not to change once exported, so the summary is exported again with its
		// paths rather than relying on the framework holding the same pointer.
		pc.exportSummary(fn, summary)
	}
}

// witnessPaths turns a function's candidates into the paths its fact carries, ordered by the
// classes they release so that the encoding does not depend on map iteration.
func witnessPaths(keyed map[string]witnessCandidate) []witnessPath {
	paths := make([]witnessPath, 0, len(keyed))
	for _, key := range slices.Sorted(maps.Keys(keyed)) {
		c := keyed[key]
		paths = append(paths, witnessPath{Released: c.released, Named: c.named, Hops: c.hops})
	}
	return paths
}

// witnessKeep adds a path to a function's set, keeping the better of the two when the set
// already holds one releasing the same classes. It reports whether the set changed.
func witnessKeep(keyed map[string]witnessCandidate, c witnessCandidate) bool {
	key := strings.Join(c.released, ",")
	if prev, ok := keyed[key]; ok && !witnessBetter(c, prev) {
		return false
	}
	keyed[key] = c
	return true
}

// witnessExtend extends a callee's path through the call that reaches it. What the callee
// releases is released here only when it acts on this function's receiver, the same rule
// the call site itself applies.
func witnessExtend(e witnessEdge, sub witnessCandidate) witnessCandidate {
	released := e.released
	if e.onReceiver {
		released = unionClasses(e.released, sub.released)
	}
	hops := make([]string, 0, len(sub.hops)+1)
	hops = append(hops, e.hop)
	return witnessCandidate{
		hops:     append(hops, sub.hops...),
		released: released,
		named:    sub.named,
	}
}

// witnessBetter reports whether c is preferred over than, which releases the same classes:
// the function's own +blocking declaration, then a named wait over a channel operation,
// fewer hops, and the hop text. Ranking the released sets is a call site's to do.
func witnessBetter(c, than witnessCandidate) bool {
	if c.annotated != than.annotated {
		// A declaration is a statement about the whole function, so it stands for
		// whatever its body does.
		return c.annotated
	}
	if c.named != than.named {
		return c.named
	}
	if len(c.hops) != len(than.hops) {
		return len(c.hops) < len(than.hops)
	}
	return slices.Compare(c.hops, than.hops) < 0
}

// report emits a diagnostic, grouped with the other sites of the same defect
// when grouping is on.
func (pc *lockOrderContext) report(key string, pos token.Pos, fmtStr string, args ...any) {
	msg := fmt.Sprintf(fmtStr, args...)
	pc.reportPath(key, pos, "", msg, msg)
}

// reportPath emits a diagnostic that names the path to the wait it reports, with plain the
// message that leaves the path out. A grouped report names a path only where every site of
// the group reaches the wait the same way, which is known once the sites have been seen.
func (pc *lockOrderContext) reportPath(key string, pos token.Pos, path, msg, plain string) {
	if pc.grouped() {
		pc.maybeFailGrouped(key, pos, path, plain, "%s", msg)
		return
	}
	pc.maybeFail(pos, "%s", msg)
}

// grouped reports whether the analyzer being run groups its reports.
func (pc *lockOrderContext) grouped() bool {
	if pc.check == checkBlocking {
		return groupBlockingReports
	}
	return groupOrderReports
}
