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
	"slices"

	"golang.org/x/tools/go/ssa"
)

// heldLock is a lock the engine holds at a site.
type heldLock struct {
	path lockPath

	// assumed says the lock is held by assertion, not by acquisition.
	assumed bool

	// entry says the lock was already held when the function was entered.
	entry bool

	// guards names the entry guards the lock came from; set on entry locks only.
	guards []string
}

// engineView is what the engine records per pass for lockorder and lockblocking, for top-level
// functions only: locks held at each call and channel operation, entry locks, and what each
// call's annotations acquired and released. A nil view records and reports nothing.
type engineView struct {
	held   map[ssa.Instruction]map[pathKey]heldLock
	entry  map[*ssa.Function][]heldLock
	gaveUp map[*ssa.Function]string

	// gains is the real locks each call's acquire annotations took, joined over the states.
	gains map[ssa.Instruction]map[pathKey]lockPath

	// releases is the real locks each call's release annotations dropped, in some state.
	releases map[ssa.Instruction]map[pathKey]bool
}

// add joins the locks held at inst in one state into the site's record. A lock counts as
// assumed only if every state that holds it assumes it. An empty set is still a record.
func (e *engineView) add(inst ssa.Instruction, locks, entry map[pathKey]lockInfo) {
	if e == nil {
		return
	}
	if e.held == nil {
		e.held = make(map[ssa.Instruction]map[pathKey]heldLock)
	}
	rec := e.held[inst]
	if rec == nil {
		rec = make(map[pathKey]heldLock)
		e.held[inst] = rec
	}
	for key, info := range locks {
		_, atEntry := entry[key]
		if cur, ok := rec[key]; ok {
			info.assumed = info.assumed && cur.assumed
			atEntry = atEntry || cur.entry
		}
		rec[key] = heldLock{path: info.path, assumed: info.assumed, entry: atEntry}
	}
}

// setEntry records the locks fn is entered with, and the guards each came from.
func (e *engineView) setEntry(fn *ssa.Function, locks map[pathKey]lockInfo, guards map[pathKey][]string) {
	if e == nil {
		return
	}
	if e.entry == nil {
		e.entry = make(map[*ssa.Function][]heldLock)
	}
	held := make(map[pathKey]heldLock, len(locks))
	for key, info := range locks {
		held[key] = heldLock{path: info.path, assumed: info.assumed, entry: true, guards: guards[key]}
	}
	e.entry[fn] = sortedHeld(held)
}

// addGain records that the annotations of the call at inst took path.
func (e *engineView) addGain(inst ssa.Instruction, path lockPath) {
	if e == nil {
		return
	}
	if e.gains == nil {
		e.gains = make(map[ssa.Instruction]map[pathKey]lockPath)
	}
	if e.gains[inst] == nil {
		e.gains[inst] = make(map[pathKey]lockPath)
	}
	e.gains[inst][path.Key()] = path
}

// addRelease records that the annotations of the call at inst released path.
func (e *engineView) addRelease(inst ssa.Instruction, path lockPath) {
	if e == nil {
		return
	}
	if e.releases == nil {
		e.releases = make(map[ssa.Instruction]map[pathKey]bool)
	}
	if e.releases[inst] == nil {
		e.releases[inst] = make(map[pathKey]bool)
	}
	e.releases[inst][path.Key()] = true
}

// sortedHeld returns the locks in key order.
func sortedHeld(locks map[pathKey]heldLock) []heldLock {
	keys := make([]pathKey, 0, len(locks))
	for k := range locks {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]heldLock, len(keys))
	for i, k := range keys {
		out[i] = locks[k]
	}
	return out
}

// giveUp marks fn as having no records.
func (e *engineView) giveUp(fn *ssa.Function, reason string) {
	if e == nil {
		return
	}
	if e.gaveUp == nil {
		e.gaveUp = make(map[*ssa.Function]string)
	}
	e.gaveUp[fn] = reason
}

// usable says whether the engine has records for inst's function.
func (e *engineView) usable(inst ssa.Instruction) bool {
	return e != nil && e.gaveUp[inst.Parent()] == ""
}

