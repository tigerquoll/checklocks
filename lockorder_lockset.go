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
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// siteLocks is what the walk reads and updates of the locks held at a point in a function.
type siteLocks interface {
	held() []string
	releasedClasses() []string
	acquire(class string)
	release(class string, receiverOwned bool)
	fork() siteLocks
	merge(other siteLocks)
	// at runs before the walk visits inst.
	at(fn *ssa.Function, inst ssa.Instruction)
	// after runs on every exit from a visited call; consumed says the call was a lock operation.
	after(fn *ssa.Function, call ssa.CallInstruction, consumed bool)
}

// classSet is the set of lock classes held at a point in a function.
//
// It counts rather than just records: one class can legitimately be held more than once by
// a hierarchical class, and a release has to put the count back rather than clear it. The
// analysis is per class, not per instance, so two locks of one class are indistinguishable
// here by design; that is the whole point of a class based order.
type classSet struct {
	counts map[string]int

	// entry is what the function was called with held, from its annotations. It is the
	// baseline a release is measured against: dropping below it releases a lock that
	// belongs to the CALLER, which is what a call site needs to know about.
	//
	// It does not change once the walk starts, so a fork shares it.
	entry map[string]int

	// released is the classes released out of the caller's locks, and not taken back. A
	// summary exports it so a call site can subtract it from its own held set.
	released map[string]bool

	// d2 is released as the engine's records would set it; the tagged build logs where it differs.
	d2 map[string]bool

	// site is the classes of the entry locks only the engine has, as it held them at the current site;
	// it is replaced at every call and channel operation, cleared where the engine has no record, and never counted down.
	site map[string]int

	// eng is the engine's locks that counts includes, and the class each is counted under: the
	// entry locks this analysis entered with itself, and the locks a callee acquired.
	eng map[pathKey]string

	// lo is this analysis's own count of each class, without the engine's gains. Counts never fall below it.
	lo map[string]int

	// pc is the context at and after run their overlay and effect in; a fork shares it.
	pc *lockOrderContext
}

func newClassSet() *classSet {
	return &classSet{
		counts:   make(map[string]int),
		entry:    make(map[string]int),
		released: make(map[string]bool),
		eng:      make(map[pathKey]string),
		lo:       make(map[string]int),
	}
}

// fork copies the set, for the separate paths of a branch.
func (cs *classSet) fork() siteLocks {
	out := &classSet{
		pc:       cs.pc,
		counts:   make(map[string]int, len(cs.counts)),
		entry:    cs.entry,
		released: make(map[string]bool, len(cs.released)),
		site:     cs.site,
		eng:      make(map[pathKey]string, len(cs.eng)),
	}
	for k, v := range cs.eng {
		out.eng[k] = v
	}
	out.lo = make(map[string]int, len(cs.lo))
	for k, v := range cs.lo {
		out.lo[k] = v
	}
	for k, v := range cs.counts {
		out.counts[k] = v
	}
	for k := range cs.released {
		out.released[k] = true
	}
	if cs.d2 != nil {
		out.d2 = maps.Clone(cs.d2)
	}
	return out
}

func (cs *classSet) at(fn *ssa.Function, inst ssa.Instruction) {
	cs.pc.entryOverlay(fn, inst, cs)
}

func (cs *classSet) after(fn *ssa.Function, call ssa.CallInstruction, consumed bool) {
	if !consumed {
		cs.pc.applyEffect(fn, call, cs)
	}
}

// enter records a class the function is called with held.
func (cs *classSet) enter(class string) {
	if class == "" {
		return
	}
	cs.entry[class]++
	cs.acquire(class)
}

// gain counts a lock the engine saw a callee take; lo does not include it.
func (cs *classSet) gain(class string) {
	if class == "" {
		return
	}
	cs.raise(class)
}

// undo takes back one gain of class, never below this analysis's own count.
func (cs *classSet) undo(class string) {
	if cs.counts[class] > cs.lo[class] {
		cs.counts[class]--
		if cs.counts[class] == 0 {
			delete(cs.counts, class)
		}
	}
}

