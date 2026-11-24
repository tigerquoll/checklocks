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
	"testing"

	"golang.org/x/tools/go/ssa"
)

const joinSource = `package p

type Mutex struct{}

func needsLock(m *Mutex) {}
func noop()              {}
func noop2()             {}

func arms(c bool) {
	if c {
		defer noop()
	} else {
		defer noop2()
	}
	noop()
}

func gaps(m *Mutex) {
	defer noop()
	defer needsLock(m)
	defer noop()
}
`

func TestPushDeferEntries(t *testing.T) {
	pkg := buildTestPackage(t, "join", joinSource)
	pc := deferPC(pkg.Prog.Fset)
	sites := deferSites(pkg.Func("gaps"))
	low, rel, high := sites[0], sites[1], sites[2]
	if !pc.deferRelevant(rel) || pc.deferRelevant(low) {
		t.Fatal("the fixture wants one relevant defer between two no-ops")
	}
	ls := newLockState()
	push := func(d *ssa.Defer) { ls.pushDefer(d, pc.deferRelevant(d)) }
	check := func(what string, want ...deferEntry) {
		t.Helper()
		if !slices.Equal(ls.noops, want) {
			t.Errorf("%s: entries %v, want %v", what, ls.noops, want)
		}
	}
	push(low)
	push(low)
	check("a no-op twice with nothing between", deferEntry{d: low})
	push(rel)
	check("a relevant defer after it", deferEntry{d: low, after: "\x00"})
	push(low)
	check("the no-op again above the relevant defer", deferEntry{d: low, after: "\x00"}, deferEntry{d: low})
	push(rel)
	check("the relevant defer again", deferEntry{d: low, after: "\x00"})
	push(high)
	push(rel)
	check("a second no-op", deferEntry{d: low, after: "\x00"}, deferEntry{d: high, after: "\x00"})
}

// TestPushDeferAfterOrder checks after lists the relevant defers in the order each was first pushed after the entry.
func TestPushDeferAfterOrder(t *testing.T) {
	noop, r0, r1 := &ssa.Defer{}, &ssa.Defer{}, &ssa.Defer{}
	var s deferStack
	s = s.push(r0, true)
	s = s.push(noop, false)
	s = s.push(r1, true)
	old := s
	s = s.push(r0, true)
	if want := []deferEntry{{d: noop, after: "\x01\x00"}}; !slices.Equal(s.noops, want) {
		t.Errorf("entries %v, want %v", s.noops, want)
	}
	if !slices.Equal(old.noops, []deferEntry{{d: noop, after: "\x01"}}) || !slices.Equal(s.defers, []*ssa.Defer{r0, r1}) {
		t.Error("a push changed the stack it was made from, or moved a relevant defer")
	}
}

// TestPushDeferCap checks a no-op with more than maxEntries distinct entries collapses to one may entry, and runs holding every lock held on the way.
func TestPushDeferCap(t *testing.T) {
	noop := &ssa.Defer{}
	rel := make([]*ssa.Defer, maxEntries+1)
	for i := range rel {
		rel[i] = &ssa.Defer{}
	}
	var s deferStack
	for i, r := range rel {
		s = s.push(noop, false)
		s = s.push(r, true)
		if i < maxEntries && len(s.noops) != i+1 {
			t.Fatalf("after %d pushes the no-op has %d entries, want %d", i+1, len(s.noops), i+1)
		}
	}
	s = s.push(noop, false)
	if len(s.noops) != 1 || !s.noops[0].may {
		t.Fatalf("past the cap the entries are %v, want one may entry", s.noops)
	}
	if again := s.push(noop, false); !slices.Equal(again.noops, s.noops) {
		t.Error("a push of a collapsed no-op changed its entry")
	}

	// Rank 0 releases g; rank 1 acquires h.
	ls := newLockState()
	ls.lockedMutexes["g"] = lockInfo{}
	ls.deferStack = s
	var sawNoop []pathKey
	may := unwindDefers(ls, func(d *ssa.Defer, at *lockState) {
		at.modify()
		switch d {
		case rel[0]:
			delete(at.lockedMutexes, "g")
		case rel[1]:
			at.lockedMutexes["h"] = lockInfo{}
		case noop:
			for k := range at.lockedMutexes {
				sawNoop = append(sawNoop, k)
			}
		}
	})
	slices.Sort(sawNoop)
	if may != 1 || !slices.Equal(sawNoop, []pathKey{"g", "h"}) {
		t.Errorf("the may entry ran %d times holding %v, want once holding g and h", may, sawNoop)
	}
}

