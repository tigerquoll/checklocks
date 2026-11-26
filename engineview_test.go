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

	"golang.org/x/tools/go/ssa"
)

// TestEngineViewJoin pins how states join at a site: assumed only if every state assumes it,
// entry if any state holds it from entry, and an empty set is still a record.
func TestEngineViewJoin(t *testing.T) {
	pkg := buildTestPackage(t, "join", "package join\n\nfunc f() {}\n\nfunc g() {}\n")
	f, g := pkg.Func("f"), pkg.Func("g")
	at, other := f.Blocks[0].Instrs[0], g.Blocks[0].Instrs[0]

	p := newLockPath(pathRootValue, nil, nil, "mu")
	held := func(assumed bool) *lockState {
		return &lockState{lockedMutexes: map[pathKey]lockInfo{p.Key(): {path: p, assumed: assumed}}}
	}
	var e engineView
	e.add(at, held(true), nil)
	e.add(at, held(false), held(false).lockedMutexes)
	e.add(other, newLockState(), nil)

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

// TestEngineViewDeferPair pins that a deferred call is recorded per return that runs it, beside its union record.
func TestEngineViewDeferPair(t *testing.T) {
	pkg := buildTestPackage(t, "pair", "package pair\n\nfunc g() {}\n\nfunc f(b bool) {\n\tdefer g()\n\tif b {\n\t\treturn\n\t}\n}\n")
	var d *ssa.Defer
	var runs []*ssa.RunDefers
	for _, b := range pkg.Func("f").Blocks {
		for _, inst := range b.Instrs {
			switch x := inst.(type) {
			case *ssa.Defer:
				d = x
			case *ssa.RunDefers:
				runs = append(runs, x)
			}
		}
	}
	if d == nil || len(runs) != 2 {
		t.Fatalf("fixture has a defer %v and %d RunDefers, want a defer and two", d, len(runs))
	}

	p := newLockPath(pathRootValue, nil, nil, "mu")
	q := newLockPath(pathRootValue, nil, nil, "other")
	state := func(paths ...lockPath) *lockState {
		ls := newLockState()
		for _, path := range paths {
			ls.lockedMutexes[path.Key()] = lockInfo{path: path}
		}
		return ls
	}
	var e engineView
	for i, ls := range []*lockState{state(p), state(q)} {
		e.add(d, ls, nil)
		e.addPair(deferPair{runs[i], d}, ls, nil)
	}

	for i, want := range []lockPath{p, q} {
		got, ok := e.deferHeldSet(runs[i], d)
		if _, held := got[want.Key()]; !ok || len(got) != 1 || !held {
			t.Errorf("return %d record = %v, %v; want only its own lock", i, got, ok)
		}
	}
	if got, _ := e.heldSet(d); len(got) != 2 {
		t.Errorf("the defer's union record = %v, want both locks", got)
	}
	e.giveUp(pkg.Func("f"), "test")
	if _, ok := e.deferHeldSet(runs[0], d); ok {
		t.Error("function given up on still has a pair record")
	}
}

// TestRunDefersRecordsPair pins that runDefers records a deferred call under the return it runs at.
func TestRunDefersRecordsPair(t *testing.T) {
	u := newUnwindFixture(t, "package unwind\n\nfunc g() {}\n\nfunc f() {\n\tdefer g()\n}\n", "f")
	var run *ssa.RunDefers
	for _, inst := range u.fn.Blocks[0].Instrs {
		if r, ok := inst.(*ssa.RunDefers); ok {
			run = r
		}
	}
	ls := newLockState()
	ls.pushDefer(u.sites[0], true)
	u.pc.runDefers(run, u.wc, ls)
	if _, ok := u.pc.engine.deferHeldSet(run, u.sites[0]); !ok {
		t.Error("runDefers left no record for the deferred call at its return")
	}
}

// TestEngineViewEffects pins the gain records: union over states, and none for a function given up on.
func TestEngineViewEffects(t *testing.T) {
	pkg := buildTestPackage(t, "effects", "package effects\n\nfunc f() {}\n")
	f := pkg.Func("f")
	at := f.Blocks[0].Instrs[0]
	p := newLockPath(pathRootValue, nil, nil, "mu")
	q := newLockPath(pathRootValue, nil, nil, "other")

	e := &engineView{}
	e.addGain(at, p)
	e.addGain(at, q)
	if got := e.gainsOf(at); len(got) != 2 {
		t.Errorf("gains = %v, want both locks", got)
	}
	e.giveUp(f, "test")
	if e.gainsOf(at) != nil {
		t.Error("function given up on still has gain records")
	}

	var none *engineView
	none.addGain(at, p)
	none.add(at, newLockState(), nil)
	if _, ok := none.heldAt(at); ok || none.gainsOf(at) != nil {
		t.Error("nil view reported records")
	}
}
