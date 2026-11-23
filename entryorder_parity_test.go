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

//go:build checklocks_parity

package checklocks

import (
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// The entry record comes before the records of a literal walked inside the entry block, in every order.
func TestEntryRecordFirst(t *testing.T) {
	saved := walkOrder
	t.Cleanup(func() { walkOrder = saved })
	pkg := buildTestPackage(t, "entry", `package p

func f(c func() bool) {
	func() { _ = c() }()
	if c() {
		_ = c()
	}
}
`)
	for _, order := range []string{"", "reverse", "7"} {
		walkOrder = order
		pass := &analysis.Pass{
			Fset:             pkg.Prog.Fset,
			Report:           func(analysis.Diagnostic) {},
			ImportObjectFact: func(types.Object, analysis.Fact) bool { return false },
		}
		pc := &passContext{
			expectations: newExpectations(pass, checkLocksAnnotations, true),
			pass:         pass,
			collect:      true,
			functions:    make(map[*ssa.Function]struct{}),
			emit:         emitDirect,
		}
		fn := pkg.Func("f")
		wc := &walkContext{fn: fn, lff: &lockFunctionFacts{}, fresh: pc.computeFresh(fn, nil), context: "f", returnStates: map[string]string{}}
		entry := newLockState()
		pc.solve(wc, fn.Blocks[0], entry)
		if len(pc.records) == 0 {
			t.Fatalf("order %q: the solve made no records", order)
		}
		if first := pc.records[0]; first.wc != wc || first.block != fn.Blocks[0] || first.parent != entry {
			t.Errorf("order %q: first record is block %d of %s, want the entry block on the entry state", order, first.block.Index, first.wc.fn)
		}
		if !slices.ContainsFunc(pc.records, func(r *blockRecord) bool { return r.wc.fn != fn }) {
			t.Errorf("order %q: no record of the literal in the entry block", order)
		}
	}
}
