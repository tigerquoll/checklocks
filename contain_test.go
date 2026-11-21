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
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// TestContainedPanic pins that a panic fails only the function being analyzed.
func TestContainedPanic(t *testing.T) {
	if recoverPanics != (replayRuns == 1) {
		t.Fatalf("recoverPanics = %v with replayRuns = %d", recoverPanics, replayRuns)
	}
	defer func(old bool) { recoverPanics = old }(recoverPanics)

	pkg := buildTestPackage(t, "contain", `package contain

func outer() {
	f := func() {
		g := func() {}
		g()
	}
	f()
}

func other() {}
`)
	fn := pkg.Func("outer")
	closure := fn.AnonFuncs[0]
	nested := closure.AnonFuncs[0]
	fn2 := pkg.Func("other")

	var got []string
	pass := &analysis.Pass{
		Fset: pkg.Prog.Fset,
		Report: func(d analysis.Diagnostic) {
			got = append(got, fmt.Sprintf("%v: %s", d.Pos, d.Message))
		},
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		collect:      true,
		functions:    make(map[*ssa.Function]struct{}),
	}
	pc.addExemption(fn.Pos())
	pc.addFailures(fn.Pos(), "")
	pc.maybeFail(token.Pos(1), "held")

	recoverPanics = true
	pc.records = []*blockRecord{{}}
	pc.current = &blockRecord{}
	pc.emit = emitWalk
	pc.contained(fn, func() { panic("boom") })

	want := []string{fmt.Sprintf("%v: analysis of %s failed with an internal error; its diagnostics may be incomplete", fn.Pos(), fn)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reported\n%q\nwant\n%q", got, want)
	}
	if !pc.panicked {
		t.Error("panicked not set")
	}
	if pc.records != nil || pc.current != nil || pc.emit != emitDirect {
		t.Errorf("walk state not reset: %v %v %v", pc.records, pc.current, pc.emit)
	}
	for _, f := range []*ssa.Function{fn, closure, nested} {
		if _, ok := pc.functions[f]; !ok {
			t.Errorf("%s not marked checked", f)
		}
	}
	if len(pc.pending) != 1 {
		t.Errorf("pending = %v, want the seeded entry", pc.pending)
	}

	got = nil
	called := false
	pc.contained(fn2, func() { called = true })
	if !called || len(got) != 0 {
		t.Errorf("second check: called = %v, reported %q", called, got)
	}

	recoverPanics = false
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate with recovery off")
			}
		}()
		pc.contained(fn2, func() { panic("boom") })
	}()
}
