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
	"maps"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// The lock state read from the engine's records in place of the walk's own.

// recordCounts is the sites the record-based state found no record at, with the reason, and which of them were read.
type recordCounts struct {
	reason map[ssa.Instruction]string
	read   map[ssa.Instruction]bool
}

// recordLocks answers the walk's questions from the engine's record at the last site.
type recordLocks struct {
	pc *lockOrderContext

	// site is the instruction of the last at, and top the function outside every literal around it.
	site ssa.Instruction
	top  *ssa.Function

	// run is the return whose deferred calls are being visited, nil elsewhere.
	run *ssa.RunDefers

	released map[string]bool
}

func topOf(fn *ssa.Function) *ssa.Function {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	return fn
}

func (r *recordLocks) record() (map[pathKey]heldLock, bool) {
	view := r.pc.engineView()
	if view == nil {
		return nil, false
	}
	if d, ok := r.site.(*ssa.Defer); ok && r.run != nil {
		return view.deferHeldSet(r.run, d)
	}
	return view.heldSet(r.site)
}

func (r *recordLocks) atReturn(run *ssa.RunDefers) { r.run = run }

func (r *recordLocks) at(fn *ssa.Function, inst ssa.Instruction) {
	r.site, r.top = inst, topOf(fn)
	if _, ok := r.record(); ok {
		return
	}
	c := &r.pc.records
	if c.reason == nil {
		c.reason = make(map[ssa.Instruction]string)
	}
	c.reason[inst] = r.pc.noRecordReason(inst)
}

func (pc *lockOrderContext) noRecordReason(inst ssa.Instruction) string {
	view := pc.engineView()
	if view == nil {
		return "nil view"
	}
	if _, ok := view.heldSet(inst); ok {
		return "return not run"
	}
	fn := inst.Parent()
	if why := view.whyNone(fn); why != "" {
		return why
	}
	if _, walked := view.entryOf(fn); walked {
		return "walked, site unrecorded"
	}
	return "not walked"
}

func (r *recordLocks) held() []string {
	if r.site == nil {
		return nil
	}
	rec, ok := r.record()
	if !ok {
		c := &r.pc.records
		if c.read == nil {
			c.read = make(map[ssa.Instruction]bool)
		}
		c.read[r.site] = true
		return nil
	}
	classes := make(map[string]struct{})
	for _, l := range rec {
		if l.assumed {
			continue
		}
		cs, _ := r.pc.engineClasses(r.top, l)
		for _, c := range cs {
			classes[c] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(classes))
}

func (r *recordLocks) releasedClasses() []string {
	return slices.Sorted(maps.Keys(r.released))
}

func (r *recordLocks) acquire(class string) {
	delete(r.released, class)
}

func (r *recordLocks) release(class string, receiverOwned bool) {
	call, ok := r.site.(ssa.CallInstruction)
	if class == "" || !receiverOwned || !ok {
		return
	}
	if r.pc.callerOwned(r.top, call, class) {
		r.released[class] = true
	}
}

func (r *recordLocks) fork() siteLocks {
	return &recordLocks{pc: r.pc, site: r.site, top: r.top, run: r.run, released: maps.Clone(r.released)}
}

func (r *recordLocks) merge(other siteLocks) {
	o := other.(*recordLocks)
	for k := range r.released {
		if !o.released[k] {
			delete(r.released, k)
		}
	}
}

func (r *recordLocks) after(_ *ssa.Function, call ssa.CallInstruction, _ bool) {
	if len(r.released) == 0 {
		return
	}
	view := r.pc.engineView()
	for _, gained := range []map[pathKey]lockPath{view.gainsOf(call), view.opGainsOf(call)} {
		for _, p := range gained {
			classes, _ := r.pc.engineClasses(r.top, heldLock{path: p})
			for _, c := range classes {
				delete(r.released, c)
			}
		}
	}
}

// callerOwned says whether the record at call, taken before it, leaves class known and held by no lock the body took.
func (pc *lockOrderContext) callerOwned(fn *ssa.Function, call ssa.CallInstruction, class string) bool {
	view := pc.engineView()
	rec, ok := view.heldSet(call)
	if !ok || view.inexact[call] {
		return false
	}
	sv := view.sites[call]
	if sv == nil || sv.top {
		return false
	}
	for _, p := range sv.marker {
		if c, _ := pc.engineClass(p); c == class {
			return false
		}
	}
	for _, l := range rec {
		if l.assumed || l.entry {
			continue
		}
		if classes, _ := pc.engineClasses(fn, l); slices.Contains(classes, class) {
			return false
		}
	}
	return true
}
