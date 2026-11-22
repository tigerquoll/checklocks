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

func TestDropDead(t *testing.T) {
	fx := newLoopFixture(t)
	dies := func(p lockPath) bool { return pathDies(p, fx.body) }

	ls := held(false, fx.param, fx.inside)
	ls.dropDead(dies)
	if ls.count() != 1 || !ls.marker {
		t.Errorf("a dead acquired lock: %d held, marker %t, want 1 held and the marker set", ls.count(), ls.marker)
	}
	if _, ok := ls.lockedMutexes[fx.param.Key()]; !ok {
		t.Error("the parameter lock was dropped")
	}

	ls = held(true, fx.inside)
	ls.dropDead(dies)
	if ls.count() != 0 || ls.marker {
		t.Errorf("a dead assumed lock: %d held, marker %t, want none held and the marker unset", ls.count(), ls.marker)
	}

	shared := held(false, fx.inside)
	fork := shared.fork()
	fork.dropDead(dies)
	if shared.count() != 1 || shared.marker {
		t.Error("dropping from a fork changed the state it was forked from")
	}
}

func TestJoinMarker(t *testing.T) {
	fx := newLoopFixture(t)
	a, b := fx.param, fx.global
	marked := func(ls *lockState) *lockState { ls.marker = true; return ls }
	withDefer := func(ls *lockState) *lockState { ls.defers = []*ssa.Defer{fx.defers[0]}; return ls }
	for _, tc := range []struct {
		name    string
		entries []*lockState
		want    bool
	}{
		{"entry with the marker, same locks", []*lockState{marked(held(false, a))}, true},
		{"entry without the marker", []*lockState{held(false, a)}, false},
		{"entry with the marker, other locks", []*lockState{marked(held(false, b))}, false},
		{"entry with the marker, other defers", []*lockState{withDefer(marked(held(false, a)))}, false},
		{"any entry", []*lockState{held(false, a), marked(held(false, a))}, true},
		{"no entries", nil, false},
	} {
		s := held(false, a)
		s.joinMarker(tc.entries)
		if s.marker != tc.want {
			t.Errorf("%s: marker %t, want %t", tc.name, s.marker, tc.want)
		}
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

func TestSameEntry(t *testing.T) {
	fx := newLoopFixture(t)
	a, b := fx.param, fx.global
	base := func() *lockState { return held(false, a) }
	if !base().sameEntry(base()) {
		t.Error("equal states differ")
	}
	marked := base()
	marked.marker = true
	withDefer := base()
	withDefer.defers = []*ssa.Defer{fx.defers[0]}
	for _, tc := range []struct {
		name  string
		other *lockState
	}{
		{"held", held(false, b)},
		{"assumed", held(true, a)},
		{"marker", marked},
		{"defers", withDefer},
	} {
		if base().sameEntry(tc.other) || tc.other.sameEntry(base()) {
			t.Errorf("states differing in %s are the same entry", tc.name)
		}
	}
}

func TestSilencesRelease(t *testing.T) {
	fx := newLoopFixture(t)
	for _, tc := range []struct {
		name   string
		marker bool
		held   []lockPath
		path   lockPath
		want   bool
	}{
		{"marker unset", false, nil, fx.inside, false},
		{"loop-defined value", true, nil, fx.inside, true},
		{"value defined before the loop", true, nil, fx.before, true},
		{"parameter", true, nil, fx.param, false},
		{"global", true, nil, fx.global, false},
		{"index of a value on a parameter", true, nil, index(fx.param, fx.phi), true},
		{"index of an index of a value", true, nil, index(fx.param, index(fx.param, fx.inside)), true},
		{"constant index on a parameter", true, nil, index(fx.param, fx.number), false},
		{"value held in the other mode", true, []lockPath{fx.inside}, fx.inside, false},
	} {
		ls := held(false, tc.held...)
		ls.marker = tc.marker
		if got := ls.silencesRelease(tc.path); got != tc.want {
			t.Errorf("%s: silencesRelease = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestLoopHeadBound(t *testing.T) {
	distinct := func(i int) *lockState {
		return held(false, newLockPath(pathRootValue, nil, nil, fmt.Sprint(i)))
	}
	h := &loopHead{entries: []*lockState{distinct(0)}}
	for i := 1; i < maxHeadStates; i++ {
		if added, over := h.admit(distinct(i)); !added || over {
			t.Fatalf("state %d of %d: added %t, over %t, want it accepted", i+1, maxHeadStates, added, over)
		}
	}
	if added, over := h.admit(distinct(1)); added || over {
		t.Errorf("a state already in a full set: added %t, over %t, want neither", added, over)
	}
	if added, over := h.admit(distinct(maxHeadStates)); added || !over {
		t.Errorf("state %d: added %t, over %t, want over", maxHeadStates+1, added, over)
	}
}
