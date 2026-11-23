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
	"testing"
)

// TestEngineViewJoin pins how states join at a site: assumed only if every state assumes it,
// entry if any state holds it from entry, and an empty set is still a record.
func TestEngineViewJoin(t *testing.T) {
	pkg := buildTestPackage(t, "join", "package join\n\nfunc f() {}\n\nfunc g() {}\n")
	f, g := pkg.Func("f"), pkg.Func("g")
	at, other := f.Blocks[0].Instrs[0], g.Blocks[0].Instrs[0]

	p := newLockPath(pathRootValue, nil, nil, "mu")
	held := func(assumed bool) map[pathKey]lockInfo {
		return map[pathKey]lockInfo{p.Key(): {path: p, assumed: assumed}}
	}
	var e engineView
	e.add(at, held(true), nil)
	e.add(at, held(false), held(false))
	e.add(other, nil, nil)

	got, ok := e.heldAt(at)
	if !ok || len(got) != 1 || got[0].assumed || !got[0].entry {
		t.Errorf("joined record = %+v, %v; want one lock, not assumed, held from entry", got, ok)
	}
	e.add(at, held(true), nil)
	if got, _ := e.heldAt(at); got[0].assumed {
		t.Error("a state that assumes the lock made it assumed again")
	}
	if got, ok := e.heldAt(other); !ok || len(got) != 0 {
		t.Errorf("empty state = %v, %v; want a record with no locks", got, ok)
	}
}

// TestEngineViewEffects pins the gain and release records: union over states, and none for a function given up on.
func TestEngineViewEffects(t *testing.T) {
	pkg := buildTestPackage(t, "effects", "package effects\n\nfunc f() {}\n")
	f := pkg.Func("f")
	at := f.Blocks[0].Instrs[0]
	p := newLockPath(pathRootValue, nil, nil, "mu")
	q := newLockPath(pathRootValue, nil, nil, "other")

	e := &engineView{}
	e.addGain(at, p)
	e.addGain(at, q)
	e.addRelease(at, p)
	if got := e.gainsOf(at); len(got) != 2 {
		t.Errorf("gains = %v, want both locks", got)
	}
	if got := e.releasesOf(at); len(got) != 1 || !got[p.Key()] {
		t.Errorf("releases = %v, want the one lock", got)
	}
	e.giveUp(f, "test")
	if e.gainsOf(at) != nil || e.releasesOf(at) != nil {
		t.Error("function given up on still has effect records")
	}

	var none *engineView
	none.addGain(at, p)
	none.add(at, nil, nil)
	if _, ok := none.heldAt(at); ok || none.gainsOf(at) != nil || none.releasesOf(at) != nil {
		t.Error("nil view reported records")
	}
}

// checkFloor fails when cs holds less of a class than its own count lo, or than the own-only set.
func checkFloor(t *testing.T, step string, cs, own *classSet) {
	t.Helper()
	for c, n := range cs.lo {
		if cs.counts[c] < n {
			t.Errorf("%s: counts[%s] = %d below lo %d", step, c, cs.counts[c], n)
		}
	}
	for c, n := range own.counts {
		if cs.counts[c] < n {
			t.Errorf("%s: counts[%s] = %d below own-only %d", step, c, cs.counts[c], n)
		}
	}
	for c, n := range own.lo {
		if cs.lo[c] != n {
			t.Errorf("%s: lo[%s] = %d, own-only has %d", step, c, cs.lo[c], n)
		}
	}
}

// TestClassSetFloor pins that gains and undos never take a count below this analysis's own, through forks and merges.
func TestClassSetFloor(t *testing.T) {
	cs, own := newClassSet(), newClassSet()
	both := func(step string, f func(*classSet)) {
		f(own)
		f(cs)
		checkFloor(t, step, cs, own)
	}
	both("acquire", func(c *classSet) { c.acquire("A") })
	cs.gain("A")
	checkFloor(t, "gain", cs, own)
	cs.undo("A")
	cs.undo("A")
	checkFloor(t, "undo past the floor", cs, own)
	if cs.counts["A"] != 1 {
		t.Errorf("counts[A] = %d after undoing the one gain twice, want 1", cs.counts["A"])
	}
	cs.gain("A")
	both("release", func(c *classSet) { c.release("A", false) })
	cs.undo("A")
	checkFloor(t, "undo after release", cs, own)

	cs.gain("B")
	cf, ownF := cs.fork(), own.fork()
	cf.gain("B")
	for _, c := range []*classSet{cf, ownF} {
		c.acquire("C")
		c.release("A", false)
	}
	cf.undo("B")
	cf.undo("B")
	checkFloor(t, "fork", cf, ownF)
	cs.merge(cf)
	own.merge(ownF)
	checkFloor(t, "merge", cs, own)
	cf.merge(cs)
	ownF.merge(own)
	checkFloor(t, "merge back", cf, ownF)
}