// TestJoinDefers checks the arms of an if each deferring a no-op leave the merge both, in block order, whichever arm arrived first.
func TestJoinDefers(t *testing.T) {
	pkg := buildTestPackage(t, "join", joinSource)
	pc := deferPC(pkg.Prog.Fset)
	fn := pkg.Func("arms")
	sites := deferSites(fn)
	thenDefer, elseDefer := sites[0], sites[1]
	entry, thenB, elseB := fn.Blocks[0], thenDefer.Block(), elseDefer.Block()
	merge := thenB.Succs[0]
	if elseB.Succs[0] != merge || thenB.Index >= elseB.Index {
		t.Fatal("the fixture wants two arms in source order that meet")
	}
	for _, first := range sites {
		for _, swapped := range []bool{false, true} {
			wc := &walkContext{fn: fn}
			rec := func(b *ssa.BasicBlock, noops []deferEntry, outs ...*ssa.BasicBlock) *blockRecord {
				ls := newLockState()
				ls.noops = noops
				r := &blockRecord{wc: wc, block: b, parent: ls, pair: pairID{b, "k"}}
				for _, to := range outs {
					r.outs = append(r.outs, pairID{to, "k"})
				}
				return r
			}
			recs := []*blockRecord{
				rec(entry, nil, thenB, elseB),
				rec(thenB, nil, merge),
				rec(elseB, nil, merge),
				rec(merge, []deferEntry{{d: first}}),
			}
			if swapped {
				recs[1], recs[2] = recs[2], recs[1]
			}
			pc.records = recs
			pc.joinDefers(wc, 0)
			got := recs[3].parent.noops
			if want := []deferEntry{{d: thenDefer}, {d: elseDefer}}; !slices.Equal(got, want) {
				t.Errorf("first arrival %v, swapped %t: the merge holds %d defers, want both in block order", first.Pos(), swapped, len(got))
			}
			if n := len(recs[0].parent.noops) + len(recs[1].parent.noops) + len(recs[2].parent.noops); n != 0 {
				t.Errorf("blocks before the merge hold %d defers, want none", n)
			}
		}
	}
}

// TestUnwindInfoChange checks a no-op sees a relevant defer's exclusive or assumed change to a held lock.
func TestUnwindInfoChange(t *testing.T) {
	noop, rel := &ssa.Defer{}, &ssa.Defer{}
	ls := newLockState()
	ls.lockedMutexes["g"] = lockInfo{exclusive: true, assumed: true}
	ls.lockedMutexes["h"] = lockInfo{exclusive: true}
	ls.pushDefer(noop, false)
	ls.pushDefer(rel, true)
	var seen map[pathKey]lockInfo
	unwindDefers(ls, func(d *ssa.Defer, at *lockState) {
		at.modify()
		if d == rel {
			at.lockedMutexes["g"] = lockInfo{exclusive: true}
			at.lockedMutexes["h"] = lockInfo{}
			return
		}
		seen = maps.Clone(at.lockedMutexes)
	})
	want := map[pathKey]lockInfo{"g": {exclusive: true}, "h": {}}
	if !maps.EqualFunc(seen, want, func(a, b lockInfo) bool { return a.exclusive == b.exclusive && a.assumed == b.assumed }) {
		t.Errorf("the no-op ran holding %v, want %v", seen, want)
	}
}
