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
	"strings"

	"golang.org/x/tools/go/ssa"
)

// A method that reads or writes a guarded field of its receiver without taking
// the lock itself is not a mistake to be reported where it is written: it is a
// method whose caller must hold the lock. That is what +checklocks states, and
// it follows from the field's own guard, so it is derived rather than restated.
//
// This is the dual of the derived exclusion. One says a method takes the lock,
// so a caller must not hold it; the other says a method assumes the lock, so a
// caller must. A method cannot do both for the same lock, and the derivation
// of one excludes the other.
//
// The requirement comes from the guard on the field, never from how often the
// method is observed to be called under a lock. Inferring from frequency would
// let code that violates the invariant count as evidence against it.

var (
	// enableSynthPreconds derives the precondition of a method that
	// assumes its receiver's lock. On by default: it is a sound
	// derivation from a guard the author already wrote.
	enableSynthPreconds = true
)

func init() {
	Analyzer.Flags.BoolVar(&enableSynthPreconds, "synthpreconds", true,
		"derive the caller precondition of a method that assumes its receiver's lock")
}

// requiredLock is a lock a method assumes its caller holds.
type requiredLock struct {
	// fieldList is the traversal from the receiver to the lock.
	fieldList fieldList

	// exclusive is set when the method writes a field the lock guards, so
	// a read lock is not enough.
	exclusive bool
}

// synthesizePreconditions derives and exports the preconditions of the
// package's methods.
//
// The fixpoint is needed for the same reason as the exclusions': a method that
// reaches a guarded field through another method assumes the lock too, and the
// callee may be analyzed after the caller.
func (pc *passContext) synthesizePreconditions(fns []*ssa.Function) {
	if !enableSynthPreconds {
		return
	}
	found := make(map[*ssa.Function]map[int]requiredLock)
	for round := 0; round < maxFixpointRounds; round++ {
		changed := false
		for _, fn := range fns {
			needs := pc.requiredLocksOf(fn, found)
			if len(needs) == 0 {
				continue
			}
			if before, ok := found[fn]; ok && len(before) == len(needs) {
				continue
			}
			found[fn] = needs
			changed = true
		}
		if !changed {
			break
		}
	}
	for fn, needs := range found {
		pc.exportSynthesizedPreconditions(fn, needs)
	}
}

