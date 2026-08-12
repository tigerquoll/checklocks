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
	"go/token"
	"go/types"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// analyzeFunction walks a function and produces its summary, reporting any ordering
// violation it realizes.
//
// The walk is a single pass over the blocks in the order the SSA builder produced them,
// with the lock set merged at every block from the sets that reach it. Loops are handled by
// the outer fixpoint rather than by iterating here: a summary that grows re-runs the
// function, so a class acquired on the second time round a loop is still seen.
func (pc *lockOrderContext) analyzeFunction(fn *ssa.Function, summary *summaryFact, report bool) {
	if fn.Blocks == nil {
		return
	}
	ignored := pc.functionIgnored(fn)

	// Entry state: the classes the function is annotated as already holding. This is what
	// makes an annotated callback work, where the caller took the lock and handed control
	// over through machinery this analysis cannot follow.
	entry := pc.entryClasses(fn)

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
			state = newWalkState(newClassSet())
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
	locks     *classSet
	instances *instanceSet
	deferred  []ssa.CallInstruction
}

// newWalkState returns the state a block starts from, holding the given classes. Nothing is
// carried in for the instances: an annotation names a class, not the instance it was taken
// on.
func newWalkState(locks *classSet) *walkState {
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
func (pc *lockOrderContext) visitCall(fn *ssa.Function, call ssa.CallInstruction, cur *classSet, curInst *instanceSet, summary *summaryFact, report bool) {
	callee := staticCallee(call)

	// A lock operation changes what is held. Both the standard lock types and a type that
	// wraps its own lock and forwards to it are recognised, since the code bases this is
	// aimed at reach their locks through such a wrapper.
	if obj := funcObject(callee); obj != nil {
		if op := lockOpName(obj.Name()); op != opNone {
			recv, ok := receiverOf(call)
			var class string
			if ok {
				class = pc.classOf(recv)
			}
			// A lock method of a standard lock type on an object with no class is still a
			// lock operation, it just does not participate in the order; consuming it here
			// stops it being treated as an ordinary call.
			if class != "" || (ok && isStandardLock(obj)) {
				switch op {
				case opAcquire:
					if class != "" {
						pc.checkAcquire(call.Pos(), class, displayName(callee), cur.held(), summary, report)
						pc.checkHierarchy(call.Pos(), recv, class, displayName(callee), curInst, report)
						// What has been released is recorded as it stands BEFORE the
						// acquisition: taking the lock back closes the gap.
						summary.addAcquire(class, cur.releasedClasses())
						cur.acquire(class)
						curInst.acquire(recv, class)
					}
				case opRelease:
					cur.release(class, isReceiver(fn, recv))
					curInst.release(recv)
				}
				return
			}
		}
	}

	// Any other call contributes the classes its callee may acquire. A call into a
	// function this analysis has no summary for contributes nothing, which is the
	// documented unsoundness of a modular analysis.
	if callee == nil {
		return
	}
	if isSelfRecursive(fn, callee) {
		// The fixpoint already accounts for the function's own effects.
		return
	}
	var cs summaryFact
	obj := funcObject(callee)
	if obj == nil {
		// A closure: it has no fact, but it is analyzed inline by the SSA walk of the
		// enclosing function, so its effects are already accounted for.
		return
	}
	if !pc.pass.ImportObjectFact(obj, &cs) {
		return
	}
	if pc.calleeIgnored(obj) {
		return
	}
	// A callee called on this function's own receiver acts on the same object, so a lock it
	// releases of its receiver's is one of this function's caller's locks too, and that
	// carries into this summary. A callee called on any other object says nothing about the
	// locks of this one, so only this function's own releases carry.
	onReceiver := false
	if recv, ok := receiverOf(call); ok {
		onReceiver = isReceiver(fn, recv)
	}
	released := cur.releasedClasses()
	for _, a := range cs.Acquires {
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
		if !pc.order.violates(held, acquired) {
			continue
		}
		if !report {
			continue
		}
		if held == acquired {
			pc.maybeFail(pos, "acquiring %s (via %s) while holding %s: two locks of one class must not nest", acquired, via, held)
			continue
		}
		pc.maybeFail(pos, "acquiring %s (via %s) while holding %s: the declared order has %s before %s", acquired, via, held, acquired, held)
	}
}

// entryClasses returns the classes a function is annotated as holding on entry.
//
// The annotations are the ones the checklocks analyzer already reads, so a code base that
// has been annotated for guarded fields gets the ordering check for free: a function
// declared to run with a lock held starts with that lock's class held here.
func (pc *lockOrderContext) entryClasses(fn *ssa.Function) *classSet {
	cs := newClassSet()
	obj := funcObject(fn)
	if obj == nil {
		return cs
	}
	for _, class := range pc.heldOnEntry(obj) {
		cs.enter(class)
	}
	return cs
}

// functionIgnored reports whether reporting is suppressed inside a function.
func (pc *lockOrderContext) functionIgnored(fn *ssa.Function) bool {
	obj := funcObject(fn)
	if obj == nil {
		return false
	}
	return pc.calleeIgnored(obj)
}

// calleeIgnored reports whether a function carries the ignore annotation.
func (pc *lockOrderContext) calleeIgnored(obj *types.Func) bool {
	var ff funcFact
	if !pc.pass.ImportObjectFact(obj, &ff) {
		return false
	}
	return ff.Ignore
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

	// Phase two: report against the settled summaries.
	for _, fn := range fns {
		pc.analyzeFunction(fn, summaries[fn], true)
	}
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
