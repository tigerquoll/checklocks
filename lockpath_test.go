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