// acquire records that a class has been taken.
func (cs *classSet) acquire(class string) {
	if class == "" {
		return
	}
	cs.lo[class]++
	cs.raise(class)
}

// raise counts one more of class held.
func (cs *classSet) raise(class string) {
	cs.counts[class]++
	// Taking the caller's lock back closes the gap: from here on it is held again.
	if cs.counts[class] >= cs.entry[class] {
		delete(cs.released, class)
	}
}

// release records that a class has been dropped, and whether the lock belongs to the
// function's own receiver.
//
// A release that takes the count below what the caller held, or that has nothing held at
// all, drops a lock this function did not take: the caller's. That is recorded, but only
// for the receiver's own lock, which is the shape the unlock-relock idiom takes; releasing
// some other object's lock is not modelled and stays invisible to the call site.
//
// The count itself never goes negative. The lock was taken somewhere this analysis cannot
// see, and inventing a negative count would make every later check wrong.
func (cs *classSet) release(class string, receiverOwned bool) {
	if class == "" {
		return
	}
	if cs.counts[class] == 0 {
		// Nothing held here at all, so the lock came from the caller.
		if receiverOwned {
			cs.released[class] = true
		}
		return
	}
	if cs.lo[class] > 0 {
		cs.lo[class]--
	}
	cs.counts[class]--
	if cs.counts[class] == 0 {
		delete(cs.counts, class)
	}
	if receiverOwned && cs.counts[class] < cs.entry[class] {
		cs.released[class] = true
	}
}

// releasedClasses returns the caller's locks the function has released, sorted.
func (cs *classSet) releasedClasses() []string {
	if len(cs.released) == 0 {
		return nil
	}
	out := make([]string, 0, len(cs.released))
	for k := range cs.released {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// held returns the classes currently held, sorted so diagnostics are deterministic.
func (cs *classSet) held() []string {
	out := make([]string, 0, len(cs.counts)+len(cs.site))
	for k := range cs.counts {
		out = append(out, k)
	}
	for k, n := range cs.site {
		if n > 0 && cs.counts[k] == 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// empty reports whether nothing is held.
func (cs *classSet) empty() bool { return len(cs.held()) == 0 }

// String renders the set for a diagnostic.
func (cs *classSet) String() string {
	h := cs.held()
	if len(h) == 0 {
		return "no locks held"
	}
	return strings.Join(h, ", ")
}

func (cs *classSet) merge(other siteLocks) {
	cs.mergeChanged(other.(*classSet))
}

// mergeChanged folds another set into this one, keeping the larger count of each class, and
// reports whether this one changed.
//
// Merging at a join point is a deliberate over approximation: if a class is held on one
// path into a block it is treated as held afterwards. That errs towards reporting rather
// than towards silence, which is the right direction for an ordering check, and matches
// what the runtime checker would observe on the path that does hold it.
//
// The released set goes the other way, for the same reason: a lock counts as released only
// where every path into the block released it, so a gap on one path alone does not excuse
// the path that still holds the lock.
func (cs *classSet) mergeChanged(other *classSet) bool {
	changed := false
	for k, v := range other.lo {
		if cs.lo[k] < v {
			cs.lo[k] = v
			changed = true
		}
	}
	for k, v := range other.eng {
		if _, ok := cs.eng[k]; !ok {
			cs.eng[k] = v
			changed = true
		}
	}
	if len(other.site) > 0 {
		site := make(map[string]int, len(cs.site)+len(other.site))
		for k, v := range cs.site {
			site[k] = v
		}
		for k, v := range other.site {
			if site[k] < v {
				site[k] = v
				changed = true
			}
		}
		cs.site = site
	}
	for k, v := range other.counts {
		if cs.counts[k] < v {
			cs.counts[k] = v
			changed = true
		}
	}
	for k := range cs.released {
		if !other.released[k] {
			delete(cs.released, k)
			changed = true
		}
	}
	for k := range cs.d2 {
		if !other.d2[k] {
			delete(cs.d2, k)
			changed = true
		}
	}
	return changed
}
