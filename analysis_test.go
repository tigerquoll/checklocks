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
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
)

// TestInferredOrder pins the emission sequence, which per-line corpus expectations cannot see.
func TestInferredOrder(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("inferred.go", -1, 10000)
	lines := make([]int, 500)
	for i := range lines {
		lines[i] = i * 20
	}
	file.SetLines(lines)
	var got []string
	pass := &analysis.Pass{
		Fset:             fset,
		Report:           func(d analysis.Diagnostic) { got = append(got, d.Message) },
		ImportObjectFact: func(types.Object, analysis.Fact) bool { return false },
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		observations: make(map[types.Object]*objectObservations),
	}

	// Added back to front, so only a sort yields the order below.
	pkg := types.NewPackage("p", "p")
	const fields, locks = 3, 3
	var want []string
	for f := fields; f >= 1; f-- {
		field := types.NewVar(file.LineStart(10*f), pkg, fmt.Sprintf("f%d", f), types.Typ[types.Int])
		oo := &objectObservations{counts: make(map[types.Object]int), total: 1}
		for l := locks; l >= 1; l-- {
			lock := types.NewVar(file.LineStart(100+10*f+l), pkg, fmt.Sprintf("m%d%d", f, l), types.Typ[types.Int])
			oo.counts[lock] = 1
		}
		pc.observations[field] = oo
	}
	for f := 1; f <= fields; f++ {
		for l := 1; l <= locks; l++ {
			want = append(want, fmt.Sprintf("may require checklocks annotation for m%d%d, used with lock held 100%% of the time", f, l))
		}
	}

	pc.checkInferred()
	if !slices.Equal(got, want) {
		t.Errorf("reported\n%q\nwant\n%q", got, want)
	}
}
