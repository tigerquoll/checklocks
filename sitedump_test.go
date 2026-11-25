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

// takePoolHeld is the held field of the dump lines at the djTakePool defer of each case in test/lockorder/deferjoin.go.
var takePoolHeld = map[string]string{
	"djLoopRelease":   "-",
	"djPinRepeat":     "&(&({param:e}.info).g)*,&(&({param:e}.info).h)",
	"djPinTwoRelease": "&(&({param:e}.info).g)*",
	"djPinAcquire":    "&(&({param:e}.info).g),&(&({param:e}.info).h)*",
	"djPinReleased":   "-",
	"djPinRelock":     "-",
	"djPinVis":        "&(&({param:e}.info).g)*",
	"djPinTwoRel":     "&(&({param:e}.info).h)*",
}

// checkTakePoolHeld checks every dump line at those sites holds exactly the locks of takePoolHeld.
func checkTakePoolHeld(t *testing.T, lines []string) {
	t.Helper()
	src, err := os.ReadFile("test/lockorder/deferjoin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Split(string(src), "\n")
	for fn, want := range takePoolHeld {
		at := slices.IndexFunc(text, func(l string) bool { return strings.HasPrefix(l, "func "+fn+"(") })
		off := slices.IndexFunc(text[max(at, 0):], func(l string) bool { return strings.Contains(l, "defer djTakePool(p)") })
		if at < 0 || off < 0 {
			t.Errorf("%s: no djTakePool defer in the corpus", fn)
			continue
		}
		prefix := fmt.Sprintf("%s\tdeferjoin.go:%d:", fn, at+off+1)
		found := false
		for _, l := range lines {
			if !strings.HasPrefix(l, prefix) {
				continue
			}
			found = true
			if f := strings.Split(l, "\t"); len(f) != 5 || f[3] != want {
				t.Errorf("%s: dump line %q, want the held field %q", fn, l, want)
			}
		}
		if !found {
			t.Errorf("%s: no dump line for its djTakePool defer", fn)
		}
	}
}

// unwindCells are the functions of the corpus whose deferred calls are checked: how many dump lines each has
// before its first deferred call, how many deferred calls, and whether their unwind is inexact.
var unwindCells = map[string]struct {
	before, calls int
	inexact       bool
}{
	"djFailedUnlock":       {1, 3, true},
	"djCleanUnlock":        {1, 2, false},
	"djLiteralUnlock":      {1, 3, true},
	"djForcedPrecondition": {0, 2, true},
}

// checkUnwindAnswers checks every deferred call of unwindCells answers unknown when its unwind is inexact, with
// the failure silenced, and none does when it is exact.
func checkUnwindAnswers(t *testing.T, lines []string) {
	t.Helper()
	count := map[string]int{}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		cell, ok := unwindCells[f[0]]
		if len(f) != 5 || !ok {
			continue
		}
		if count[f[0]]++; count[f[0]] <= cell.before {
			continue
		}
		if got := strings.Contains(f[4], "?"); got != cell.inexact {
			t.Errorf("%s: dump line %q, unknown flag %t, want %t", f[0], l, got, cell.inexact)
		}
		if cell.inexact && f[3] != "-" && !strings.Contains(f[4], "=unknown") {
			t.Errorf("%s: dump line %q, want its held lock answered unknown", f[0], l)
		}
		if !cell.inexact && f[3] != "-" && !strings.Contains(f[4], "=all/x") {
			t.Errorf("%s: dump line %q, want its held lock answered all", f[0], l)
		}
	}
	for fn, cell := range unwindCells {
		if count[fn] != cell.before+cell.calls {
			t.Errorf("%s: %d dump lines, want %d", fn, count[fn], cell.before+cell.calls)
		}
	}
}

// litCells are the functions of test/lockorder/litsites.go and how many dump site lines each has.
var litCells = map[string]int{
	"litTwoStates$1":     1,
	"litArms$1":          2,
	"litGap$1":           4,
	"litEscapeFree$1":    0,
	"litEscapeClosure$1": 0,
	"litNested$1":        0,
	"litNested$1$1":      0,
	"litDeferred$1":      0,
	"litDeferred$2":      1,
	"litInherits$1":      3,
	"litInherits$1$1":    1,
	"litNoopSites$1":     1,
	"litRewalkPanic$1":   0,
	"litRewalkPanic$2":   1,
	"litNestGiveUp$1":    0,
	"litNestGiveUp$1$1":  0,
	"litGaveUp$1":        0,
	"litGaveUp":          5,
}

