// Copyright 2026 The gVisor Authors.
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

package checklocks_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// corpus is the directory holding the packages the analyzer is run over.
const corpus = "test"

// expandToFiles replaces the package patterns in a command line with the
// source files of those packages, which is what turns a package run into a
// file list run.
func expandToFiles(t *testing.T, args []string) []string {
	t.Helper()
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			out = append(out, arg)
			continue
		}
		listed, err := exec.Command("go", "list", "-f",
			"{{$dir := .Dir}}{{range .GoFiles}}{{$dir}}/{{.}}\n{{end}}", arg).Output()
		if err != nil {
			t.Fatalf("listing the files of %s failed: %v", arg, err)
		}
		for _, file := range strings.Fields(string(listed)) {
			out = append(out, file)
		}
	}
	return out
}

// readCorpus reads every source file in the corpus and returns the number
// read.
//
// The analyzer runs in a subprocess, so the corpus is invisible to the test
// caching machinery, which would otherwise report a stale success after the
// corpus changed. Reading the files here registers them as inputs of this
// test, since the cache tracks the files a test opens.
func readCorpus(t *testing.T) int {
	t.Helper()
	count := 0
	if err := filepath.WalkDir(corpus, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if _, err := os.ReadFile(path); err != nil {
			return err
		}
		count++
		return nil
	}); err != nil {
		t.Fatalf("reading the corpus failed: %v", err)
	}
	return count
}

// analyzerCase is one analyzer run over its own corpus.
//
// Each analyzer has its own corpus, and each is run over it with the others
// disabled. The corpora hold deliberate violations, and most are violations in
// more than one analyzer's terms; an expectation can only be stated once per
// line, so they cannot be stated for two analyzers at once.
//
// Note that a disabled analyzer still runs when another requires it, so
// lockstringer receives checklocks' guard facts here even with checklocks
// silenced.
var analyzerCases = []struct {
	name string
	args []string

	// fileList runs the case over the FILES of the named packages rather
	// than over the packages. That is how a Makefile drives a vet tool
	// when it wants to choose which files are checked, and it is a
	// different unit as far as go vet is concerned: the package under
	// analysis is "command-line-arguments" and it belongs to no module.
	// An analysis that consults either must behave the same way in both.
	fileList bool
}{
	{
		name: "checklocks",
		// -checklocks.wrappers=false suppresses diagnostics that have
		// no source position, such as those about the call inside a bound-method
		// wrapper. gVisor's own nogo configuration excludes them too. The flag
		// disables no analysis.
		args: []string{"-lockstringer=false", "-checklocks.wrappers=false", "./test", "./test/crosspkg", "./test/order", "./test/polarity"},
	},
	{
		name: "lockstringer",
		args: []string{"-checklocks=false", "./test/lockstringer"},
	},
	{
		// The corpus declares its own lock taxonomy, so only this
		// analyzer has anything to say about it. The forwarders in it
		// carry checklocks ignores, as the wrappers in a real code base
		// do, so the guard analysis stays quiet either way.
		name: "lockorder",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockblocking=false", "./test/lockorder"},
	},
	{
		// The corpus declares classes but no order: this analysis asks
		// only whether one is held, so an order would say nothing about
		// it. lockorder is disabled and still runs, which is what
		// builds the summaries this reports against.
		name: "lockblocking",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockorder=false", "./test/lockblocking"},
	},
	{
		// The same corpus over a file list, which is one package at a
		// time: go vet takes files from a single directory. What may
		// cross a package boundary is decided per callee, and a declared
		// wait must cross it whichever way the analyser was driven;
		// before that was so, this run reported the cross-package
		// expectations as unmet while the run above was green.
		name:     "lockblocking-filelist",
		args:     []string{"-checklocks=false", "-lockstringer=false", "-lockorder=false", "./test/lockblocking"},
		fileList: true,
	},
	{
		// A file list belongs to no module, so this corpus's dependency is out of scope.
		name:     "lockblocking-scope",
		args:     []string{"-checklocks=false", "-lockstringer=false", "-lockorder=false", "./test/lockblockingscope"},
		fileList: true,
	},
	{
		// Grouped blocking reports: a group names a path only when all its sites share it.
		name: "lockblocking-grouped",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockorder=false", "-lockblocking.group=true", "./test/lockblockinggroup"},
	},
	{
		// Derived exclusions. The corpus carries no exclusion
		// annotation; every one it relies on is derived.
		name: "synthexclude",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "./test/synthexclude"},
	},
	{
		// Guards naming a value recovered by a type assertion.
		name: "typeassert",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "./test/typeassert"},
	},
	{
		// Annotations on function literals.
		name: "closure",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "./test/closure"},
	},
	{
		// A guard declared on a structure. The corpus is about which
		// fields the expansion reaches, so the inferred suggestions are
		// off: they would comment on the fields that are deliberately
		// not covered.
		name: "blockguards",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "-checklocks.inferred=false", "./test/blockguards"},
	},
	{
		// A lock released and taken again in one function. The analysis
		// is off unless asked for, so the corpus asks.
		name: "lockgap",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-lockgap.enable=true", "./test/lockgap"},
	},
	{
		// Objects under construction. The other analyses have nothing
		// to say about the corpus and are silenced so that the
		// expectations can be stated for this one.
		name: "fresh",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "-checklocks.inferred=false", "./test/fresh"},
	},
	{
		// Declared lock primitives. The wrappers carry no ignore, which
		// is what restores the call site checks.
		name: "locktype",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "./test/locktype"},
	},
	{
		// Two analyses at once, which no other case does: the point of
		// this corpus is a line that is a violation in two analyses'
		// terms and carries an annotation for each.
		name: "multiannotation",
		args: []string{"-lockstringer=false", "-lockorder=false", "-checklocks.wrappers=false", "-checklocks.inferred=false", "./test/multiannotation"},
	},
	{
		// Grouped reports. Run with grouping on, so that the
		// suppression rules are exercised in the mode that changes how
		// they are emitted.
		name: "grouping",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockblocking=false", "-lockorder.group=true", "./test/grouping"},
	},
	{
		// Wrappers on; the inline literal's implicit return must not report.
		name: "inlinereturn",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "./test/inlinereturn"},
	},
	{
		// Wrappers on: no diagnostic here may be positionless.
		name: "positionless",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.inferred=false", "./test/positionless"},
	},
	{
		// The hierarchical direction check is off by default, so its
		// corpus is the one place it is turned on.
		name: "lockhier",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockblocking=false", "-lockorder.hierarchy=true", "./test/lockhier"},
	},
}

