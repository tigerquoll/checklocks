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
	"testing"

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

// nestedClosure returns the literal in fn, the literal nested in it, and a state with the
// nested one's free variables bound as checkClosure binds them.
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

// TestIdentityFreeVarChain pins that a nested closure's free variable is the enclosing
// closure's variable when it captures it, and another lock when it captures another.
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

// TestIdentityAliasScope pins that an alias and an entry lock of one function's parameter
// do not reach another function's parameter of the same name.
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
