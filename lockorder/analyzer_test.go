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

package lockorder_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// corpus is the directory holding the packages the analyzer is run over.
const corpus = "../test/lockorder"

// readCorpus reads every source file in the corpus and returns the number read.
//
// The analyzer runs in a subprocess, so the corpus is invisible to the test caching
// machinery, which would otherwise report a stale success after the corpus changed. Reading
// the files here registers them as inputs of this test, since the cache tracks the files a
// test opens.
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
		t.Fatalf("reading the corpus: %v", err)
	}
	return count
}

// TestAnalyzer runs the analyzer over the corpus and requires that it produce no output.
//
// The corpus is self-checking. Every case that must be reported carries a "+lockorderfail"
// annotation, and the analyzer reports "missing expected failure" when an annotated line
// produces no diagnostic. So an expectation that stops holding is reported, an expectation
// that was never met is reported, and any diagnostic the corpus does not expect is
// reported. All three appear as output, and any output fails this test.
//
// The analyzer is exercised as a vettool rather than through analysistest because the
// corpus is a set of ordinary packages that must be analyzed with facts flowing between
// them, which is how the summaries reach a call site in another package.
func TestAnalyzer(t *testing.T) {
	if n := readCorpus(t); n == 0 {
		t.Fatalf("no source files found in %s", corpus)
	}

	bin := filepath.Join(t.TempDir(), "lockorder")
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/lockorder").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	// N.B. go vet exits non-zero whenever diagnostics are reported, so the error is not
	// itself interesting; the output is.
	out, err := exec.Command("go", "vet", "-vettool="+bin, "../test/lockorder/...").CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("analyzer reported unexpected output (err: %v):\n%s", err, s)
	}
}
