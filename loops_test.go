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
	"go/token"
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

func N(n, m int) {
	for i := 0; i < n; i++ {
		o := get(1)
		_ = o
		for j := 0; j < m; j++ {
			in := get(2)
			_ = in
		}
	}
	for k := 0; k < n; k++ {
		s := get(3)
		_ = s
	}
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

// dropped makes a marker holding the keys of the paths, with no regions.
func dropped(paths ...lockPath) map[pathKey]regionSet {
	m := make(map[pathKey]regionSet)
	for _, p := range paths {
		m[p.Key()] = nil
	}
	return m
}

// marked sets the marker of ls to the keys of the paths.
func marked(ls *lockState, paths ...lockPath) *lockState {
	ls.marker = dropped(paths...)
	return ls
}

// markedIn adds the path to the marker of ls with the regions.
func markedIn(ls *lockState, p lockPath, rs ...region) *lockState {
	if ls.marker == nil {
		ls.marker = make(map[pathKey]regionSet)
	}
	ls.marker[p.Key()] = regions(rs...)
	return ls
}

// topped sets the marker of ls to top.
func topped(ls *lockState) *lockState {
	ls.markerTop = true
	return ls
}

// toppedIn sets the marker of ls to top over the regions.
func toppedIn(ls *lockState, rs ...region) *lockState {
	ls.markerTop, ls.topRegions = true, regions(rs...)
	return ls
}

func regions(rs ...region) regionSet {
	var set regionSet
	for _, r := range rs {
		set = set.with(r)
	}
	return set
}

// nestFixture holds a value of each loop of N: an outer loop, one inside it, and a sibling after both.
type nestFixture struct {
	fn                    *ssa.Function
	outer, inner, sibling lockPath
	rOuter, rInner, rSib  region
}

func newNestFixture(t *testing.T, pc *passContext) nestFixture {
	t.Helper()
	pkg := buildTestPackage(t, "loops", loopSource)
	fx := nestFixture{fn: pkg.Func("N")}
	for _, b := range fx.fn.Blocks {
		for _, inst := range b.Instrs {
			c, ok := inst.(*ssa.Call)
			if !ok {
				continue
			}
			k, ok := c.Call.Args[0].(*ssa.Const)
			if !ok {
				continue
			}
			p := newLockPath(pathRootValue, c, nil, c.Name())
			r := pc.cfgOf(fx.fn).region[b]
			switch k.Int64() {
			case 1:
				fx.outer, fx.rOuter = p, r
			case 2:
				fx.inner, fx.rInner = p, r
			case 3:
				fx.sibling, fx.rSib = p, r
			}
		}
	}
	if !fx.rOuter.loop || !fx.rInner.loop || !fx.rSib.loop || fx.rOuter == fx.rInner || fx.rInner == fx.rSib || fx.rOuter == fx.rSib {
		t.Fatalf("N's loops have regions %v %v %v, want three loops", fx.rOuter, fx.rInner, fx.rSib)
	}
	return fx
}

func TestCyclicRegions(t *testing.T) {
	cyclicOf := func(src string) (*cfgFacts, int) {
		fn := buildTestPackage(t, "regions", src).Func("f")
		g := newCFGFacts(fn)
		return g, len(g.cyclic)
	}
	g, n := cyclicOf(lockSource("natural", 2))
	headers := map[*ssa.BasicBlock]bool{}
	for b := range g.cyclic {
		if !g.region[b].loop {
			t.Errorf("block %d of a natural loop is its own region", b.Index)
		}
		headers[g.region[b].block] = true
	}
	if n == 0 || len(headers) != 1 {
		t.Errorf("a loop with %d cyclic blocks has %d regions, want 1", n, len(headers))
	}
	g, n = cyclicOf(lockSource("goto", 2))
	for b := range g.cyclic {
		if g.region[b] != (region{block: b}) {
			t.Errorf("block %d of a goto cycle has region %v, want itself alone", b.Index, g.region[b])
		}
	}
	if n == 0 {
		t.Error("the goto shape has no cyclic block")
	}
	pc := &passContext{}
	nf := newNestFixture(t, pc)
	if !nf.rOuter.block.Dominates(nf.rInner.block) || nf.rSib.block == nf.rOuter.block {
		t.Error("the inner loop is not nested in the outer one")
	}
}

func TestMarkerBound(t *testing.T) {
	if !markerBoundOn {
		t.Skip("the marker bound is off")
	}
	fx := newLoopFixture(t)
	nf := newNestFixture(t, &passContext{})
	a, b, c := fx.inside.Key(), fx.before.Key(), fx.phi.Key()
	r1, r2, r3 := nf.rOuter, nf.rInner, nf.rSib
	da, db, dc := droppedKey{a, regions(r1)}, droppedKey{b, regions(r2)}, droppedKey{c, regions(r3)}
	mark := func(keys ...droppedKey) *lockState {
		l := newLockState()
		l.markDropped(keys)
		return l
	}
	for _, tc := range []struct {
		name  string
		from  *lockState
		keys  []droppedKey
		want  map[pathKey]regionSet
		top   regionSet
		isTop bool
		size  int
	}{
		{"nothing", newLockState(), nil, nil, nil, false, 0},
		{"one key", newLockState(), []droppedKey{da}, map[pathKey]regionSet{a: regions(r1)}, nil, false, 0},
		{"same key again", mark(da), []droppedKey{da}, map[pathKey]regionSet{a: regions(r1)}, nil, false, 0},
		{"second key", mark(da), []droppedKey{db}, nil, regions(r1, r2), true, 2},
		{"two keys at once", newLockState(), []droppedKey{da, db}, nil, regions(r1, r2), true, 2},
		{"two keys of one region", newLockState(), []droppedKey{da, {b, regions(r1)}}, nil, regions(r1), true, 2},
		{"top takes the regions of a key", mark(da, db), []droppedKey{dc}, nil, regions(r1, r2, r3), true, 0},
		{"top takes nothing new", mark(da, db), []droppedKey{da}, nil, regions(r1, r2), true, 0},
		{"top absorbs nothing", mark(da, db), nil, nil, regions(r1, r2), true, 0},
	} {
		size := tc.from.markDropped(tc.keys)
		if size != tc.size || tc.from.markerTop != tc.isTop || !maps.Equal(tc.from.topRegions, tc.top) || !maps.EqualFunc(tc.from.marker, tc.want, func(x, y regionSet) bool { return maps.Equal(x, y) }) {
			t.Errorf("%s: size %d, top %t over %v, marker %v; want size %d, top %t over %v, marker %v", tc.name, size, tc.from.markerTop, tc.from.topRegions, tc.from.marker, tc.size, tc.isTop, tc.top, tc.want)
		}
	}

	// Bounding after each step is the same as bounding the union.
	sets := [][]droppedKey{nil, {da}, {db}, {da, db}, {db, dc}, {da, db, dc}}
	for _, first := range sets {
		for _, second := range sets {
			once, twice := newLockState(), newLockState()
			once.markDropped(append(slices.Clone(first), second...))
			twice.markDropped(first)
			twice.markDropped(second)
			if once.markerTop != twice.markerTop || !maps.Equal(once.topRegions, twice.topRegions) || !maps.EqualFunc(once.marker, twice.marker, func(x, y regionSet) bool { return maps.Equal(x, y) }) {
				t.Errorf("%v then %v: at once top %t over %v marker %v, in two steps top %t over %v marker %v", first, second, once.markerTop, once.topRegions, once.marker, twice.markerTop, twice.topRegions, twice.marker)
			}
		}
	}
}

func TestDropDead(t *testing.T) {
	fx := newLoopFixture(t)
	pc := &passContext{}
	dies := func(p lockPath) bool { return pathDies(p, fx.body) }

	ls := held(false, fx.param, fx.inside)
	ls.dropDead(dies, pc.regionsOf)
	want := pc.regionsOf(fx.inside)
	if ls.count() != 1 || len(ls.marker) != 1 || len(want) != 1 || !maps.Equal(ls.marker[fx.inside.Key()], want) {
		t.Errorf("a dead acquired lock: %d held, marker %v, want 1 held and the dropped key in the marker with the region %v", ls.count(), ls.marker, want)
	}
	if _, ok := ls.lockedMutexes[fx.param.Key()]; !ok {
		t.Error("the parameter lock was dropped")
	}

	ls = held(true, fx.inside)
	ls.dropDead(dies, pc.regionsOf)
	if ls.count() != 0 || len(ls.marker) != 0 {
		t.Errorf("a dead assumed lock: %d held, marker %v, want none held and the marker empty", ls.count(), ls.marker)
	}

	shared := held(false, fx.inside)
	fork := shared.fork()
	fork.dropDead(dies, pc.regionsOf)
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
	pc := &passContext{}
	nf := newNestFixture(t, pc)
	for _, tc := range []struct {
		name     string
		set      []lockPath
		top      bool
		topIn    []lockPath
		held     []lockPath
		path     lockPath
		deferred bool
		want     bool
	}{
		{"marker empty", nil, false, nil, nil, fx.inside, false, false},
		{"dropped key", []lockPath{fx.inside}, false, nil, nil, fx.inside, false, true},
		{"dropped key but held", []lockPath{fx.inside}, false, nil, []lockPath{fx.inside}, fx.inside, false, false},
		{"value defined before the loop", []lockPath{fx.inside}, false, nil, nil, fx.before, false, false},
		{"other value-rooted key, set non-empty", []lockPath{fx.inside}, false, nil, nil, fx.phi, false, false},
		{"dropped index of a value on a parameter", []lockPath{index(fx.param, fx.phi)}, false, nil, nil, index(fx.param, fx.phi), false, true},
		{"dropped index of an index of a value", []lockPath{index(fx.param, index(fx.param, fx.inside))}, false, nil, nil, index(fx.param, index(fx.param, fx.inside)), false, true},
		{"deferred, value defined before the loop", []lockPath{fx.inside}, false, nil, nil, fx.before, true, true},
		{"deferred, marker empty", nil, false, nil, nil, fx.before, true, false},
		{"deferred, same key held", []lockPath{fx.inside}, false, nil, []lockPath{fx.before}, fx.before, true, false},
		{"deferred, parameter root", []lockPath{fx.inside}, false, nil, nil, fx.param, true, false},
		{"top, value defined in the loop", nil, true, []lockPath{fx.inside}, nil, fx.inside, false, true},
		{"top, loop-defined index of a parameter", nil, true, []lockPath{fx.inside}, nil, index(fx.param, fx.phi), false, true},
		{"top, value defined before the loop", nil, true, []lockPath{fx.inside}, nil, fx.before, false, false},
		{"top, parameter root", nil, true, []lockPath{fx.inside}, nil, fx.param, false, false},
		{"top, but held", nil, true, []lockPath{fx.inside}, []lockPath{fx.inside}, fx.inside, false, false},
		{"top, deferred, value defined before the loop", nil, true, []lockPath{fx.inside}, nil, fx.before, true, true},
		{"top, deferred, parameter root", nil, true, []lockPath{fx.inside}, nil, fx.param, true, false},
		{"top of a loop, value of that loop", nil, true, []lockPath{nf.inner}, nil, nf.inner, false, true},
		{"top of a loop, value of the loop around it", nil, true, []lockPath{nf.inner}, nil, nf.outer, false, false},
		{"top of a loop, value of a sibling loop", nil, true, []lockPath{nf.inner}, nil, nf.sibling, false, false},
		{"top of a loop, index defined in a sibling loop", nil, true, []lockPath{nf.inner}, nil, index(fx.param, nf.sibling), false, false},
		{"top of the outer loop, value of the loop inside it", nil, true, []lockPath{nf.outer}, nil, nf.inner, false, false},
		{"top of two loops, value of the second", nil, true, []lockPath{nf.outer, nf.sibling}, nil, nf.sibling, false, true},
		{"top of no loop, value of a loop", nil, true, nil, nil, nf.inner, false, false},
		{"top of no loop, deferred", nil, true, nil, nil, nf.inner, true, true},
	} {
		ls := marked(held(false, tc.held...), tc.set...)
		ls.markerTop = tc.top
		for _, p := range tc.topIn {
			ls.topRegions = ls.topRegions.union(pc.regionsOf(p))
		}
		if got := pc.silencesRelease(ls, tc.path, tc.deferred, token.NoPos); got != tc.want {
			t.Errorf("%s: silencesRelease = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// lockSource has n locks, taken or not by each of n branches, in a loop, a goto cycle, before a self-loop block or no cycle.
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
	case "self":
		branches()
		b.WriteString("again:\n\tif c() {\n\t\tgoto again\n\t}\n")
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

// armSource has a loop whose body takes one of n locks, each on a value of its own, then n2 conditional no-ops.
func armSource(n, n2 int) string {
	var b strings.Builder
	b.WriteString("package p\n\ntype Mutex struct{}\n\nfunc (*Mutex) Lock()   {}\nfunc (*Mutex) Unlock() {}\n\ntype T struct{ mu Mutex }\n\nfunc get() *T { return nil }\n\nfunc work() {}\n\n")
	b.WriteString("func f(n, c int, d func() bool) {\n\tfor i := 0; i < n; i++ {\n\t\tswitch c {\n")
	for i := 0; i < n; i++ {
		if i == n-1 {
			b.WriteString("\t\tdefault:\n")
		} else {
			fmt.Fprintf(&b, "\t\tcase %d:\n", i)
		}
		fmt.Fprintf(&b, "\t\t\tx%d := get()\n\t\t\tx%d.mu.Lock()\n", i, i)
	}
	b.WriteString("\t\t}\n")
	for i := 0; i < n2; i++ {
		b.WriteString("\t\tif d() {\n\t\t\twork()\n\t\t}\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func TestCycleStateCap(t *testing.T) {
	if !markerBoundOn {
		t.Skip("the marker bound is off")
	}
	capText := fmt.Sprintf("checklocks gave up on this function: more than %d lock states reach one block in a loop", maxCycleStates)
	budgetText := fmt.Sprintf("checklocks gave up on this function: more than %d block walks", maxFunctionWalks)
	giveUp := func(src string) string {
		pkg := buildTestPackage(t, "cap", src)
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
		for _, r := range pc.pending {
			if strings.Contains(r.msg, "gave up") {
				return r.msg
			}
		}
		return ""
	}
	if maxCycleStates != 64 {
		t.Fatalf("maxCycleStates = %d, want 64", maxCycleStates)
	}
	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"natural 6", lockSource("natural", 6), ""},
		{"natural 7", lockSource("natural", 7), capText},
		{"goto 6", lockSource("goto", 6), ""},
		{"goto 7", lockSource("goto", 7), capText},
		{"self 6", lockSource("self", 6), ""},
		{"self 7", lockSource("self", 7), capText},
		{"acyclic 7", lockSource("acyclic", 7), ""},
		{"acyclic 9", lockSource("acyclic", 9), ""},
		{"64 arms", armSource(64, 0), ""},
		{"65 arms", armSource(65, 0), capText},
		{"64 arms and 6 branches", armSource(64, 6), budgetText},
	} {
		if got := giveUp(tc.src); got != tc.want {
			t.Errorf("%s: gave up with %q, want %q", tc.name, got, tc.want)
		}
	}
}
