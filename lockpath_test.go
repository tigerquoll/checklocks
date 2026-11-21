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
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
)

type pathCase struct {
	want string
	path lockPath
}

func testLockPaths() []pathCase {
	mu := types.NewVar(token.NoPos, nil, "mu", types.Typ[types.Int])
	param := newLockPath(pathRootParam, nil, nil, "t")
	idx := newLockPath(pathRootValue, nil, nil, "{*ssa.Const:f:k0}")
	return []pathCase{
		{"{param:t}", param},
		{"{global:G}", newLockPath(pathRootGlobal, nil, nil, "G")},
		{"{freevar:fv}", newLockPath(pathRootFreeVar, nil, nil, "fv")},
		{"{*ssa.Alloc:f}", newLockPath(pathRootValue, nil, nil, "{*ssa.Alloc:f}")},
		{"*({param:t})", param.extend(lockStep{kind: stepDeref})},
		{"{param:t}.mu", param.extend(lockStep{kind: stepField, field: mu})},
		{"&({param:t}.mu)", param.extend(lockStep{kind: stepFieldAddr, field: mu})},
		{"{param:t}[{*ssa.Const:f:k0}]", param.extend(lockStep{kind: stepIndex, index: idx})},
		{"&({param:t}[{*ssa.Const:f:k0}])", param.extend(lockStep{kind: stepIndexAddr, index: idx})},
		{"{param:t}[{*ssa.Const:f:k0}]", param.extend(lockStep{kind: stepLookup, index: idx})},
		{"{param:t}[3]", param.extend(lockStep{kind: stepConstIndex, n: 3})},
		{"{param:t}[{param:t}[2]]", param.extend(lockStep{kind: stepIndex, index: param.extend(lockStep{kind: stepConstIndex, n: 2})})},
		{"*(&({param:t}.mu))", param.extend(lockStep{kind: stepFieldAddr, field: mu}).extend(lockStep{kind: stepDeref})},
	}
}

// TestLockPathRender pins the text of each root and step kind.
func TestLockPathRender(t *testing.T) {
	for _, c := range testLockPaths() {
		if got := c.path.String(); got != c.want {
			t.Errorf("path renders %q, want %q", got, c.want)
		}
	}
}

// TestLockPathFresh pins that the cached text equals a render from root and steps.
func TestLockPathFresh(t *testing.T) {
	for _, c := range testLockPaths() {
		if got := c.path.fresh(); got != c.path.String() {
			t.Errorf("%q: fresh render %q differs from cached %q", c.want, got, c.path.String())
		}
	}
}

// TestLockPathExtendIndependent pins that two extensions of one base do not share steps.
func TestLockPathExtendIndependent(t *testing.T) {
	base := newLockPath(pathRootParam, nil, nil, "t").extend(lockStep{kind: stepDeref})
	// Spare capacity would let an append write into shared storage.
	base.steps = append(make([]lockStep, 0, 8), base.steps...)
	a := base.extend(lockStep{kind: stepConstIndex, n: 1})
	b := base.extend(lockStep{kind: stepConstIndex, n: 2})
	if a.String() != "*({param:t})[1]" || b.String() != "*({param:t})[2]" {
		t.Errorf("extensions render %q and %q", a.String(), b.String())
	}
	if a.steps[1].n != 1 || b.steps[1].n != 2 || len(base.steps) != 1 {
		t.Errorf("extensions share steps: %v %v %v", a.steps, b.steps, base.steps)
	}
	if a.fresh() != a.String() || b.fresh() != b.String() {
		t.Error("an extension's cached text differs from a fresh render")
	}
}

const keySource = `package p

type T struct{ a, b int }

var Mu int

func A(t *T, i *T) {}

func B(t *T) {}
`

func testParam(t *testing.T, pkg *ssa.Package, fn string, i int) lockPath {
	t.Helper()
	p := pkg.Func(fn).Params[i]
	return newLockPath(pathRootParam, p, p.Object(), p.Name())
}

