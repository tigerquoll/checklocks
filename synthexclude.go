// Copyright 2026 The gVisor Authors.
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
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// A method that takes its own receiver's lock cannot be called by anyone
// already holding it: the second acquisition deadlocks. That is exactly what
// +checklocksexclude states, and it is derivable from the body, so it is
// derived rather than restated.
//
// This is sounder than the annotation it replaces. A self-acquiring method has
// no legitimate caller holding the lock, so a method that was never annotated
// was not exempt, it was unchecked; synthesis closes those gaps rather than
// preserving them.
//
// The derivation is from the lock the body takes, never from how the method
// happens to be called. Nothing here counts call sites.

var (
	// enableSynthExcludes derives the exclusion of a self-locking method.
	// On by default: it is a sound derivation, and it only adds checking.
	enableSynthExcludes = true
)

func init() {
	Analyzer.Flags.BoolVar(&enableSynthExcludes, "synthexcludes", true,
		"derive the caller exclusion of a method that takes its own receiver's lock")
}

// selfLock describes a lock a method takes on its own receiver.
type selfLock struct {
	// field is the index of the lock field on the receiver's struct.
	field int

	// fieldList is the traversal from the receiver to the lock.
	fieldList fieldList

	// exclusive is set when the method takes the lock for writing. A
	// reader only excludes a caller holding it for writing.
	exclusive bool
}

// synthesizeExcludes derives and exports the exclusions of the package's
// methods.
//
// A fixpoint is needed because the property travels: a method that calls
// another method of the same receiver which takes the lock takes it too, and
// the callee may be analyzed after the caller. The set only grows over a
// finite set of methods, so it settles.
func (pc *passContext) synthesizeExcludes(fns []*ssa.Function) {
	if !enableSynthExcludes {
		return
	}
	found := make(map[*ssa.Function]map[int]selfLock)
	for round := 0; round < maxFixpointRounds; round++ {
		changed := false
		for _, fn := range fns {
			locks := pc.selfLocksOf(fn, found)
			if len(locks) == 0 {
				continue
			}
			if before, ok := found[fn]; ok && len(before) == len(locks) {
				// The set only grows, so an unchanged size is
				// an unchanged set.
				continue
			}
			found[fn] = locks
			changed = true
		}
		if !changed {
			break
		}
	}
	for fn, locks := range found {
		pc.exportSynthesizedExcludes(fn, locks)
	}
}

// selfLocksOf returns the locks of its own receiver that a method takes,
// directly or through a method of the same receiver.
func (pc *passContext) selfLocksOf(fn *ssa.Function, found map[*ssa.Function]map[int]selfLock) map[int]selfLock {
	recv := receiverStruct(fn)
	if recv == nil {
		return nil
	}
	structType, ok := resolveStruct(recv.Type())
	if !ok || len(fn.Blocks) == 0 {
		return nil
	}
	out := make(map[int]selfLock)
	pc.collectSelfLocks(fn, fn, recv, structType, nil, found, out, 0)
	if len(out) == 0 {
		return nil
	}
	return out
}

// maxClosureDepth bounds the descent into nested closures.
const maxClosureDepth = 4

// collectSelfLocks walks a body and the closures it creates, recording the
// locks of the receiver it takes.
func (pc *passContext) collectSelfLocks(fn, body *ssa.Function, recv *ssa.Parameter, structType *types.Struct, captured map[*ssa.FreeVar]ssa.Value, found map[*ssa.Function]map[int]selfLock, out map[int]selfLock, depth int) {
	for _, block := range body.Blocks {
		for _, inst := range block.Instrs {
			call, ok := inst.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := call.Common()
			if isLockAcquire(pc.pass, common) {
				pc.noteDirectSelfLock(common, recv, structType, captured, out)
				continue
			}
			// A method of the same receiver that takes the lock
			// takes it on this receiver too.
			callee := common.StaticCallee()
			if callee == nil || callee == fn {
				continue
			}
			if !callsOwnMethod(common, recv, captured) {
				continue
			}
			for index, sl := range pc.calleeSelfLocks(callee, found) {
				merge(out, index, sl)
			}
		}
	}
	if depth >= maxClosureDepth {
		return
	}
	for _, anon := range body.AnonFuncs {
		captures, spawned := closureCaptures(body, anon, captured)
		if spawned || captures == nil {
			continue
		}
		pc.collectSelfLocks(fn, anon, recv, structType, captures, found, out, depth+1)
	}
}

