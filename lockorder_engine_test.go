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
	"go/types"
	"maps"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// TestLabelsUnion pins that two acquires of different classes gaining one key give both classes, in either order.
func TestLabelsUnion(t *testing.T) {
	const src = `package u

type A struct{}

func (a *A) Lock() {}

type B struct{}

func (b *B) Lock() {}

func ab(a *A, b *B) {
	a.Lock()
	b.Lock()
}

func ba(a *A, b *B) {
	b.Lock()
	a.Lock()
}
`
	pkg := buildTestPackage(t, "example.com/u", src)
	classes := map[string]string{"A": "ClassA", "B": "ClassB"}
	pass := &analysis.Pass{
		Fset: pkg.Prog.Fset,
		Pkg:  pkg.Pkg,
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			cf, ok := fact.(*classFact)
			if ok && classes[obj.Name()] != "" {
				cf.Class = classes[obj.Name()]
				return true
			}
			return false
		},
	}
	shared := newLockPath(pathRootGlobal, nil, nil, "mu")
	for _, name := range []string{"ab", "ba"} {
		fn := pkg.Func(name)
		view := &engineView{}
		for _, b := range fn.Blocks {
			for _, inst := range b.Instrs {
				if call, ok := inst.(*ssa.Call); ok {
					view.addOpGain(call, shared)
				}
			}
		}
		pass.ResultOf = map[*analysis.Analyzer]any{Analyzer: &ownerResult{engine: view}}
		pc := &lockOrderContext{pass: pass}
		got := slices.Sorted(maps.Keys(pc.labels(fn)[shared.Key()]))
		if want := []string{"ClassA", "ClassB"}; !slices.Equal(got, want) {
			t.Errorf("%s: labels = %q, want %q", name, got, want)
		}
	}
}