// TestLockPathKey pins which paths share a key.
func TestLockPathKey(t *testing.T) {
	pkg := buildTestPackage(t, "a", keySource)
	other := buildTestPackage(t, "b", keySource)
	aT, aI, bT := testParam(t, pkg, "A", 0), testParam(t, pkg, "A", 1), testParam(t, pkg, "B", 0)

	if aT.String() != bT.String() || aT.Key() == bT.Key() {
		t.Errorf("two parameters named t: renderings %q and %q, keys %q and %q", aT.String(), bT.String(), aT.Key(), bT.Key())
	}
	if aT.Key() != testParam(t, pkg, "A", 0).Key() {
		t.Error("one parameter rooted twice has two keys")
	}
	global := func(p *ssa.Package) lockPath {
		g := p.Members["Mu"].(*ssa.Global)
		return newLockPath(pathRootGlobal, g, g.Object(), g.Name())
	}
	if g, o := global(pkg), global(other); g.String() != o.String() || g.Key() == o.Key() {
		t.Errorf("globals of two packages: renderings %q and %q, keys %q and %q", g.String(), o.String(), g.Key(), o.Key())
	}

	fa := types.NewVar(token.NoPos, nil, "a", types.Typ[types.Int])
	fb := types.NewVar(token.NoPos, nil, "b", types.Typ[types.Int])
	distinct := map[string]pathKey{
		"field and constant index, same n":  aT.extend(lockStep{kind: stepField, field: fa, n: 1}).Key(),
		"constant index":                    aT.extend(lockStep{kind: stepConstIndex, n: 1}).Key(),
		"field named b, n 0":                aT.extend(lockStep{kind: stepField, field: fb}).Key(),
		"field named a, n 0":                aT.extend(lockStep{kind: stepField, field: fa}).Key(),
		"index of a field":                  aT.extend(lockStep{kind: stepIndex, index: aI.extend(lockStep{kind: stepField, field: fa})}).Key(),
		"field of an index":                 aT.extend(lockStep{kind: stepIndex, index: aI}).extend(lockStep{kind: stepField, field: fa}).Key(),
		"nil-value param":                   newLockPath(pathRootParam, nil, nil, "t").Key(),
		"nil-value free variable":           newLockPath(pathRootFreeVar, nil, nil, "t").Key(),
		"field address, field a, n 0":       aT.extend(lockStep{kind: stepFieldAddr, field: fa}).Key(),
		"index address, same index as aT.i": aT.extend(lockStep{kind: stepIndexAddr, index: aI}).Key(),
		"index, same index as aT.i":         aT.extend(lockStep{kind: stepIndex, index: aI}).Key(),
	}
	seen := make(map[pathKey]string)
	for name, key := range distinct {
		if prev, ok := seen[key]; ok {
			t.Errorf("%q and %q share key %q", prev, name, key)
		}
		seen[key] = name
	}

	blockless := newLockPath(pathRootValue, &ssa.Call{}, nil, "{*ssa.Call:7}")
	if blockless.Key() == "" {
		t.Error("a value root on a blockless instruction has no key")
	}
}

// TestLockPathExtendKey pins that extend builds a key without changing its base.
func TestLockPathExtendKey(t *testing.T) {
	base := newLockPath(pathRootParam, nil, nil, "t").extend(lockStep{kind: stepDeref})
	before := base.Key()
	a := base.extend(lockStep{kind: stepConstIndex, n: 1})
	b := base.extend(lockStep{kind: stepConstIndex, n: 2})
	if base.Key() != before {
		t.Error("extend changed the base's key")
	}
	if a.Key() == b.Key() || a.Key() == before {
		t.Errorf("extensions share keys: %q %q %q", before, a.Key(), b.Key())
	}
	if a.freshKey() != a.Key() || b.freshKey() != b.Key() {
		t.Error("an extension's cached key differs from a fresh encoding")
	}
}
