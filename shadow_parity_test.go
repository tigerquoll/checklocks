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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

const shadowPkg = "github.com/tigerquoll/checklocks/test/shadow"

var shadowRun struct {
	once     sync.Once
	shadow   map[string]string
	decision []string
}

// shadowLogs vets the shadow corpus once with both logs on and returns what they wrote.
func shadowLogs(t *testing.T) (shadow map[string]string, decision []string) {
	t.Helper()
	shadowRun.once.Do(func() {
		bin := filepath.Join(t.TempDir(), "checklocks")
		if out, err := exec.Command("go", "build", "-tags=checklocks_parity", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
			t.Fatalf("building the vettool failed: %v\n%s", err, out)
		}
		shadowDir, decisionDir := t.TempDir(), t.TempDir()
		cmd := exec.Command("go", "vet", "-vettool="+bin, "./test/shadow")
		cmd.Env = append(os.Environ(), "GOCACHE="+t.TempDir(), "CHECKLOCKS_SHADOW_LOG="+shadowDir, "CHECKLOCKS_DECISION_LOG="+decisionDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("vet failed: %v\n%s", err, out)
		}
		shadowRun.shadow = make(map[string]string)
		entries, err := os.ReadDir(shadowDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(shadowDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			shadowRun.shadow[e.Name()] = string(b)
		}
		entries, err = os.ReadDir(decisionDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			shadowRun.decision = append(shadowRun.decision, e.Name())
		}
	})
	if shadowRun.shadow == nil {
		t.Fatal("the shared vet run failed")
	}
	return shadowRun.shadow, shadowRun.decision
}

// shadowCorpus returns the shadow log of the corpus package and the first and last line of a function in its source.
func shadowCorpus(t *testing.T, fn string) (lines []string, first, last int) {
	t.Helper()
	logs, _ := shadowLogs(t)
	log, ok := logs[strings.ReplaceAll(shadowPkg, "/", "%2F")+".log"]
	if !ok {
		t.Fatalf("no shadow log for the corpus in %v", logs)
	}
	src, err := os.ReadFile("test/shadow/shadow.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(src), "\n") {
		switch {
		case strings.HasPrefix(l, "func "+fn+"("):
			first = i + 1
		case first > 0 && last == 0 && l == "}":
			last = i + 1
		}
	}
	if last == 0 {
		t.Fatalf("function %s not found", fn)
	}
	return strings.Split(strings.TrimSuffix(log, "\n"), "\n"), first, last
}

// linesIn returns the D and X lines positioned inside lines first to last of the corpus.
func linesIn(log []string, first, last int) []string {
	var out []string
	for _, l := range log {
		f := strings.Split(l, "\t")
		if f[0] != "D" && f[0] != "X" {
			continue
		}
		for n := first; n <= last; n++ {
			if strings.Contains(f[2], fmt.Sprintf("shadow.go:%d:", n)) {
				out = append(out, l)
			}
		}
	}
	return out
}

func TestShadowAgree(t *testing.T) {
	for _, fn := range []string{"straight", "twoStates", "commaOK"} {
		log, first, last := shadowCorpus(t, fn)
		if got := linesIn(log, first, last); len(got) > 0 {
			t.Errorf("%s: want no difference at its calls, got %q", fn, got)
		}
		n := log[len(log)-1]
		if !strings.HasPrefix(n, "N\t") || strings.Contains(n, "compared=0\t") {
			t.Errorf("%s: want an N line with compared > 0, got %q", fn, n)
		}
	}
	// The sites without a record: the literal litEscapes hands away, and Guard's primitive bodies.
	log, _, _ := shadowCorpus(t, "straight")
	if n := log[len(log)-1]; !strings.Contains(n, "\tnoengine=5\t") || !strings.Contains(n, "\tunrecorded=0") {
		t.Errorf("want noengine=5 so that every other site is counted, and unrecorded=0, got %q", n)
	}

	// A literal called directly has records; lockorder holds nothing there.
	log, first, last := shadowCorpus(t, "inLiteral")
	got := linesIn(log, first, last)
	if len(got) != 1 {
		t.Fatalf("want one line in inLiteral, got %q", got)
	}
	if f := strings.Split(got[0], "\t"); f[0] != "D" || f[3] != "inLiteral$1" || f[5] != "Lonly=" || f[6] != "Eonly=G" {
		t.Errorf("want a D line with Eonly G in the literal, got %q", got[0])
	}

	// A literal handed away has no records, and the line says why.
	log, first, last = shadowCorpus(t, "litEscapes")
	got = linesIn(log, first, last)
	if len(got) != 2 {
		t.Fatalf("want two lines in litEscapes, got %q", got)
	}
	for _, l := range got {
		if f := strings.Split(l, "\t"); f[0] != "X" || f[3] != "litEscapes$1" || f[4] != "L=G" || f[5] != "not called directly" {
			t.Errorf("want an X line in the literal that is not called directly, got %q", l)
		}
	}
}

func TestShadowEngineOnly(t *testing.T) {
	log, first, last := shadowCorpus(t, "entryOnly")
	got := linesIn(log, first, last)
	if len(got) != 1 {
		t.Fatalf("want one line in entryOnly, got %q", got)
	}
	f := strings.Split(got[0], "\t")
	if f[0] != "D" || f[4] != "call" || f[5] != "Lonly=" || f[6] != "Eonly=G" || len(f) != 8 || f[7] != "G=entry/T" {
		t.Errorf("want a D call line with Eonly G from entry by the type rule, got %q", got[0])
	}
}

func TestShadowView(t *testing.T) {
	shadow, decision := shadowLogs(t)
	var names []string
	for name, log := range shadow {
		names = append(names, name)
		n := 0
		for _, l := range strings.Split(log, "\n") {
			if strings.HasPrefix(l, "N\t") {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: want one N line, got %d", name, n)
		}
		if strings.Contains(log, " noview") || strings.HasPrefix(log, "P\t") || strings.Contains(log, "\nP\t") {
			t.Errorf("%s: want no noview and no P line:\n%s", name, log)
		}
	}
	slices.Sort(names)
	slices.Sort(decision)
	if !slices.Equal(names, decision) {
		t.Errorf("shadow logs %q, decision logs %q", names, decision)
	}
}

// TestShadowUnrecorded pins that a site with a class held and no engine record, in a function the engine walked, is counted.
func TestShadowUnrecorded(t *testing.T) {
	old := shadowLogDir
	shadowLogDir = t.TempDir()
	t.Cleanup(func() { shadowLogDir = old })

	pkg := buildTestPackage(t, "unrec", "package unrec\n\nfunc f() {}\n")
	fn := pkg.Func("f")
	inst := fn.Blocks[0].Instrs[0]
	pass := &analysis.Pass{Fset: pkg.Prog.Fset, Pkg: pkg.Pkg, ResultOf: map[*analysis.Analyzer]any{}}
	count := func(withEntry bool) int {
		view := &engineView{}
		if withEntry {
			view.setEntry(fn, nil, nil)
		}
		pass.ResultOf[Analyzer] = &ownerResult{engine: view}
		pc := &lockOrderContext{pass: pass, check: checkOrder}
		pc.shadow.held = map[ssa.Instruction]map[string]struct{}{inst: {"A": {}}}
		pc.shadowCompare()
		return pc.shadow.unrecorded
	}
	if got := count(true); got != 1 {
		t.Errorf("walked function: unrecorded = %d, want 1", got)
	}
	if got := count(false); got != 0 {
		t.Errorf("function with no entry record: unrecorded = %d, want 0", got)
	}
}
