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
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
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
		if slices.ContainsFunc(b.Preds, b.Dominates) {
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

// lockSource has n locks, taken or not by each of n branches, in a loop, a goto cycle or no cycle.
func lockSource(shape string, n int) string {
	var b strings.Builder
	b.WriteString("package p\n\ntype Mutex struct{}\n\nfunc (*Mutex) Lock()   {}\nfunc (*Mutex) Unlock() {}\n\ntype Locks struct {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\tm%d Mutex\n", i)
	}
	b.WriteString("}\n\nfunc f(l *Locks, c func() bool) {\n")
	branches := func() {
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, "\tif c() {\n\t\tl.m%d.Lock()\n\t}\n", i)
		}
	}
	switch shape {
	case "natural":
		b.WriteString("\tfor c() {\n")
		branches()
		b.WriteString("\t}\n")
	case "goto":
		b.WriteString("\tif c() {\n\t\tgoto second\n\t}\nfirst:\n")
		branches()
		b.WriteString("second:\n\tif c() {\n\t\tgoto first\n\t}\n")
	default:
		branches()
	}
	b.WriteString("}\n")
	return b.String()
}

func TestCycleStateCap(t *testing.T) {
	gaveUp := func(shape string, n int) bool {
		pkg := buildTestPackage(t, "cap", lockSource(shape, n))
		pass := &analysis.Pass{
			Fset:             pkg.Prog.Fset,
			Report:           func(analysis.Diagnostic) {},
			ImportObjectFact: func(types.Object, analysis.Fact) bool { return false },
		}
		pc := &passContext{
			expectations: newExpectations(pass, checkLocksAnnotations, true),
			pass:         pass,
			collect:      true,
			functions:    make(map[*ssa.Function]struct{}),
		}
		pc.checkFunction(callSite{kind: siteEntry}, nil, pkg.Func("f"), &lockFunctionFacts{}, nil, false)
		want := fmt.Sprintf("checklocks gave up on this function: more than %d lock states reach one block in a loop", maxCycleStates)
		for _, r := range pc.pending {
			if r.msg == want {
				return true
			}
			if strings.Contains(r.msg, "gave up") {
				t.Errorf("%s with %d locks gave up with %q", shape, n, r.msg)
			}
		}
		return false
	}
	if maxCycleStates != 64 {
		t.Fatalf("maxCycleStates = %d, want 64", maxCycleStates)
	}
	for _, tc := range []struct {
		shape string
		n     int
		want  bool
	}{
		{"natural", 6, false},
		{"natural", 7, true},
		{"goto", 6, false},
		{"goto", 7, true},
		{"acyclic", 7, false},
		{"acyclic", 9, false},
	} {
		if got := gaveUp(tc.shape, tc.n); got != tc.want {
			t.Errorf("%s with %d locks: gave up = %t, want %t", tc.shape, tc.n, got, tc.want)
		}
	}
}

// The entry record comes before the records of a literal walked inside the entry block.
func TestEntryRecordFirst(t *testing.T) {
	pkg := buildTestPackage(t, "entry", `package p

func f(c func() bool) {
	func() { _ = c() }()
	if c() {
		_ = c()
	}
}
`)
	pass := &analysis.Pass{
		Fset:             pkg.Prog.Fset,
		Report:           func(analysis.Diagnostic) {},
		ImportObjectFact: func(types.Object, analysis.Fact) bool { return false },
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		collect:      true,
		functions:    make(map[*ssa.Function]struct{}),
		emit:         emitDirect,
	}
	fn := pkg.Func("f")
	wc := &walkContext{fn: fn, lff: &lockFunctionFacts{}, fresh: pc.computeFresh(fn, nil), context: "f", returnStates: map[string]string{}}
	entry := newLockState()
	pc.solve(wc, fn.Blocks[0], entry)
	if len(pc.records) == 0 {
		t.Fatal("the solve made no records")
	}
	if first := pc.records[0]; first.wc != wc || first.block != fn.Blocks[0] || first.parent != entry {
		t.Errorf("first record is block %d of %s, want the entry block on the entry state", first.block.Index, first.wc.fn)
	}
	if !slices.ContainsFunc(pc.records, func(r *blockRecord) bool { return r.wc.fn != fn }) {
		t.Error("no record of the literal in the entry block")
	}
}
