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

func ordered(m, n *Mutex) {
	defer noop()
	defer m.Unlock()
	defer n.Unlock()
}
`

// noFactsPC is a passContext whose pass has no facts.
func noFactsPC() *passContext {
	return deferPC(token.NewFileSet())
}

func deferPC(fset *token.FileSet) *passContext {
	pass := &analysis.Pass{
		Fset: fset,
		ImportObjectFact: func(o types.Object, fact analysis.Fact) bool {
			switch f := fact.(type) {
			case *lockFunctionFacts:
				if o.Name() == "needsLock" {
					f.HeldOnEntry = map[string]functionGuardInfo{"m": {}}
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
	for fn, want := range map[string]bool{"relevant": true, "irrelevant": false} {
		sites := deferSites(pkg.Func(fn))
		if len(sites) != 5 {
			t.Fatalf("%s has %d defers, want 5", fn, len(sites))
		}
		for i, d := range sites {
			if got := pc.deferRelevant(d); got != want {
				t.Errorf("%s defer %d: relevant = %t, want %t", fn, i, got, want)
			}
		}
	}
}

func TestSameElement(t *testing.T) {
	fx := newLoopFixture(t)
	a, b := fx.param, fx.global
	pc := noFactsPC()
	base := func() *lockState { return held(false, a) }
	if !pc.sameElement(base(), base()) {
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
	} {
		if pc.sameElement(tc.left, tc.right) || pc.sameElement(tc.right, tc.left) {
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
	if !pc.sameElement(plain, extra) {
		t.Error("states differing in a no-op defer are not the same element")
	}
	h := &loopHead{entries: []*lockState{plain}}
	if added, over := h.admit(pc, extra); added || over {
		t.Errorf("admitting a state that differs in a no-op defer: added %t, over %t, want neither", added, over)
	}

	if pc.sameElement(withDefers(first), withDefers(first, second)) {
		t.Error("states differing in a relevant defer are the same element")
	}
	h = &loopHead{entries: []*lockState{withDefers(first)}}
	if added, over := h.admit(pc, withDefers(first, second)); !added || over {
		t.Errorf("admitting a state that differs in a relevant defer: added %t, over %t, want added", added, over)
	}

	ab, ba := withDefers(first, second), withDefers(second, first)
	ba.marker = dropped(fx.before)
	if pc.sameElement(ab, ba) {
		t.Error("states with the relevant defers in another order are the same element")
	}
	ba.joinMarker(pc, []*lockState{ab})
	if !maps.Equal(ba.marker, dropped(fx.before)) {
		t.Errorf("joining entries with the defers in another order changed the marker to %v", ba.marker)
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