// noteDirectSelfLock records a lock operation whose receiver is a lock field of
// this method's receiver.
func (pc *passContext) noteDirectSelfLock(common *ssa.CallCommon, recv *ssa.Parameter, structType *types.Struct, captured map[*ssa.FreeVar]ssa.Value, out map[int]selfLock) {
	args := common.Args
	if common.Method != nil {
		args = append([]ssa.Value{common.Value}, args...)
	}
	if len(args) == 0 {
		return
	}
	fa, ok := underlyingFieldAddr(args[0])
	if !ok || !resolvesToReceiver(fa.X, recv, captured) {
		return
	}
	if fa.Field >= structType.NumFields() {
		return
	}
	field := structType.Field(fa.Field)
	if !isLockTypeIn(pc.pass, field.Type()) {
		return
	}
	merge(out, fa.Field, selfLock{
		field:     fa.Field,
		fieldList: fieldList{pc.fieldEntryFor(field, fa.Field)},
		exclusive: lockOpIsWrite(common),
	})
}

// calleeSelfLocks returns the self locks of a callee, from this package's
// working set or from the fact a previously analyzed package exported.
func (pc *passContext) calleeSelfLocks(callee *ssa.Function, found map[*ssa.Function]map[int]selfLock) map[int]selfLock {
	if locks, ok := found[callee]; ok {
		return locks
	}
	// A callee outside this package carries its exclusions as facts
	// already, and the call site consults them in the ordinary way, so
	// there is nothing to propagate here.
	return nil
}

// merge folds a lock into the set, keeping the stronger exclusivity.
func merge(out map[int]selfLock, index int, sl selfLock) {
	if prev, ok := out[index]; ok {
		sl.exclusive = sl.exclusive || prev.exclusive
	}
	out[index] = sl
}

// callsOwnMethod reports whether a call is a method call on this receiver.
func callsOwnMethod(common *ssa.CallCommon, recv *ssa.Parameter, captured map[*ssa.FreeVar]ssa.Value) bool {
	args := common.Args
	if common.Method != nil {
		args = append([]ssa.Value{common.Value}, args...)
	}
	return len(args) > 0 && resolvesToReceiver(args[0], recv, captured)
}

// lockOpIsWrite reports whether a lock operation takes the lock for writing.
func lockOpIsWrite(common *ssa.CallCommon) bool {
	var fn *types.Func
	if common.Method != nil {
		fn = common.Method
	} else if sf, ok := common.Value.(*ssa.Function); ok && sf.Object() != nil {
		fn, _ = sf.Object().(*types.Func)
	}
	if fn == nil {
		return false
	}
	switch fn.Name() {
	case "Lock", "NestedLock":
		return true
	}
	return false
}

// receiverStruct returns the receiver parameter of a method.
func receiverStruct(fn *ssa.Function) *ssa.Parameter {
	if fn.Signature.Recv() == nil || len(fn.Params) == 0 {
		return nil
	}
	return fn.Params[0]
}

// exportSynthesizedExcludes adds the derived exclusions to a method's facts.
//
// An annotation on the method wins: a lock the method is declared to hold on
// entry is not one it acquires, and re-exporting it as excluded would state
// the opposite of what the author wrote. A declared exclusion is left alone
// for the same reason, and because it may be broader than the derived one.
func (pc *passContext) exportSynthesizedExcludes(fn *ssa.Function, locks map[int]selfLock) {
	obj := fn.Object()
	if obj == nil {
		return
	}
	funcObj, ok := obj.(*types.Func)
	if !ok {
		return
	}
	var lff lockFunctionFacts
	pc.importLockFunctionFacts(funcObj, &lff)
	if lff.Ignore {
		return
	}
	recv := receiverStruct(fn)
	structType, ok := resolveStruct(recv.Type())
	if !ok {
		return
	}
	changed := false
	for index, sl := range locks {
		name := recv.Name() + "." + structType.Field(index).Name()
		if _, held := lff.HeldOnEntry[name]; held {
			continue
		}
		if _, held := lff.HeldOnExit[name]; held {
			continue
		}
		if _, already := lff.ExcludedOnEntry[name]; already {
			continue
		}
		if lff.ExcludedOnEntry == nil {
			lff.ExcludedOnEntry = make(map[string]functionGuardInfo)
		}
		lff.ExcludedOnEntry[name] = functionGuardInfo{
			Resolver: &parameterGuard{Index: 0, FieldList: sl.fieldList},
			// A writer excludes a caller holding the lock in any
			// mode; a reader only excludes one holding it for
			// writing.
			Exclusive: !sl.exclusive,
		}
		changed = true
	}
	if changed {
		pc.pass.ExportObjectFact(funcObj, &lff)
	}
}