// allEnabledArgs derives the command line of a case's all-analyzers run. The
// flags that switch an analyzer off go, lockgap's opt-in comes on, and the mode
// flags stay: those are what the corpus is written against.
func allEnabledArgs(args []string) []string {
	const lockgap = "-lockgap.enable=true"
	out := make([]string, 0, len(args)+1)
	out = append(out, lockgap)
	for _, arg := range args {
		// An analyzer switch carries no dot in its name, which is what
		// tells it apart from a mode flag like -checklocks.wrappers.
		if arg == lockgap || (strings.HasSuffix(arg, "=false") && !strings.Contains(arg, ".")) {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// TestAnalyzer runs each analyzer over its corpus and requires that it produce
// no output at all.
//
// The corpora are self-checking. Every case that must be reported carries a
// "+<analyzer>fail" annotation, and the analyzer reports "missing expected
// failure" when an annotated line produces no diagnostic. So an expectation
// that stops holding is reported, an expectation that was never met is
// reported, and any diagnostic the corpus does not expect is reported. All
// three appear as output, and any output fails this test.
//
// The analyzers are exercised as a vettool rather than through analysistest,
// because the corpora are ordinary packages that must be analyzed with facts
// flowing between them: test/crosspkg exports facts that test consumes, and
// several cases exist only to check that behaviour.
func TestAnalyzer(t *testing.T) {
	if n := readCorpus(t); n == 0 {
		t.Fatalf("no source files found in %s", corpus)
	}

	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	for _, tc := range analyzerCases {
		t.Run(tc.name, func(t *testing.T) {
			tcArgs := tc.args
			if tc.fileList {
				tcArgs = expandToFiles(t, tcArgs)
			}
			args := append([]string{"vet", "-vettool=" + bin}, tcArgs...)
			// N.B. go vet exits non-zero whenever diagnostics are
			// reported, so the error is not itself interesting;
			// the output is.
			out, err := exec.Command("go", args...).CombinedOutput()
			if s := strings.TrimSpace(string(out)); s != "" {
				t.Errorf("analyzer reported unexpected output (err: %v):\n%s", err, s)
			}
		})
	}
}

// TestAnalyzerAllEnabled runs each case with every analyzer on: a diagnostic one
// analyzer raises on another's corpus must be marked there.
func TestAnalyzerAllEnabled(t *testing.T) {
	if n := readCorpus(t); n == 0 {
		t.Fatalf("no source files found in %s", corpus)
	}

	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	for _, tc := range analyzerCases {
		t.Run(tc.name, func(t *testing.T) {
			// A file list case runs over its files here too.
			tcArgs := allEnabledArgs(tc.args)
			if tc.fileList {
				tcArgs = expandToFiles(t, tcArgs)
			}
			args := append([]string{"vet", "-vettool=" + bin}, tcArgs...)
			out, err := exec.Command("go", args...).CombinedOutput()
			if s := strings.TrimSpace(string(out)); s != "" {
				t.Errorf("analyzer reported unexpected output (err: %v):\n%s", err, s)
			}
		})
	}
}

// TestAnalyzerStandalone runs each case with every analyzer on through the
// binary itself, where each analyzer keeps its own fact store, as go vet does not.
func TestAnalyzerStandalone(t *testing.T) {
	if n := readCorpus(t); n == 0 {
		t.Fatalf("no source files found in %s", corpus)
	}

	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	run := func(t *testing.T, name string, extra ...string) {
		for _, tc := range analyzerCases {
			if tc.name != name {
				continue
			}
			tcArgs := allEnabledArgs(tc.args)
			if tc.fileList {
				tcArgs = expandToFiles(t, tcArgs)
			}
			out, err := exec.Command(bin, append(extra, tcArgs...)...).CombinedOutput()
			s := strings.TrimSpace(string(out))
			wantCode := 0
			if name == "checklocks" {
				// Standalone loads crosspkg from source, so the unexported guard barMu resolves here; vet cannot.
				wantCode = 3
				var kept []string
				for _, line := range strings.Split(s, "\n") {
					if !strings.Contains(line, "test/globals.go:255:11") {
						kept = append(kept, line)
					}
				}
				if n := len(strings.Split(s, "\n")) - len(kept); n != 1 {
					t.Errorf("want exactly one barMu line, got %d:\n%s", n, s)
				}
				s = strings.TrimSpace(strings.Join(kept, "\n"))
			}
			code := 0
			if err != nil {
				code = -1
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					code = ee.ExitCode()
				}
			}
			if s != "" || code != wantCode {
				t.Errorf("analyzer reported unexpected output or exit code (want %d, got %d, err: %v):\n%s", wantCode, code, err, s)
			}
			return
		}
		t.Fatalf("no case named %s", name)
	}

	for _, tc := range analyzerCases {
		t.Run(tc.name, func(t *testing.T) { run(t, tc.name) })
	}
	t.Run("lockblocking-scope-debug", func(t *testing.T) { run(t, "lockblocking-scope", "-debug=s") })
}

// TestLockOrderCycle requires the declared-cycle report to be the only output,
// with every analyzer on, standalone and under vet.
func TestLockOrderCycle(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module cyc\n\ngo 1.21\n",
		"cyc.go": "// Package cyc declares a cycle.\n//\n// +lockorder:A < B\n// +lockorder:B < A\npackage cyc\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for name, argv := range map[string][]string{
		"standalone": {bin, "-lockgap.enable=true", "./..."},
		"vet":        {"go", "vet", "-vettool=" + bin, "-lockgap.enable=true", "./..."},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = dir
			out, _ := cmd.CombinedOutput()
			found := false
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				switch {
				case strings.HasPrefix(line, "#"):
				case strings.Contains(line, "lock order declarations contain a cycle"):
					found = true
				default:
					t.Errorf("unexpected output line: %s", line)
				}
			}
			if !found {
				t.Errorf("no cycle report in output:\n%s", out)
			}
		})
	}
}
