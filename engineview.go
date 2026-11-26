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

// engineView is what the engine records per pass for lockorder and lockblocking: locks held at
// each call and channel operation, entry locks, and what each call's annotations acquired and
// released. Literal sites carry held, gains, op gains and releases too. A nil view records and reports nothing.
type engineView struct {
	// held is the locks held at each site in some state; a client that suppresses or derives must use query.
	held   map[ssa.Instruction]map[pathKey]heldLock
	entry  map[*ssa.Function][]heldLock
	gaveUp map[*ssa.Function]string

	// gains is the real locks each call's acquire annotations took, joined over the states.
	gains map[ssa.Instruction]map[pathKey]lockPath

	// opGains is the lock a direct lock operation took at each call, joined over the states. It is
	// kept apart from gains, which the old walk reads at every call it does not consume.
	opGains map[ssa.Instruction]map[pathKey]lockPath

	// releases is the real locks each call's release annotations dropped, in some state.
	releases map[ssa.Instruction]map[pathKey]bool

	// deferHeld is held for a deferred call as run by one return; the Defer's own record is the union over its returns.
	deferHeld map[deferPair]map[pathKey]heldLock

	// sites is what query reads, kept beside held.
	sites map[ssa.Instruction]*siteView

	// inexact is the sites that answer unknown, marked before or after their record.
	inexact map[ssa.Instruction]bool

	// callOnly caches recordable.
	callOnly map[*ssa.Function]bool
}

// deferPair is a deferred call as run by one return.
type deferPair struct {
	run *ssa.RunDefers
	d   *ssa.Defer
}

// siteView is the joined states of one site, as flags that adding a state twice does not change.
type siteView struct {
	// every says a key was held in every state added; exclusive that each state holding it did so exclusively.
	every, exclusive map[pathKey]bool

	// marker is the union of the states' marker keys and their paths; top says some state's marker was top.
	marker map[pathKey]lockPath
	top    bool
}

// viewKind is what the states at a site say about one lock.
type viewKind uint8

const (
	viewUnknown viewKind = iota
	viewAll
	viewSome
	viewNone
)

// viewAnswer is the answer of engineView.query; exclusive and assumed are set for all and some.
type viewAnswer struct {
	kind               viewKind
	exclusive, assumed bool
}

// query says whether the lock under the canonical key k is held in all, some or none of the states at inst, or unknown.
// The held field is the some answer: a client that suppresses or derives must use query.
func (e *engineView) query(inst ssa.Instruction, k pathKey) viewAnswer {
	if !e.usable(inst) {
		return viewAnswer{}
	}
	sv := e.sites[inst]
	if sv == nil || e.inexact[inst] {
		return viewAnswer{}
	}
	if every, ok := sv.every[k]; ok {
		kind := viewSome
		if every {
			kind = viewAll
		}
		return viewAnswer{kind: kind, exclusive: sv.exclusive[k], assumed: e.held[inst][k].assumed}
	}
	if _, marked := sv.marker[k]; sv.top || marked {
		return viewAnswer{}
	}
	return viewAnswer{kind: viewNone}
}

// markInexact marks the site of inst as an approximation, and says whether the mark is new.
func (e *engineView) markInexact(inst ssa.Instruction) bool {
	if e == nil || e.inexact[inst] {
		return false
	}
	if e.inexact == nil {
		e.inexact = make(map[ssa.Instruction]bool)
	}
	e.inexact[inst] = true
	return true
}

// add joins the locks held at inst in one state into the site's record. A lock counts as
// assumed only if every state that holds it assumes it. An empty set is still a record.
func (e *engineView) add(inst ssa.Instruction, ls *lockState, entry map[pathKey]lockInfo) {
	if e == nil {
		return
	}
	locks := ls.lockedMutexes
	e.addSite(inst, ls)
	if e.held == nil {
		e.held = make(map[ssa.Instruction]map[pathKey]heldLock)
	}
	e.held[inst] = joinHeld(e.held[inst], locks, entry)
}

