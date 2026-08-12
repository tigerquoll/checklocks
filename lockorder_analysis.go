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

	// The set reaching each block, built up as the blocks are visited.
	in := make([]*classSet, len(fn.Blocks))
	in[0] = entry

	// The instances holding a lock, tracked beside the classes for the hierarchical
	// direction check. Nothing is carried in at the entry: an annotation names a class,
	// not the instance it was taken on.
	inInst := make([]*instanceSet, len(fn.Blocks))
	inInst[0] = newInstanceSet()

	// The calls deferred so far. A defer is registered when it is reached and runs at the
	// return, so the list accumulates across the blocks on the way there.
	var deferred []ssa.CallInstruction
	for _, b := range fn.Blocks {
		cur := in[b.Index]
		if cur == nil {
			// Unreachable from the entry as far as this walk is concerned; start clean
			// rather than skipping, so its calls are still checked.
			cur = newClassSet()
		}
		cur = cur.fork()
		curInst := inInst[b.Index]
		if curInst == nil {
			curInst = newInstanceSet()
		}
		curInst = curInst.fork()

		for _, instr := range b.Instrs {
			switch t := instr.(type) {
			case *ssa.Defer:
				deferred = append(deferred, t)
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
			for i := len(deferred) - 1; i >= 0; i-- {
				pc.visitCall(fn, deferred[i], exit, exitInst, summary, report && !ignored)
			}
		}

		// Propagate to the successors.
		for _, succ := range b.Succs {
			if in[succ.Index] == nil {
				in[succ.Index] = cur.fork()
			} else {
				in[succ.Index].merge(cur)
			}
			if inInst[succ.Index] == nil {
				inInst[succ.Index] = curInst.fork()
			} else {
				inInst[succ.Index].merge(curInst)
			}
		}
	}
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
						pc.checkAcquire(call.Pos(), class, displayName(callee), cur, summary, report)
						pc.checkHierarchy(call.Pos(), recv, class, displayName(callee), curInst, report)
						cur.acquire(class)
						curInst.acquire(recv, class)
						summary.addAcquire(class)
					}
				case opRelease:
					cur.release(class)
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
	for _, class := range cs.Acquires {
		pc.checkAcquire(call.Pos(), class, displayName(callee), cur, summary, report)
		summary.addAcquire(class)
	}
}

// checkAcquire records the pair and reports if the acquisition breaks the order.
func (pc *lockOrderContext) checkAcquire(pos token.Pos, acquired, via string, cur *classSet, summary *summaryFact, report bool) {
	for _, held := range cur.held() {
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
		cs.acquire(class)
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
			if len(next.Acquires) != len(before.Acquires) || len(next.Pairs) != len(before.Pairs) {
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