// checkLitCells checks the dump lines of litCells, the some answer, and the give-up line.
func checkLitCells(t *testing.T, lines []string) {
	t.Helper()
	count := map[string]int{}
	for _, l := range lines {
		f := strings.Split(l, "\t")
		switch {
		case len(f) == 5:
			count[f[0]]++
			if f[0] == "litTwoStates$1" && !strings.HasSuffix(f[4], "=some/x") {
				t.Errorf("dump line %q, want its lock answered some", l)
			}
		case f[0] == "litGaveUp$1" && f[1] == "gaveup":
			count["gaveup"]++
		}
	}
	for fn, want := range litCells {
		if count[fn] != want {
			t.Errorf("%s: %d dump lines, want %d", fn, count[fn], want)
		}
	}
	if count["gaveup"] != 1 {
		t.Errorf("want one give-up line for litGaveUp$1, got %d", count["gaveup"])
	}
}

// twinCells pair a function of test/lockorder that wraps its body in a literal with the same body written at the top level.
var twinCells = [][2]string{{"litArms$1", "djArms"}, {"litGap$1", "litGapTop"}}

// checkTwins checks each pair has the same dump lines but for the position.
func checkTwins(t *testing.T, lines []string) {
	t.Helper()
	body := func(fn string) []string {
		var out []string
		for _, l := range lines {
			if f := strings.Split(l, "\t"); len(f) == 5 && f[0] == fn {
				out = append(out, strings.Join(f[2:], "\t"))
			}
		}
		slices.Sort(out)
		return out
	}
	for _, p := range twinCells {
		if got, want := body(p[0]), body(p[1]); len(want) == 0 || !slices.Equal(got, want) {
			t.Errorf("%s has the dump lines %q, want those of %s: %q", p[0], got, p[1], want)
		}
	}
}

// checkLiteralReleases checks the deferred unlock of each function that calls a literal releasing its lock finds the lock not held.
func checkLiteralReleases(t *testing.T, lines []string) {
	t.Helper()
	src, err := os.ReadFile("test/lockorder/deferjoin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Split(string(src), "\n")
	for _, fn := range []string{"djLiteralReleases", "djLiteralArgReleases"} {
		at := slices.IndexFunc(text, func(l string) bool { return strings.HasPrefix(l, "func "+fn+"(") })
		off := slices.IndexFunc(text[max(at, 0):], func(l string) bool { return strings.Contains(l, "defer s.mu.Unlock()") })
		if at < 0 || off < 0 {
			t.Errorf("%s: no deferred unlock in the corpus", fn)
			continue
		}
		prefix := fmt.Sprintf("%s\tdeferjoin.go:%d:", fn, at+off+1)
		found := false
		for _, l := range lines {
			if !strings.HasPrefix(l, prefix) {
				continue
			}
			found = true
			if f := strings.Split(l, "\t"); len(f) != 5 || f[3] != "-" {
				t.Errorf("%s: dump line %q, want the lock not held", fn, l)
			}
		}
		if !found {
			t.Errorf("%s: no dump line for its deferred unlock", fn)
		}
	}
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
		return len(f) == 5 && strings.Contains(f[3], "*")
	})
	if !atEntry {
		t.Errorf("want a site held at entry in the dump")
	}
	if !strings.Contains(forward, "\tentry\t") {
		t.Errorf("want an entry line in the dump")
	}
	checkTakePoolHeld(t, lines)
	checkUnwindAnswers(t, lines)
	checkLitCells(t, lines)
	checkTwins(t, lines)
	checkLiteralReleases(t, lines)
	for _, order := range []string{"reverse", "7"} {
		if got := siteDumpOf(t, bin, order); got != forward {
			t.Errorf("the dump differs between forward and order %q", order)
		}
	}
}
