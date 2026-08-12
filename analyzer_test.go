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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// corpus is the directory holding the packages the analyzer is run over.
const corpus = "test"

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
}{
	{
		name: "checklocks",
		// -checklocks.wrappers=false suppresses diagnostics that have
		// no source position. Those arise from synthetic wrapper
		// functions, cannot be annotated, and are excluded by gVisor's
		// own nogo configuration for the same reason. The flag
		// suppresses only position-less reports; it disables no
		// analysis.
		args: []string{"-lockstringer=false", "-checklocks.wrappers=false", "./test", "./test/crosspkg"},
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
		// Annotations on function literals.
		name: "closure",
		args: []string{"-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.wrappers=false", "./test/closure"},
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
		// The hierarchical direction check is off by default, so its
		// corpus is the one place it is turned on.
		name: "lockhier",
		args: []string{"-checklocks=false", "-lockstringer=false", "-lockblocking=false", "-lockorder.hierarchy=true", "./test/lockhier"},
	},
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
			args := append([]string{"vet", "-vettool=" + bin}, tc.args...)
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
