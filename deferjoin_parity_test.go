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
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// TestParityDefersCompareAfter checks the replay's defer-stack checks tell entries apart by after, and catch a repeated entry.
func TestParityDefersCompareAfter(t *testing.T) {
	pkg := buildTestPackage(t, "join", joinSource)
	sites := deferSites(pkg.Func("gaps"))
	low, rel := sites[0], sites[1]
	plain := deferStack{[]*ssa.Defer{rel}, []deferEntry{{d: low}}}
	after := deferStack{[]*ssa.Defer{rel}, []deferEntry{{d: low, after: "\x00"}}}
	p := &blockParity{wantIn: &plain, wantOut: &plain}
	if msg := p.deferStacksOff(plain, plain); msg != "" {
		t.Errorf("equal stacks are off: %s", msg)
	}
	if p.deferStacksOff(after, plain) == "" || p.deferStacksOff(plain, after) == "" {
		t.Error("stacks that differ in after compare equal")
	}
	twice := deferStack{plain.defers, []deferEntry{{d: low}, {d: low}}}
	if (&blockParity{}).deferStacksOff(plain, twice) == "" {
		t.Error("an entry on the stack twice is not caught")
	}
}

// TestRunDefersCountsMay checks runDefers feeds the walk statistics one count per may entry it runs.
func TestRunDefersCountsMay(t *testing.T) {
	var src strings.Builder
	src.WriteString("package p\n\nfunc other() {}\nfunc noop() {}\n\nfunc over() {\n")
	src.WriteString("\tdefer noop()\n")
	for range maxEntries + 2 {
		src.WriteString("\tdefer other()\n")
	}
	src.WriteString("}\n")
	pkg := buildTestPackage(t, "cap", src.String())
	pc := deferPC(pkg.Prog.Fset)
	fn := pkg.Func("over")
	ls := newLockState()
	// other has no facts, so the test forces the pushes relevant.
	sites := deferSites(fn)
	for _, d := range sites[1:] {
		ls.pushDefer(sites[0], false)
		ls.pushDefer(d, true)
	}
	ls.pushDefer(sites[0], false)
	if n := len(ls.noops); n != 1 || !ls.noops[0].may {
		t.Fatalf("the fixture holds %d no-op entries, want one may entry", n)
	}
	wc := &walkContext{fn: fn, lff: &lockFunctionFacts{Ignore: true}}
	stack := ls.deferStack
	pc.runDefers(wc, ls)
	if pc.stats.deferMay != 1 {
		t.Errorf("deferMay is %d after one may entry ran, want 1", pc.stats.deferMay)
	}
	pc.emit = emitReplay
	ls.deferStack = stack
	pc.runDefers(wc, ls)
	if pc.stats.deferMay != 1 {
		t.Errorf("deferMay is %d after a replay, want it unchanged", pc.stats.deferMay)
	}
}
