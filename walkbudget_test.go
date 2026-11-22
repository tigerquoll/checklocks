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
	"go/types"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

const budgetSource = `package p

type Mutex struct{}

func needsLock(m *Mutex) {}

func many(c bool) int {
	n := 0
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	if c {
		n++
	}
	return n
}

func few(m *Mutex) { needsLock(m) }

func sinkSlot(**Mutex) {}

func diamonds(a, b *Mutex, c bool) {
	p := a
	sinkSlot(&p)
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	if c {
		p = a
	} else {
		p = b
	}
	needsLock(p)
}
`

func TestWalkBudget(t *testing.T) {
	if maxFunctionWalks != 40000 {
		t.Errorf("maxFunctionWalks = %d, want 40000", maxFunctionWalks)
	}
	defer func(old int) { maxFunctionWalks = old }(maxFunctionWalks)
	maxFunctionWalks = 10

	pkg := buildTestPackage(t, "budget", budgetSource)
	pass := &analysis.Pass{
		Fset:   pkg.Prog.Fset,
		Report: func(analysis.Diagnostic) {},
		ImportObjectFact: func(o types.Object, fact analysis.Fact) bool {
			if f, ok := fact.(*lockFunctionFacts); ok && o.Name() == "needsLock" {
				f.HeldOnEntry = map[string]functionGuardInfo{"m": {Resolver: &parameterGuard{Index: 0}, Exclusive: true}}
				return true
			}
			return false
		},
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		collect:      true,
		functions:    make(map[*ssa.Function]struct{}),
	}
	check := func(name string) []string {
		pc.pending = nil
		pc.checkFunction(callSite{kind: siteEntry}, nil, pkg.Func(name), &lockFunctionFacts{}, nil, false)
		var got []string
		for _, r := range pc.pending {
			got = append(got, r.msg)
		}
		return got
	}

	got := check("many")
	if want := fmt.Sprintf("checklocks gave up on this function: more than %d block walks", maxFunctionWalks); len(got) != 1 || got[0] != want {
		t.Errorf("function over the budget reported %q, want only %q", got, want)
	}
	if pc.walks > maxFunctionWalks+3 {
		t.Errorf("the walk went on to %d block walks past a budget of %d", pc.walks, maxFunctionWalks)
	}

	got = check("few")
	if !slices.ContainsFunc(got, func(m string) bool { return strings.HasPrefix(m, "must hold m") }) || slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, "gave up") }) {
		t.Errorf("function under the budget reported %q, want its missing lock and no give-up", got)
	}
	if pc.walks > maxFunctionWalks {
		t.Errorf("the second function walked %d blocks, over a budget of %d it should not reach", pc.walks, maxFunctionWalks)
	}

	maxFunctionWalks = 40000
	got = check("diamonds")
	if slices.ContainsFunc(got, func(m string) bool { return strings.Contains(m, "gave up") }) {
		t.Errorf("16 diamonds reassigning a lock-relevant local reported %q, want no give-up", got)
	}
	if pc.walks > 400 {
		t.Errorf("16 diamonds walked %d blocks, want at most 400", pc.walks)
	}
	t.Logf("diamonds walks: %d", pc.walks)
}