// The receiver is not always the parameter itself.
//
// A method whose body contains a closure capturing the receiver has that
// receiver spilled to a local by the ssa builder, so an acquisition written
// plainly in the method reaches the lock through a load of that local rather
// than through the parameter. A closure may also take the lock itself, in
// which case the receiver arrives as one of its free variables. Both are the
// same lock on the same object, and neither was attributed before.
//
// The resolution is deliberately narrow: a local is followed only when exactly
// one value is ever stored into it, so a variable that holds different objects
// at different times resolves to nothing rather than to the wrong one.

// maxCaptureHops bounds the walk back to the receiver.
const maxCaptureHops = 16

// resolvesToReceiver reports whether v is the receiver, reached through the
// spills and captures the ssa builder introduces.
func resolvesToReceiver(v ssa.Value, recv *ssa.Parameter, captured map[*ssa.FreeVar]ssa.Value) bool {
	cur := v
	for i := 0; i < maxCaptureHops; i++ {
		switch x := unwrapAssertValue(cur).(type) {
		case *ssa.Parameter:
			return x == recv
		case *ssa.FreeVar:
			outer, ok := captured[x]
			if !ok {
				return false
			}
			cur = outer
		case *ssa.Alloc:
			stored, ok := singleStore(x)
			if !ok {
				return false
			}
			cur = stored
		default:
			return false
		}
	}
	return false
}

// singleStore returns the only value ever stored into a local.
func singleStore(alloc *ssa.Alloc) (ssa.Value, bool) {
	refs := alloc.Referrers()
	if refs == nil {
		return nil, false
	}
	var found ssa.Value
	for _, inst := range *refs {
		st, ok := inst.(*ssa.Store)
		if !ok || st.Addr != ssa.Value(alloc) {
			continue
		}
		if found != nil {
			// More than one value passes through it; which one is
			// live here is not something this can answer.
			return nil, false
		}
		found = st.Val
	}
	return found, found != nil
}

// closureCaptures returns the free variables of an anonymous function bound to
// the values the enclosing function passed, along with whether the closure is
// started on another goroutine.
//
// A closure is attributed to the method only when the method runs it: invoked
// directly or deferred, and not otherwise handed on. One started with "go"
// belongs to the new goroutine, and one that is returned or stored is run by
// whoever received it, at a time this cannot see; in neither case is a caller
// holding the lock deadlocked. This matches how the ordering analysis treats a
// spawned acquisition, and reporting it would punish the sanctioned way of
// breaking a nesting.
func closureCaptures(parent *ssa.Function, anon *ssa.Function, outer map[*ssa.FreeVar]ssa.Value) (map[*ssa.FreeVar]ssa.Value, bool) {
	for _, block := range parent.Blocks {
		for _, inst := range block.Instrs {
			mc, ok := inst.(*ssa.MakeClosure)
			if !ok || mc.Fn != ssa.Value(anon) {
				continue
			}
			// The closure's acquisitions belong to this method only
			// if this method runs it. A closure that is returned,
			// stored or handed on is run by someone else, at a
			// time this cannot see, and a caller holding the lock
			// is not deadlocked by it. One started with "go"
			// likewise belongs to the new goroutine.
			invoked, escapes := false, false
			if refs := mc.Referrers(); refs != nil {
				for _, ref := range *refs {
					switch r := ref.(type) {
					case *ssa.Call:
						if r.Common().Value == ssa.Value(mc) {
							invoked = true
						} else {
							escapes = true
						}
					case *ssa.Defer:
						if r.Common().Value == ssa.Value(mc) {
							invoked = true
						} else {
							escapes = true
						}
					default:
						escapes = true
					}
				}
			}
			spawned := !invoked || escapes
			captures := make(map[*ssa.FreeVar]ssa.Value, len(anon.FreeVars))
			for i, fv := range anon.FreeVars {
				if i < len(mc.Bindings) {
					captures[fv] = mc.Bindings[i]
				}
			}
			// A capture may itself be a free variable of the
			// enclosing closure.
			for fv, v := range outer {
				if _, ok := captures[fv]; !ok {
					captures[fv] = v
				}
			}
			return captures, spawned
		}
	}
	return nil, false
}
