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
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

const closureSource = `package p

type T struct{ mu int }

func shared() {
	t := &T{}
	go func() {
		func() { println(t.mu) }()
	}()
}

func distinct() {
	t := &T{}
	go func() {
		println(t.mu)
		{
			t := &T{}
			func() { println(t.mu) }()
		}
	}()
}
`

// nestedClosure returns fn's literal, its nested literal, and a state binding the nested one's free variables as checkClosure does.
func nestedClosure(t *testing.T, pkg *ssa.Package, fn string) (outer, inner *ssa.Function, ls *lockState) {
	t.Helper()
	outer = pkg.Func(fn).AnonFuncs[0]
	inner = outer.AnonFuncs[0]
	ls = newLockState()
	for _, block := range outer.Blocks {
		for _, instr := range block.Instrs {
			if mc, ok := instr.(*ssa.MakeClosure); ok && mc.Fn == inner {
				for i, fv := range inner.FreeVars {
					ls.store(fv, mc.Bindings[i])
				}
			}
		}
	}
	return outer, inner, ls
}

// TestIdentityGlobals pins that two globals of one name in two packages are two locks.
func TestIdentityGlobals(t *testing.T) {
	global := func(p *ssa.Package) resolvedValue {
		return makeResolvedValue(p.Members["Mu"].(*ssa.Global), nil)
	}
	one, other := global(buildTestPackage(t, "a", keySource)), global(buildTestPackage(t, "b", keySource))
	ls := newLockState()
	ls.lockField(one, true)
	if _, held := ls.isHeld(other, false); held {
		t.Error("locking one package's Mu holds the other's")
	}
	if _, held := ls.isHeld(one, false); !held {
		t.Error("the locked Mu is not held")
	}
}

// TestIdentityFreeVarChain pins that a nested closure's free variable is the variable it captures.
func TestIdentityFreeVarChain(t *testing.T) {
	pkg := buildTestPackage(t, "a", closureSource)

	outer, inner, ls := nestedClosure(t, pkg, "distinct")
	outerT, innerT := makeResolvedValue(outer.FreeVars[0], nil), makeResolvedValue(inner.FreeVars[0], nil)
	ls.lockField(outerT, true)
	if _, held := ls.isHeld(innerT, false); held {
		t.Error("a different local of the same name is held through the outer variable")
	}

	outer, inner, ls = nestedClosure(t, pkg, "shared")
	outerT, innerT = makeResolvedValue(outer.FreeVars[0], nil), makeResolvedValue(inner.FreeVars[0], nil)
	ls.lockField(outerT, true)
	if _, held := ls.isHeld(innerT, false); !held {
		t.Error("the captured variable is not held through the inner free variable")
	}
	if _, locked := ls.lockField(innerT, true); locked {
		t.Error("the captured variable was locked twice")
	}
}

// TestIdentityAliasScope pins that A's alias and entry lock do not reach B's parameter of the same name.
func TestIdentityAliasScope(t *testing.T) {
	a, b := twinLocks(t)
	pkg := buildTestPackage(t, "a", keySource)
	other := makeResolvedValue(ssa.NewConst(nil, pkg.Func("A").Params[0].Type()), nil)

	ls := newLockState()
	ls.addAlias(a, other)
	aPath, _ := a.valueAndObject(ls)
	bPath, _ := b.valueAndObject(ls)
	if ls.aliasPath(aPath).Key() == aPath.Key() {
		t.Error("the alias did not redirect its own path")
	}
	if got := ls.aliasPath(bPath); got.Key() != bPath.Key() {
		t.Errorf("an alias of A's t redirects B's t to %q", got.String())
	}

	ls = newLockState()
	ls.lockField(a, true)
	ls.lockField(b, true)
	entry := map[pathKey]bool{aPath.Key(): true}
	if n := ls.countOnReturn(entry); n != 1 {
		t.Errorf("countOnReturn counts %d locks, want only B's", n)
	}
	ls.setAssumed(aPath, true)
	ls.setAssumed(bPath, true)
	text, summary := ls.acquiredString(entry)
	if want := fmt.Sprintf("%q %t", string(aPath.Key()), true); summary != want || text != "{param:t} exclusively" {
		t.Errorf("acquiredString is %q, %q; want only A's lock", text, summary)
	}
}

