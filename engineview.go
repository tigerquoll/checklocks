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
}

// engineView is what the engine records per pass for lockorder and lockblocking: the locks held
// at each call and channel operation of a top-level function, joined over every state the replay
// reaches the site in; the locks each such function is entered with; and the functions with no records.
type engineView struct {
	held   map[ssa.Instruction]map[pathKey]heldLock
	entry  map[*ssa.Function][]heldLock
	gaveUp map[*ssa.Function]string
}

// add joins the locks held at inst in one state into the site's record. A lock counts as
// assumed only if every state that holds it assumes it. An empty set is still a record.
func (e *engineView) add(inst ssa.Instruction, locks, entry map[pathKey]lockInfo) {
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

// setEntry records the locks fn is entered with.
func (e *engineView) setEntry(fn *ssa.Function, locks map[pathKey]lockInfo) {
	if e.entry == nil {
		e.entry = make(map[*ssa.Function][]heldLock)
	}
	held := make(map[pathKey]heldLock, len(locks))
	for key, info := range locks {
		held[key] = heldLock{path: info.path, assumed: info.assumed, entry: true}
	}
	e.entry[fn] = sortedHeld(held)
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
	if e.gaveUp == nil {
		e.gaveUp = make(map[*ssa.Function]string)
	}
	e.gaveUp[fn] = reason
}

// heldAt returns the locks the engine held at inst, and false when it recorded none or gave up on inst's function.
func (e *engineView) heldAt(inst ssa.Instruction) ([]heldLock, bool) {
	rec, ok := e.held[inst]
	if !ok || e.gaveUp[inst.Parent()] != "" {
		return nil, false
	}
	return sortedHeld(rec), true
}

// entryOf returns the locks the engine entered fn with, and false when it has no record of fn.
func (e *engineView) entryOf(fn *ssa.Function) ([]heldLock, bool) {
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
	if reason := e.gaveUp[fn]; reason != "" {
		return "gave up: " + reason
	}
	return ""
}

// recordHeld records the locks held at inst, a call or channel operation of the function being replayed.
func (pc *passContext) recordHeld(inst ssa.Instruction, wc *walkContext, ls *lockState) {
	fn := wc.fn
	if pc.emit != emitReplay || inst == nil || fn.Parent() != nil || inst.Parent() != fn || len(pc.records) == 0 {
		return
	}
	pc.engine.add(inst, ls.lockedMutexes, pc.records[0].parent.lockedMutexes)
}

// recordEntry records the locks the top-level function about to be replayed is entered with.
func (pc *passContext) recordEntry() {
	if len(pc.records) == 0 {
		return
	}
	r := pc.records[0]
	if r.wc.fn.Parent() != nil {
		return
	}
	pc.engine.setEntry(r.wc.fn, r.parent.lockedMutexes)
}

// recordGaveUp records why the walk of fn was abandoned.
func (pc *passContext) recordGaveUp(fn *ssa.Function) {
	reason := fmt.Sprintf("more than %d lock states reach one loop", maxHeadStates)
	if pc.overBudget {
		reason = fmt.Sprintf("more than %d block walks", maxFunctionWalks)
	}
	pc.engine.giveUp(fn, reason)
}