// requiredLocksOf returns the locks of its own receiver a method assumes.
func (pc *passContext) requiredLocksOf(fn *ssa.Function, found map[*ssa.Function]map[int]requiredLock) map[int]requiredLock {
	recv := receiverStruct(fn)
	if recv == nil || len(fn.Blocks) == 0 {
		return nil
	}
	structType, ok := resolveStruct(recv.Type())
	if !ok {
		return nil
	}

	// A lock the method takes itself is not one it assumes. Deriving both
	// for the same lock would have the method requiring what it also
	// forbids.
	taken := pc.selfLocksOf(fn, nil)

	out := make(map[int]requiredLock)
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			switch x := inst.(type) {
			case *ssa.FieldAddr:
				pc.noteGuardedUse(x, x.X, x.Field, isWrite(x), recv, structType, taken, out)
			case *ssa.Field:
				pc.noteGuardedUse(x, x.X, x.Field, false, recv, structType, taken, out)
			case ssa.CallInstruction:
				pc.noteInheritedPrecond(x, recv, structType, taken, found, out)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// noteGuardedUse records the lock guarding a field of the receiver that the
// method uses.
func (pc *passContext) noteGuardedUse(inst ssa.Value, base ssa.Value, index int, write bool, recv *ssa.Parameter, structType *types.Struct, taken map[int]selfLock, out map[int]requiredLock) {
	if unwrapAssertValue(base) != ssa.Value(recv) {
		// A guarded field of some other object says nothing about this
		// receiver's lock, and is reported where it is used.
		return
	}
	if index >= structType.NumFields() {
		return
	}
	field := structType.Field(index)
	var lgf lockGuardFacts
	pc.importLockGuardFacts(field, &lgf)
	if len(lgf.GuardedBy) == 0 {
		return
	}
	// The guard names a lock; resolve the name against the receiver's own
	// structure. This is deliberately by name rather than by inspecting
	// the resolver, so that a guard written on the field and one expanded
	// from a declaration on the structure are treated alike.
	for name := range lgf.GuardedBy {
		fl, _, objs, ok := pc.resolveFieldListParts(field.Pos(), structType, strings.Split(name, "."))
		if !ok || len(objs) == 0 {
			continue
		}
		lockIndex, ok := lockFieldIndexOf(structType, name)
		if !ok {
			continue
		}
		if _, self := taken[lockIndex]; self {
			continue
		}
		prev, seen := out[lockIndex]
		out[lockIndex] = requiredLock{
			fieldList: fl,
			exclusive: write || (seen && prev.exclusive),
		}
	}
}

// lockFieldIndexOf returns the index of the lock a guard name refers to, when
// it is a direct field of the structure.
//
// A guard that reaches through another object is not a precondition this
// receiver can state, and is left to be checked where it is used.
func lockFieldIndexOf(structType *types.Struct, name string) (int, bool) {
	if strings.Contains(name, ".") {
		return 0, false
	}
	for i := 0; i < structType.NumFields(); i++ {
		if structType.Field(i).Name() == name {
			return i, true
		}
	}
	return 0, false
}

// noteInheritedPrecond records the locks a callee on the same receiver assumes.
//
// Both the derivation in progress and an annotation already written are
// consulted, so a hand-written precondition propagates to callers exactly as a
// derived one does.
func (pc *passContext) noteInheritedPrecond(call ssa.CallInstruction, recv *ssa.Parameter, structType *types.Struct, taken map[int]selfLock, found map[*ssa.Function]map[int]requiredLock, out map[int]requiredLock) {
	common := call.Common()
	callee := common.StaticCallee()
	if callee == nil || !callsOwnMethod(common, recv) {
		return
	}
	inherit := func(index int, rl requiredLock) {
		if _, self := taken[index]; self {
			return
		}
		prev, seen := out[index]
		rl.exclusive = rl.exclusive || (seen && prev.exclusive)
		out[index] = rl
	}
	if needs, ok := found[callee]; ok {
		for index, rl := range needs {
			inherit(index, rl)
		}
		return
	}
	// An annotated callee, or one whose precondition was derived when its
	// own package was analyzed.
	obj := callee.Object()
	funcObj, ok := obj.(*types.Func)
	if !ok {
		return
	}
	var lff lockFunctionFacts
	pc.importLockFunctionFacts(funcObj, &lff)
	for _, fg := range lff.HeldOnEntry {
		pg, ok := fg.Resolver.(*parameterGuard)
		if !ok || pg.Index != 0 || len(pg.FieldList) != 1 {
			continue
		}
		fs, ok := pg.FieldList[0].(*fieldStruct)
		if !ok || fs.Field >= structType.NumFields() {
			continue
		}
		inherit(fs.Field, requiredLock{fieldList: pg.FieldList, exclusive: fg.Exclusive})
	}
}

// exportSynthesizedPreconditions adds the derived preconditions to a method's
// facts.
//
// An annotation on the method wins throughout: a lock it is declared to
// exclude, to acquire or to hold is not one to be derived, and a method that
// is ignored is left alone.
func (pc *passContext) exportSynthesizedPreconditions(fn *ssa.Function, needs map[int]requiredLock) {
	obj := fn.Object()
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
	for index, rl := range needs {
		name := recv.Name() + "." + structType.Field(index).Name()
		if _, ok := lff.HeldOnEntry[name]; ok {
			continue
		}
		if _, ok := lff.HeldOnExit[name]; ok {
			continue
		}
		if _, ok := lff.ExcludedOnEntry[name]; ok {
			continue
		}
		info := functionGuardInfo{
			Resolver:  &parameterGuard{Index: 0, FieldList: rl.fieldList},
			Exclusive: rl.exclusive,
		}
		if lff.HeldOnEntry == nil {
			lff.HeldOnEntry = make(map[string]functionGuardInfo)
		}
		if lff.HeldOnExit == nil {
			lff.HeldOnExit = make(map[string]functionGuardInfo)
		}
		// A precondition is also a postcondition: the method does not
		// release what it was given, so the lock is still held when it
		// returns. This is what the annotation does, and the return
		// balance check depends on it.
		lff.HeldOnEntry[name] = info
		lff.HeldOnExit[name] = info
		changed = true
	}
	if changed {
		pc.pass.ExportObjectFact(funcObj, &lff)
	}
}
