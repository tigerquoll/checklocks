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
	"go/constant"
	"go/types"
	"maps"
	"testing"

	"golang.org/x/tools/go/ssa"
)

const loopSource = `package p

type T struct{ a int }

var G int

func get(i int) *T { return nil }

func F(t *T, ts []*T, n int) {
	before := get(0)
	_ = before
	for i := 0; i < n; i++ {
		inside := get(i)
		_ = inside
	}
}

func H() {
	m := 0
	func() { m++ }()
}

func D() {
	defer get(1)
	defer get(2)
}
`

// loopFixture is the SSA of loopSource with the pieces the tests build paths from.
type loopFixture struct {
	body                        map[*ssa.BasicBlock]bool
	param, global, free         lockPath
	before, inside, phi, number lockPath
	defers                      []*ssa.Defer
}

func newLoopFixture(t *testing.T) loopFixture {
	t.Helper()
	pkg := buildTestPackage(t, "loops", loopSource)
	f := pkg.Func("F")
	var head, latch *ssa.BasicBlock
	for _, b := range f.Blocks {
		if isLoopHead(b) {
			head = b
			for _, p := range b.Preds {
				if b.Dominates(p) {
					latch = p
				}
			}
		}
	}
	if head == nil {
		t.Fatal("F has no loop head")
	}
	fx := loopFixture{body: loopBody(head, latch)}
	value := func(v ssa.Value) lockPath { return newLockPath(pathRootValue, v, nil, v.Name()) }
	for _, b := range f.Blocks {
		for _, inst := range b.Instrs {
			switch x := inst.(type) {
			case *ssa.Call:
				if _, ok := x.Call.Args[0].(*ssa.Const); ok && b == f.Blocks[0] {
					fx.before = value(x)
				} else if fx.body[b] {
					fx.inside = value(x)
				}
			case *ssa.Phi:
				fx.phi = value(x)
			}
		}
	}
	if fx.before.root.value == nil || fx.inside.root.value == nil || fx.phi.root.value == nil {
		t.Fatalf("fixture is missing a value: %v %v %v", fx.before, fx.inside, fx.phi)
	}
	fx.number = value(ssa.NewConst(constant.MakeInt64(7), types.Typ[types.Int]))
	p := f.Params[0]
	fx.param = newLockPath(pathRootParam, p, p.Object(), p.Name())
	g := pkg.Members["G"].(*ssa.Global)
	fx.global = newLockPath(pathRootGlobal, g, g.Object(), g.Name())
	fv := pkg.Func("H").AnonFuncs[0].FreeVars[0]
	fx.free = newLockPath(pathRootFreeVar, fv, nil, fv.Name())
	for _, b := range pkg.Func("D").Blocks {
		for _, inst := range b.Instrs {
			if d, ok := inst.(*ssa.Defer); ok {
				fx.defers = append(fx.defers, d)
			}
		}
	}
	return fx
}

func index(base, idx lockPath) lockPath {
	return base.extend(lockStep{kind: stepIndexAddr, index: idx})
}

// held makes a state holding each path.
func held(assumed bool, paths ...lockPath) *lockState {
	ls := newLockState()
	for _, p := range paths {
		ls.lockedMutexes[p.Key()] = lockInfo{exclusive: true, assumed: assumed, path: p}
	}
	return ls
}

func TestLoopBodyAndHead(t *testing.T) {
	fx := newLoopFixture(t)
	if len(fx.body) < 2 {
		t.Errorf("the loop body has %d blocks, want the head and the body", len(fx.body))
	}
	if fx.body[fx.before.root.value.(ssa.Instruction).Block()] {
		t.Error("the entry block is in the loop body")
	}
}