const deferSource = `package p

type Mutex struct{}

func (*Mutex) Lock()   {}
func (*Mutex) Unlock() {}

type Plain struct{}

func (*Plain) Do() {}

type Aliased struct{ n int }

func needsLock(m *Mutex)  {}
func useAliased(a *Aliased) {}
func makeFresh(p *Plain)  {}
func noop()               {}
func heldOnExit(m *Mutex) {}
func excludedOnEntry(m *Mutex) {}

func relevant(m *Mutex, p *Plain, a *Aliased) {
	defer m.Unlock()
	defer needsLock(m)
	defer useAliased(a)
	defer makeFresh(p)
	defer func() {}()
}

func irrelevant(m *Mutex, p *Plain, fn func(), ch chan int, i interface{ M() }) {
	defer p.Do()
	defer fn()
	defer close(ch)
	defer i.M()
	defer noop()
}

func more(m *Mutex) {
	defer func() { m.Unlock() }()
	defer heldOnExit(m)
	defer excludedOnEntry(m)
}

func ordered(m, n *Mutex) {
	defer noop()
	defer m.Unlock()
	defer n.Unlock()
}
`

// noFactsPC is a passContext for states whose defers name none of deferPC's faked objects.
func noFactsPC() *passContext {
	return deferPC(token.NewFileSet())
}

func deferPC(fset *token.FileSet) *passContext {
	pass := &analysis.Pass{
		Fset: fset,
		ImportObjectFact: func(o types.Object, fact analysis.Fact) bool {
			switch f := fact.(type) {
			case *lockFunctionFacts:
				switch o.Name() {
				case "needsLock":
					f.HeldOnEntry = map[string]functionGuardInfo{"m": {}}
					return true
				case "heldOnExit":
					f.HeldOnExit = map[string]functionGuardInfo{"m": {}}
					return true
				case "excludedOnEntry":
					f.ExcludedOnEntry = map[string]functionGuardInfo{"m": {}}
					return true
				}
			case *lockTypeFacts:
				if o.Name() == "Aliased" {
					f.Aliases = []typeAlias{{}}
					return true
				}
			case *freshFacts:
				if o.Name() == "makeFresh" {
					f.FreshParams = []int{0}
					return true
				}
			}
			return false
		},
	}
	return &passContext{pass: pass, collect: true, expectations: newExpectations(pass, checkLocksAnnotations, true)}
}

// deferSites returns the defer instructions of fn in source order.
func deferSites(fn *ssa.Function) []*ssa.Defer {
	var ds []*ssa.Defer
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			if d, ok := inst.(*ssa.Defer); ok {
				ds = append(ds, d)
			}
		}
	}
	return ds
}

func TestDeferRelevant(t *testing.T) {
	pkg := buildTestPackage(t, "defers", deferSource)
	pc := deferPC(pkg.Prog.Fset)
	for _, tc := range []struct {
		fn    string
		count int
		want  bool
	}{{"relevant", 5, true}, {"more", 3, true}, {"irrelevant", 5, false}} {
		fn, want := tc.fn, tc.want
		sites := deferSites(pkg.Func(fn))
		if len(sites) != tc.count {
			t.Fatalf("%s has %d defers, want %d", fn, len(sites), tc.count)
		}
		for i, d := range sites {
			if got := pc.deferRelevant(d); got != want {
				t.Errorf("%s defer %d: relevant = %t, want %t", fn, i, got, want)
			}
		}
	}
}

// sameKey reports sameElement(a, b) and fails the test when the element keys disagree with it.
func sameKey(t *testing.T, pc *passContext, fn *ssa.Function, name string, a, b *lockState) bool {
	t.Helper()
	same := pc.sameElement(fn, a, b)
	if keys := pc.elementKey(fn, a) == pc.elementKey(fn, b); keys != same {
		t.Errorf("%s: keys equal = %t but same element = %t", name, keys, same)
	}
	return same
}

func TestEmptyElementKey(t *testing.T) {
	pc := noFactsPC()
	if got := pc.buildElementKey(nil, newLockState()); got != emptyElementKey {
		t.Errorf("the key built for an empty state is %q, want %q", got, emptyElementKey)
	}
}

