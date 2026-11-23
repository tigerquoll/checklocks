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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const lockorderPkg = modulePrefix + "test/lockorder"

// siteDumpOf vets the lockorder corpus under one walk order and returns its site dump.
func siteDumpOf(t *testing.T, bin, order string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "vet", "-vettool="+bin, "-lockstringer=false", "-checklocks.wrappers=false", "./test/lockorder")
	cmd.Env = append(os.Environ(), "GOCACHE="+t.TempDir(), "CHECKLOCKS_SITE_DUMP="+dir, "CHECKLOCKS_WALK_ORDER="+order)
	// Reports differ by order, so the exit status says nothing.
	out, _ := cmd.CombinedOutput()
	files, err := filepath.Glob(filepath.Join(dir, "*.sites"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if filepath.Base(f) == url.PathEscape(lockorderPkg)+".sites" {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
	}
	t.Fatalf("order %q wrote no site dump for the lockorder package; files %v\n%s", order, files, out)
	return ""
}

// TestSiteDump checks the site dump of the lockorder corpus is non-empty, sorted and the same in every walk order.
func TestSiteDump(t *testing.T) {
	// The analyzer runs in a subprocess; reading the corpus registers it as an input of this test's cache entry.
	files := 0
	if err := filepath.WalkDir("test", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		files++
		_, err = os.ReadFile(path)
		return err
	}); err != nil || files == 0 {
		t.Fatalf("reading the corpus failed after %d files: %v", files, err)
	}

	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-tags=checklocks_parity", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	forward := siteDumpOf(t, bin, "")
	lines := strings.Split(strings.TrimSuffix(forward, "\n"), "\n")
	if len(lines) < 10 {
		t.Fatalf("want at least 10 dump lines, got %d", len(lines))
	}
	if !slices.IsSorted(lines) {
		t.Errorf("the dump is not sorted")
	}
	atEntry := slices.ContainsFunc(lines, func(l string) bool {
		f := strings.Split(l, "\t")
		return len(f) == 4 && strings.Contains(f[3], "*")
	})
	if !atEntry {
		t.Errorf("want a site held at entry in the dump")
	}
	if !strings.Contains(forward, "\tentry\t") {
		t.Errorf("want an entry line in the dump")
	}
	if reverse := siteDumpOf(t, bin, "reverse"); reverse != forward {
		t.Errorf("the dump differs between forward and reverse order")
	}
}
