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
	if pc.fnHints != nil {
		t.Error("fnHints not cleared")
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

// hintsFixture is a passContext set up for contained to recover in, with recoverPanics on.
func hintsFixture(t *testing.T) (*passContext, *ssa.Function) {
	t.Helper()
	old := recoverPanics
	recoverPanics = true
	t.Cleanup(func() { recoverPanics = old })

	pkg := buildTestPackage(t, "hints", "package hints\n\nfunc f() {}\n")
	pass := &analysis.Pass{Fset: pkg.Prog.Fset, Report: func(analysis.Diagnostic) {}}
	return &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		functions:    make(map[*ssa.Function]struct{}),
	}, pkg.Func("f")
}

// heldBy returns a lock state holding one lock named after its object.
func heldBy(lock types.Object) (*lockState, map[pathKey]struct{}) {
	p := newLockPath(pathRootValue, nil, nil, lock.Name())
	ls := newLockState()
	ls.lockedMutexes[p.Key()] = lockInfo{exclusive: true, object: lock, path: p}
	return ls, map[pathKey]struct{}{p.Key(): {}}
}

// TestContainedHints pins that a function that panics leaves no access counted.
func TestContainedHints(t *testing.T) {
	pc, fn := hintsFixture(t)
	pkg := types.NewPackage("p", "p")
	a := types.NewVar(token.NoPos, pkg, "a", types.Typ[types.Int])
	b := types.NewVar(token.NoPos, pkg, "b", types.Typ[types.Int])
	lock1 := types.NewVar(token.NoPos, pkg, "mu1", types.Typ[types.Int])
	lock2 := types.NewVar(token.NoPos, pkg, "mu2", types.Typ[types.Int])
	s1, held1 := heldBy(lock1)
	s2, held2 := heldBy(lock2)
	inst := fakeInst{}

	pc.contained(fn, func() { pc.recordObservation(inst, nil, a, s1, held1) })
	if pc.fnHints != nil {
		t.Error("fnHints set after check 1")
	}
	pc.contained(fn, func() {
		pc.recordObservation(inst, nil, b, s1, held1)
		pc.recordObservation(inst, nil, a, s2, held2)
		panic("boom")
	})
	if pc.fnHints != nil {
		t.Error("fnHints set after check 2")
	}
	if oo := pc.observations[a]; oo == nil || oo.total != 1 {
		t.Errorf("after the panic: a = %+v, want total 1", oo)
	}
	if _, ok := pc.observations[b]; ok {
		t.Error("after the panic: b has an entry")
	}
	pc.contained(fn, func() { pc.recordObservation(inst, nil, a, s2, held2) })
	if pc.fnHints != nil {
		t.Error("fnHints set after check 3")
	}
	if oo := pc.observations[a]; oo == nil || oo.total != 2 || oo.counts[lock2] != 1 {
		t.Errorf("after check 3: a = %+v, want total 2 and one count for mu2", oo)
	}
}

// TestContainedHintsDedupe pins that a site counted by one function is not counted by the next.
func TestContainedHintsDedupe(t *testing.T) {
	pc, fn := hintsFixture(t)
	pkg := types.NewPackage("p", "p")
	a := types.NewVar(token.NoPos, pkg, "a", types.Typ[types.Int])
	lock := types.NewVar(token.NoPos, pkg, "mu", types.Typ[types.Int])
	ls, held := heldBy(lock)
	inst := fakeInst{}

	pc.contained(fn, func() {
		pc.recordObservation(inst, nil, a, ls, held)
		pc.recordObservation(inst, nil, a, ls, held)
	})
	if oo := pc.observations[a]; oo.total != 1 || oo.counts[lock] != 1 {
		t.Errorf("after call 1: total %d, count %d; want 1, 1", oo.total, oo.counts[lock])
	}
	pc.contained(fn, func() { pc.recordObservation(inst, nil, a, ls, held) })
	if oo := pc.observations[a]; oo.total != 1 || oo.counts[lock] != 1 {
		t.Errorf("after call 2: total %d, count %d; want 1, 1", oo.total, oo.counts[lock])
	}
}

// engineFixture is a passContext with one record at the only instruction of f, as the replay leaves it.
func engineFixture(t *testing.T) (*passContext, *ssa.Function, ssa.Instruction) {
	t.Helper()
	pc, fn := hintsFixture(t)
	inst := fn.Blocks[0].Instrs[0]
	lock := types.NewVar(token.NoPos, types.NewPackage("p", "p"), "mu", types.Typ[types.Int])
	ls, _ := heldBy(lock)
	pc.engine.add(inst, ls.lockedMutexes, nil)
	pc.engine.setEntry(fn, ls.lockedMutexes)
	return pc, fn, inst
}

// TestContainedEngineRecords pins that a function that panics leaves no engine record.
func TestContainedEngineRecords(t *testing.T) {
	pc, fn, inst := engineFixture(t)
	pc.contained(fn, func() {})
	if _, ok := pc.engine.heldAt(inst); !ok {
		t.Error("completed function: no record at the site")
	}
	if _, ok := pc.engine.entryOf(fn); !ok {
		t.Error("completed function: no entry record")
	}

	pc, fn, inst = engineFixture(t)
	pc.contained(fn, func() { panic("boom") })
	if _, ok := pc.engine.heldAt(inst); ok {
		t.Error("panicked function: record at the site")
	}
	if _, ok := pc.engine.entryOf(fn); ok {
		t.Error("panicked function: entry record")
	}
}