func TestSameElement(t *testing.T) {
	fx := newLoopFixture(t)
	a, b := fx.param, fx.global
	pc := noFactsPC()
	base := func() *lockState { return held(false, a) }
	if !sameKey(t, pc, nil, "equal", base(), base()) {
		t.Error("equal states differ")
	}
	for _, tc := range []struct {
		name        string
		left, right *lockState
	}{
		{"held", base(), held(false, b)},
		{"assumed", base(), held(true, a)},
		{"marker", base(), marked(base(), fx.inside)},
		{"other marker", marked(base(), fx.inside), marked(base(), fx.before)},
		{"top", base(), topped(base())},
		{"top and a key", marked(base(), fx.inside), topped(base())},
	} {
		if sameKey(t, pc, nil, tc.name, tc.left, tc.right) || sameKey(t, pc, nil, tc.name, tc.right, tc.left) {
			t.Errorf("states differing in %s are the same element", tc.name)
		}
	}

	pkg := buildTestPackage(t, "defers", deferSource)
	pc = deferPC(pkg.Prog.Fset)
	sites := deferSites(pkg.Func("ordered"))
	noop, first, second := sites[0], sites[1], sites[2]
	withDefers := func(ds ...*ssa.Defer) *lockState {
		ls := marked(base(), fx.inside)
		ls.defers = ds
		return ls
	}

	plain, extra := withDefers(first), withDefers(noop, first)
	if !sameKey(t, pc, nil, "no-op defer", plain, extra) {
		t.Error("states differing in a no-op defer are not the same element")
	}
	if sameKey(t, pc, nil, "relevant defer", withDefers(first), withDefers(first, second)) {
		t.Error("states differing in a relevant defer are the same element")
	}
	ab, ba := withDefers(first, second), withDefers(second, first)
	if sameKey(t, pc, nil, "defer order", ab, ba) {
		t.Error("states with the relevant defers in another order are the same element")
	}
	withAlias := func(to lockPath) *lockState {
		ls := base()
		ls.aliases[a.Key()] = to
		return ls
	}
	if sameKey(t, pc, nil, "alias", base(), withAlias(b)) || sameKey(t, pc, nil, "other alias", withAlias(b), withAlias(fx.before)) || !sameKey(t, pc, nil, "same alias", withAlias(b), withAlias(b)) {
		t.Error("states are the same element exactly when their aliases are equal")
	}
}

func TestClassKey(t *testing.T) {
	fx := newLoopFixture(t)
	pc := noFactsPC()
	a := held(false, fx.param)
	for _, other := range []*lockState{marked(held(false, fx.param), fx.inside), marked(held(false, fx.param), fx.before), topped(held(false, fx.param))} {
		if pc.classKey(nil, a) != pc.classKey(nil, other) {
			t.Error("elements that differ only in the marker have different class keys")
		}
	}
	if pc.classKey(nil, a) == pc.classKey(nil, held(false, fx.global)) {
		t.Error("elements with different locks share a class key")
	}
}

func TestEmptyStateKeys(t *testing.T) {
	fx := newLoopFixture(t)
	pkg := buildTestPackage(t, "defers", deferSource)
	pc := deferPC(pkg.Prog.Fset)
	fns := []*ssa.Function{nil, pkg.Func("ordered")}
	sites := deferSites(pkg.Func("ordered"))
	withDefer := func(d *ssa.Defer) *lockState {
		ls := newLockState()
		ls.defers = []*ssa.Defer{d}
		return ls
	}
	aliased := newLockState()
	aliased.aliases[fx.param.Key()] = fx.global
	for _, tc := range []struct {
		name  string
		other *lockState
		same  bool
	}{
		{"marker", marked(newLockState(), fx.inside), false},
		{"top", topped(newLockState()), false},
		{"alias", aliased, false},
		{"relevant defer", withDefer(sites[1]), false},
		{"no-op defer", withDefer(sites[0]), true},
	} {
		for _, fn := range fns {
			if got := sameKey(t, pc, fn, tc.name, newLockState(), tc.other); got != tc.same {
				t.Errorf("empty state against %s only (fn %v): same element = %t, want %t", tc.name, fn, got, tc.same)
			}
		}
	}
}

const slotSource = `package p

type Aliased struct{ mu int }

var G *Aliased

func sink(*int) {}

func use(*Aliased) {}

func slots(t, u *Aliased) {
	G = t
	use(G)
	n := 0
	sink(&n)
	n = 1
}
`