// addPair joins the locks held at one return's run of a deferred call, as add does for a site.
func (e *engineView) addPair(p deferPair, ls *lockState, entry map[pathKey]lockInfo) {
	if e == nil {
		return
	}
	if e.deferHeld == nil {
		e.deferHeld = make(map[deferPair]map[pathKey]heldLock)
	}
	e.deferHeld[p] = joinHeld(e.deferHeld[p], ls.lockedMutexes, entry)
}

// joinHeld joins one state's locks into rec, which is nil for the first state.
func joinHeld(rec map[pathKey]heldLock, locks, entry map[pathKey]lockInfo) map[pathKey]heldLock {
	if rec == nil {
		rec = make(map[pathKey]heldLock)
	}
	for key, info := range locks {
		_, atEntry := entry[key]
		if cur, ok := rec[key]; ok {
			info.assumed = info.assumed && cur.assumed
			atEntry = atEntry || cur.entry
		}
		rec[key] = heldLock{path: info.path, assumed: info.assumed, entry: atEntry}
	}
	return rec
}

// addSite joins a state into the site's flags; a key first seen after the site had a record
// was not held in every state.
func (e *engineView) addSite(inst ssa.Instruction, ls *lockState) {
	if e.sites == nil {
		e.sites = make(map[ssa.Instruction]*siteView)
	}
	sv := e.sites[inst]
	first := sv == nil
	if first {
		sv = &siteView{every: make(map[pathKey]bool), exclusive: make(map[pathKey]bool), marker: make(map[pathKey]lockPath)}
		e.sites[inst] = sv
	}
	for k := range sv.every {
		if _, ok := ls.lockedMutexes[k]; !ok {
			sv.every[k] = false
		}
	}
	for k, info := range ls.lockedMutexes {
		if _, seen := sv.every[k]; !seen {
			sv.every[k], sv.exclusive[k] = first, true
		}
		sv.exclusive[k] = sv.exclusive[k] && info.exclusive
	}
	for k, m := range ls.marker {
		sv.marker[k] = m.path
	}
	sv.top = sv.top || ls.markerTop
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

// addOpGain records that the direct lock operation at inst took path.
func (e *engineView) addOpGain(inst ssa.Instruction, path lockPath) {
	if e == nil {
		return
	}
	if e.opGains == nil {
		e.opGains = make(map[ssa.Instruction]map[pathKey]lockPath)
	}
	if e.opGains[inst] == nil {
		e.opGains[inst] = make(map[pathKey]lockPath)
	}
	e.opGains[inst][path.Key()] = path
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

// giveUp marks fn, and the literals inside it, as having no usable records.
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
	return e != nil && e.gaveUpIn(inst.Parent()) == ""
}

// gaveUpIn returns the reason of the nearest function, from fn outward, that gave up.
func (e *engineView) gaveUpIn(fn *ssa.Function) string {
	for ; fn != nil; fn = fn.Parent() {
		if reason := e.gaveUp[fn]; reason != "" {
			return reason
		}
	}
	return ""
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

// deferHeldSet is heldSet for the deferred call d as run by run.
func (e *engineView) deferHeldSet(run *ssa.RunDefers, d *ssa.Defer) (map[pathKey]heldLock, bool) {
	if !e.usable(d) {
		return nil, false
	}
	rec, ok := e.deferHeld[deferPair{run, d}]
	return rec, ok
}

// gainsOf returns the locks the call's acquire annotations took, or nil.
func (e *engineView) gainsOf(inst ssa.Instruction) map[pathKey]lockPath {
	if !e.usable(inst) {
		return nil
	}
	return e.gains[inst]
}

// opGainsOf returns the lock the call took as a direct lock operation, or nil.
func (e *engineView) opGainsOf(inst ssa.Instruction) map[pathKey]lockPath {
	if !e.usable(inst) {
		return nil
	}
	return e.opGains[inst]
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

// whyNone names why a site of fn has no usable record: "gave up: <reason>", or for a literal "not called directly"
// or "not reached"; "" otherwise.
func (e *engineView) whyNone(fn *ssa.Function) string {
	if e == nil {
		return ""
	}
	if reason := e.gaveUpIn(fn); reason != "" {
		return "gave up: " + reason
	}
	if fn.Parent() == nil {
		return ""
	}
	if !e.recordable(fn) {
		return "not called directly"
	}
	return "not reached"
}

// recordable says whether the sites of the literal fn are recorded: it and every literal around it
// is only ever called directly. A function that is not a literal is recordable.
func (e *engineView) recordable(fn *ssa.Function) bool {
	parent := fn.Parent()
	if parent == nil {
		return true
	}
	if ok, cached := e.callOnly[fn]; cached {
		return ok
	}
	ok := e.recordable(parent) && calledOnly(fn)
	if e.callOnly == nil {
		e.callOnly = make(map[*ssa.Function]bool)
	}
	e.callOnly[fn] = ok
	return ok
}

// calledOnly says whether every use of the literal fn in its parent, through its closure if it has
// one, is as the callee of a call.
func calledOnly(fn *ssa.Function) bool {
	for _, b := range fn.Parent().Blocks {
		for _, inst := range b.Instrs {
			if mc, ok := inst.(*ssa.MakeClosure); ok && mc.Fn == fn {
				if refs := mc.Referrers(); refs != nil && slices.ContainsFunc(*refs, func(r ssa.Instruction) bool { return !isCallee(r, mc) }) {
					return false
				}
				continue
			}
			for _, op := range inst.Operands(nil) {
				if *op == fn && !isCallee(inst, fn) {
					return false
				}
			}
		}
	}
	return true
}

// isCallee says whether inst calls v and uses it nowhere else.
func isCallee(inst ssa.Instruction, v ssa.Value) bool {
	c, ok := inst.(*ssa.Call)
	if !ok || c.Call.IsInvoke() || c.Call.Value != v {
		return false
	}
	uses := 0
	for _, op := range inst.Operands(nil) {
		if *op == v {
			uses++
		}
	}
	return uses == 1
}

// replaying returns the engine, and whether inst is a site of the top-level function whose
// replay is running, or of a literal inside it.
func (pc *passContext) replaying(inst ssa.Instruction) (*engineView, bool) {
	if pc.engine == nil || pc.emit != emitReplay || inst == nil || len(pc.records) == 0 || pc.current.wc.foreign {
		return nil, false
	}
	fn := pc.records[0].wc.fn
	outer := inst.Parent()
	for outer != nil && outer.Parent() != nil {
		outer = outer.Parent()
	}
	return pc.engine, fn.Parent() == nil && outer == fn
}

// recordHeld records the locks held at inst, a call or channel operation of the function being replayed, of a
// recordable literal inside it, or of a synthetic wrapper walked inline under any top. A site of a literal
// that is not recordable is recorded under any top and answers unknown to query.
func (pc *passContext) recordHeld(inst ssa.Instruction, run *ssa.RunDefers, wc *walkContext, ls *lockState) {
	fn := wc.fn
	if pc.engine == nil || pc.emit != emitReplay || inst == nil || inst.Parent() != fn || len(pc.records) == 0 || wc.foreign {
		return
	}
	top := pc.records[0].wc.fn
	approx := fn.Parent() != nil && !pc.engine.recordable(fn)
	if fn.Parent() != nil && !approx && top.Parent() != nil {
		return
	}
	// A no-op defer entry of a literal with a walk whose defer stacks were not joined is whichever arrived first.
	if d, ok := inst.(*ssa.Defer); ok && fn != top && !pc.deferRelevant(d) && pc.unjoined[fn] {
		return
	}
	entry := pc.records[0].parent.lockedMutexes
	pc.engine.add(inst, ls, entry)
	d, isDefer := inst.(*ssa.Defer)
	if approx && !isDefer {
		pc.engine.markInexact(inst)
	}
	if isDefer && run != nil {
		pc.engine.addPair(deferPair{run, d}, ls, entry)
	}
}

// recordGain records a lock the annotations of the call at inst took, or made real.
func (pc *passContext) recordGain(inst ssa.Instruction, path lockPath) {
	if e, ok := pc.replaying(inst); ok {
		e.addGain(inst, path)
	}
}

// recordOpGain records the lock a direct lock operation at inst took.
func (pc *passContext) recordOpGain(inst ssa.Instruction, path lockPath) {
	if e, ok := pc.replaying(inst); ok {
		e.addOpGain(inst, path)
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