func TestPathDies(t *testing.T) {
	fx := newLoopFixture(t)
	for _, tc := range []struct {
		name string
		path lockPath
		want bool
	}{
		{"parameter", fx.param, false},
		{"global", fx.global, false},
		{"free variable", fx.free, false},
		{"value defined in the loop", fx.inside, true},
		{"value defined before the loop", fx.before, false},
		{"value without a block", fx.number, false},
		{"loop-defined index", index(fx.param, fx.phi), true},
		{"index inside an index", index(fx.param, index(fx.param, fx.inside)), true},
		{"index defined before the loop", index(fx.param, fx.before), false},
		{"dereference of a loop-defined value", fx.inside.extend(lockStep{kind: stepDeref}), true},
	} {
		if got := pathDies(tc.path, fx.body); got != tc.want {
			t.Errorf("%s: pathDies = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// dropped makes a marker holding the keys of the paths.
func dropped(paths ...lockPath) map[pathKey]struct{} {
	m := make(map[pathKey]struct{})
	for _, p := range paths {
		m[p.Key()] = struct{}{}
	}
	return m
}

// marked sets the marker of ls to the keys of the paths.
func marked(ls *lockState, paths ...lockPath) *lockState {
	ls.marker = dropped(paths...)
	return ls
}

func TestDropDead(t *testing.T) {
	fx := newLoopFixture(t)
	dies := func(p lockPath) bool { return pathDies(p, fx.body) }

	ls := held(false, fx.param, fx.inside)
	ls.dropDead(dies)
	if ls.count() != 1 || !maps.Equal(ls.marker, dropped(fx.inside)) {
		t.Errorf("a dead acquired lock: %d held, marker %v, want 1 held and the dropped key in the marker", ls.count(), ls.marker)
	}
	if _, ok := ls.lockedMutexes[fx.param.Key()]; !ok {
		t.Error("the parameter lock was dropped")
	}

	ls = held(true, fx.inside)
	ls.dropDead(dies)
	if ls.count() != 0 || len(ls.marker) != 0 {
		t.Errorf("a dead assumed lock: %d held, marker %v, want none held and the marker empty", ls.count(), ls.marker)
	}

	shared := held(false, fx.inside)
	fork := shared.fork()
	fork.dropDead(dies)
	if shared.count() != 1 || len(shared.marker) != 0 {
		t.Error("dropping from a fork changed the state it was forked from")
	}
}

func TestJoinMarker(t *testing.T) {
	fx := newLoopFixture(t)
	a, b := fx.param, fx.global
	pkg := buildTestPackage(t, "defers", deferSource)
	pc := deferPC(pkg.Prog.Fset)
	withDefer := func(ls *lockState) *lockState { ls.defers = []*ssa.Defer{fx.defers[0]}; return ls }
	withRelevant := func(ls *lockState) *lockState { ls.defers = deferSites(pkg.Func("relevant"))[:1]; return ls }
	for _, tc := range []struct {
		name    string
		entries []*lockState
		want    map[pathKey]struct{}
	}{
		{"entry with a marker, same locks", []*lockState{marked(held(false, a), fx.inside)}, dropped(fx.inside)},
		{"entry without a marker", []*lockState{held(false, a)}, dropped()},
		{"entry with a marker, other locks", []*lockState{marked(held(false, b), fx.inside)}, dropped()},
		{"entry with a marker, a no-op defer more", []*lockState{withDefer(marked(held(false, a), fx.inside))}, dropped(fx.inside)},
		{"entry with a marker, a relevant defer more", []*lockState{withRelevant(marked(held(false, a), fx.inside))}, dropped()},
		{"union of two entries", []*lockState{marked(held(false, a), fx.inside), marked(held(false, a), fx.before)}, dropped(fx.inside, fx.before)},
		{"no entries", nil, dropped()},
	} {
		s := held(false, a)
		s.joinMarker(pc, tc.entries)
		if !maps.Equal(s.marker, tc.want) {
			t.Errorf("%s: marker %v, want %v", tc.name, s.marker, tc.want)
		}
	}

	orig := marked(held(false, a), fx.inside)
	fork := orig.fork()
	fork.joinMarker(pc, []*lockState{marked(held(false, a), fx.before)})
	if !maps.Equal(fork.marker, dropped(fx.inside, fx.before)) {
		t.Errorf("joining into a fork: marker %v, want both keys", fork.marker)
	}
	if !maps.Equal(orig.marker, dropped(fx.inside)) {
		t.Errorf("joining into a fork changed the original's marker to %v", orig.marker)
	}
}

func TestPushDeferTwice(t *testing.T) {
	fx := newLoopFixture(t)
	ls := newLockState()
	ls.pushDefer(fx.defers[0])
	ls.pushDefer(fx.defers[0])
	if len(ls.defers) != 1 {
		t.Errorf("a defer site pushed twice leaves %d on the stack, want 1", len(ls.defers))
	}
	ls.pushDefer(fx.defers[1])
	if len(ls.defers) != 2 {
		t.Errorf("a second site leaves %d on the stack, want 2", len(ls.defers))
	}
}

func TestSilencesRelease(t *testing.T) {
	fx := newLoopFixture(t)
	for _, tc := range []struct {
		name     string
		set      []lockPath
		held     []lockPath
		path     lockPath
		deferred bool
		want     bool
	}{
		{"marker empty", nil, nil, fx.inside, false, false},
		{"dropped key", []lockPath{fx.inside}, nil, fx.inside, false, true},
		{"dropped key but held", []lockPath{fx.inside}, []lockPath{fx.inside}, fx.inside, false, false},
		{"value defined before the loop", []lockPath{fx.inside}, nil, fx.before, false, false},
		{"other value-rooted key, set non-empty", []lockPath{fx.inside}, nil, fx.phi, false, false},
		{"dropped index of a value on a parameter", []lockPath{index(fx.param, fx.phi)}, nil, index(fx.param, fx.phi), false, true},
		{"dropped index of an index of a value", []lockPath{index(fx.param, index(fx.param, fx.inside))}, nil, index(fx.param, index(fx.param, fx.inside)), false, true},
		{"deferred, value defined before the loop", []lockPath{fx.inside}, nil, fx.before, true, true},
		{"deferred, marker empty", nil, nil, fx.before, true, false},
		{"deferred, same key held", []lockPath{fx.inside}, []lockPath{fx.before}, fx.before, true, false},
		{"deferred, parameter root", []lockPath{fx.inside}, nil, fx.param, true, false},
	} {
		ls := marked(held(false, tc.held...), tc.set...)
		if got := ls.silencesRelease(tc.path, tc.deferred); got != tc.want {
			t.Errorf("%s: silencesRelease = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestLoopHeadBound(t *testing.T) {
	distinct := func(i int) *lockState {
		return held(false, newLockPath(pathRootValue, nil, nil, fmt.Sprint(i)))
	}
	pc := noFactsPC()
	h := &loopHead{entries: []*lockState{distinct(0)}}
	for i := 1; i < maxHeadStates; i++ {
		if added, over := h.admit(pc, distinct(i)); !added || over {
			t.Fatalf("state %d of %d: added %t, over %t, want it accepted", i+1, maxHeadStates, added, over)
		}
	}
	if added, over := h.admit(pc, distinct(1)); added || over {
		t.Errorf("a state already in a full set: added %t, over %t, want neither", added, over)
	}
	if added, over := h.admit(pc, distinct(maxHeadStates)); added || !over {
		t.Errorf("state %d: added %t, over %t, want over", maxHeadStates+1, added, over)
	}
}