// slotFixture stores a global it passes as an alias-typed argument and a local no lock resolution loads.
type slotFixture struct {
	fn            *ssa.Function
	global, local ssa.Value
	val, other    ssa.Value
}

func newSlotFixture(t *testing.T) slotFixture {
	t.Helper()
	pkg := buildTestPackage(t, "slots", slotSource)
	fx := slotFixture{fn: pkg.Func("slots"), global: pkg.Members["G"].(*ssa.Global)}
	fx.val, fx.other = fx.fn.Params[0], fx.fn.Params[1]
	for _, b := range fx.fn.Blocks {
		for _, inst := range b.Instrs {
			if a, ok := inst.(*ssa.Alloc); ok {
				fx.local = a
			}
		}
	}
	if fx.local == nil {
		t.Fatal("slots has no local to store to")
	}
	return fx
}

func TestSameElementStored(t *testing.T) {
	fx := newSlotFixture(t)
	lf := newLoopFixture(t)
	pc := noFactsPC()
	if si := pc.relevantSlots(fx.fn); !si.slots[fx.global] || si.slots[fx.local] {
		t.Fatalf("relevant slots %v, want the global and not the local", si.slots)
	}
	state := func(slot, val ssa.Value) *lockState {
		ls := held(false, lf.param)
		if slot != nil {
			ls.stored[slot] = val
		}
		return ls
	}
	aliased := func(to lockPath) *lockState {
		ls := held(false, lf.param)
		ls.aliases[lf.param.Key()] = to
		return ls
	}
	substituted := func() *lockState {
		ls := held(false, lf.param)
		ls.substitute(fx.val, lf.param, nil)
		return ls
	}
	for _, tc := range []struct {
		name        string
		left, right *lockState
		same        bool
	}{
		{"relevant slot, other value", state(fx.global, fx.val), state(fx.global, fx.other), false},
		{"relevant slot, presence", state(fx.global, fx.val), state(nil, nil), false},
		{"relevant slot, same value", state(fx.global, fx.val), state(fx.global, fx.val), true},
		{"irrelevant slot, other value", state(fx.local, fx.val), state(fx.local, fx.other), true},
		{"irrelevant slot, presence", state(fx.local, fx.val), state(nil, nil), true},
		{"alias, other target", aliased(lf.global), aliased(lf.before), false},
		{"alias, presence", aliased(lf.global), state(nil, nil), false},
		{"substitution, presence", substituted(), state(nil, nil), true},
	} {
		if got := sameKey(t, pc, fx.fn, tc.name, tc.left, tc.right); got != tc.same {
			t.Errorf("%s: same element = %t, want %t", tc.name, got, tc.same)
		}
	}
}

func TestNoOpDeferInvariant(t *testing.T) {
	pkg := buildTestPackage(t, "defers", deferSource)
	fn := pkg.Func("irrelevant")
	for _, mode := range []string{"walk", "direct"} {
		pc := deferPC(pkg.Prog.Fset)
		if mode == "walk" {
			pc.emit, pc.current = emitWalk, &blockRecord{}
		} else {
			pc.emit = emitDirect
		}
		wc := &walkContext{fn: fn, lff: &lockFunctionFacts{}}
		for i, d := range deferSites(fn) {
			if pc.deferRelevant(d) {
				t.Errorf("irrelevant defer %d rated relevant", i)
				continue
			}
			ls := newLockState()
			ls.lockField(makeResolvedValue(fn.Params[0], nil), true)
			locked, marker, aliases := maps.Clone(ls.lockedMutexes), maps.Clone(ls.marker), maps.Clone(ls.aliases)
			pc.checkCall(newCallSite(d), wc, ls)
			if !reflect.DeepEqual(ls.lockedMutexes, locked) || !reflect.DeepEqual(ls.marker, marker) || !reflect.DeepEqual(ls.aliases, aliases) {
				t.Errorf("%s: no-op defer %d changed the state", mode, i)
			}
			if mode == "walk" && !reflect.DeepEqual(pc.current.parity, blockParity{}) {
				t.Errorf("%s: no-op defer %d left a decision, finding or observation", mode, i)
			}
			if len(pc.pending) != 0 {
				t.Errorf("%s: no-op defer %d reported %d findings", mode, i, len(pc.pending))
			}
		}
	}
}
