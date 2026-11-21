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
	"golang.org/x/tools/go/ssa"
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

// TestCollectedReports pins what the collector drops and the order it keeps, which the
// walk order would otherwise decide.
func TestCollectedReports(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("collect.go", -1, 10000)
	lines := make([]int, 10)
	for i := range lines {
		lines[i] = i * 20
	}
	file.SetLines(lines)
	var got []string
	pass := &analysis.Pass{
		Fset: fset,
		Report: func(d analysis.Diagnostic) {
			got = append(got, fmt.Sprintf("%v: %s", fset.Position(d.Pos), d.Message))
		},
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		collect:      true,
	}

	pc.addFailures(file.LineStart(4), "=expected")
	pc.maybeFail(file.LineStart(3), "late")
	pc.maybeFail(file.LineStart(4), "expected")
	pc.maybeFail(file.LineStart(1), "twice")
	pc.maybeFail(file.LineStart(2), "beta")
	pc.maybeFail(file.LineStart(1), "twice")
	pc.maybeFail(file.LineStart(2), "alpha")
	pc.flushReports()

	want := []string{
		"collect.go:1:1: twice",
		"collect.go:2:1: alpha",
		"collect.go:2:1: beta",
		"collect.go:3:1: late",
	}
	if !slices.Equal(got, want) {
		t.Errorf("reported\n%q\nwant\n%q", got, want)
	}

	// The expectation the suppressed report met is consumed, so nothing is missing.
	pc.checkFailures()
	if !slices.Equal(got, want) {
		t.Errorf("after checkFailures reported\n%q\nwant\n%q", got, want)
	}
}

// fakeInst stands in for the access an observation key names.
type fakeInst struct{}

func (fakeInst) Pos() token.Pos                { return token.NoPos }
func (fakeInst) Referrers() *[]ssa.Instruction { return nil }

// TestObservationDedupe pins which observations are counted, as a hint's percentage
// cannot say how often a site was walked.
func TestObservationDedupe(t *testing.T) {
	pkg := types.NewPackage("p", "p")
	field := types.NewVar(token.NoPos, pkg, "value", types.Typ[types.Int])
	other := types.NewVar(token.NoPos, pkg, "second", types.Typ[types.Int])
	lock := types.NewVar(token.NoPos, pkg, "mu", types.Typ[types.Int])

	one := newLockState()
	one.lockedMutexes["a.mu"] = lockInfo{exclusive: true, object: lock}
	oneHeld := map[string]struct{}{"a.mu": {}}
	two := newLockState()
	two.lockedMutexes["a.mu"] = lockInfo{exclusive: true, object: lock}
	two.lockedMutexes["b.mu"] = lockInfo{exclusive: true, object: lock}
	twoHeld := map[string]struct{}{"a.mu": {}, "b.mu": {}}
	inst := fakeInst{}

	pc := &passContext{}
	pc.recordObservation(inst, nil, field, one, oneHeld)
	pc.recordObservation(inst, nil, field, one, oneHeld)
	if oo := pc.observations[field]; oo.total != 1 || oo.counts[lock] != 1 {
		t.Errorf("after a repeat: total %d, count %d; want 1, 1", oo.total, oo.counts[lock])
	}

	pc.recordObservation(inst, nil, field, two, twoHeld)
	pc.recordObservation(inst, nil, other, one, oneHeld)
	if oo := pc.observations[field]; oo.total != 2 || oo.counts[lock] != 2 {
		t.Errorf("with a second held set: total %d, count %d; want 2, 2", oo.total, oo.counts[lock])
	}
	if oo := pc.observations[other]; oo.total != 1 || oo.counts[lock] != 1 {
		t.Errorf("for a second object: total %d, count %d; want 1, 1", oo.total, oo.counts[lock])
	}
}