// heldAt returns the locks the engine held at inst in key order, and false when it recorded none or gave up on inst's function.
func (e *engineView) heldAt(inst ssa.Instruction) ([]heldLock, bool) {
	rec, ok := e.heldSet(inst)
	if !ok {
		return nil, false
	}
	return sortedHeld(rec), true
}

// heldSet is heldAt without the order.
func (e *engineView) heldSet(inst ssa.Instruction) (map[pathKey]heldLock, bool) {
	if !e.usable(inst) {
		return nil, false
	}
	rec, ok := e.held[inst]
	return rec, ok
}

// gainsOf returns the locks the call's acquire annotations took, or nil.
func (e *engineView) gainsOf(inst ssa.Instruction) map[pathKey]lockPath {
	if !e.usable(inst) {
		return nil
	}
	return e.gains[inst]
}

// releasesOf returns the locks the call's release annotations dropped in some state, or nil.
func (e *engineView) releasesOf(inst ssa.Instruction) map[pathKey]bool {
	if !e.usable(inst) {
		return nil
	}
	return e.releases[inst]
}

// entryOf returns the locks the engine entered fn with, and false when it has no record of fn.
func (e *engineView) entryOf(fn *ssa.Function) ([]heldLock, bool) {
	if e == nil {
		return nil, false
	}
	locks, ok := e.entry[fn]
	if !ok || e.gaveUp[fn] != "" {
		return nil, false
	}
	return locks, true
}

// whyNone says why the engine has no record in fn: "literal", "gave up: <reason>", or "".
func (e *engineView) whyNone(fn *ssa.Function) string {
	if fn.Parent() != nil {
		return "literal"
	}
	if e == nil {
		return ""
	}
	if reason := e.gaveUp[fn]; reason != "" {
		return "gave up: " + reason
	}
	return ""
}

// replaying returns the engine, and whether inst is a site of the top-level function whose
// replay is running.
func (pc *passContext) replaying(inst ssa.Instruction) (*engineView, bool) {
	if pc.engine == nil || pc.emit != emitReplay || inst == nil || len(pc.records) == 0 {
		return nil, false
	}
	fn := pc.records[0].wc.fn
	return pc.engine, fn.Parent() == nil && inst.Parent() == fn
}

// recordHeld records the locks held at inst, a call or channel operation of the function being replayed.
func (pc *passContext) recordHeld(inst ssa.Instruction, wc *walkContext, ls *lockState) {
	fn := wc.fn
	if pc.engine == nil || pc.emit != emitReplay || inst == nil || fn.Parent() != nil || inst.Parent() != fn || len(pc.records) == 0 {
		return
	}
	pc.engine.add(inst, ls.lockedMutexes, pc.records[0].parent.lockedMutexes)
}

// recordGain records a lock the annotations of the call at inst took, or made real.
func (pc *passContext) recordGain(inst ssa.Instruction, path lockPath) {
	if e, ok := pc.replaying(inst); ok {
		e.addGain(inst, path)
	}
}

// recordRelease records a real lock the annotations of the call at inst dropped.
func (pc *passContext) recordRelease(inst ssa.Instruction, path lockPath) {
	if e, ok := pc.replaying(inst); ok {
		e.addRelease(inst, path)
	}
}

// recordEntry records the locks the top-level function about to be replayed is entered with.
func (pc *passContext) recordEntry() {
	if pc.engine == nil || len(pc.records) == 0 {
		return
	}
	r := pc.records[0]
	if r.wc.fn.Parent() != nil {
		return
	}
	pc.engine.setEntry(r.wc.fn, r.parent.lockedMutexes, pc.entryGuards)
}

// recordGaveUp records why the walk of fn was abandoned.
func (pc *passContext) recordGaveUp(fn *ssa.Function) {
	reason := fmt.Sprintf("more than %d lock states reach one block in a loop", maxCycleStates)
	if pc.overBudget {
		reason = fmt.Sprintf("more than %d block walks", maxFunctionWalks)
	}
	pc.engine.giveUp(fn, reason)
}
