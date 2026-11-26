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

package checklocks_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const recordsModule = "github.com/tigerquoll/checklocks"

// recordNotWalked is the sites the engine state's dump counts as not walked, per case: lock-primitive bodies.
var recordNotWalked = map[string]int{"checklocks": 5, "fresh": 2, "lockgap": 2, "lockorder": 6, "locktype": 6}

type recordsRun struct {
	rows []string
	dump map[string]string
}

// recordsVet vets args on its own cache so no run reads another's facts; order is the walk order, empty for SSA order.
func recordsVet(t *testing.T, bin string, args []string, order string) recordsRun {
	t.Helper()
	dumpDir := t.TempDir()
	cmd := exec.Command("go", append([]string{"vet", "-vettool=" + bin}, args...)...)
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CHECKLOCKS_DECISION_LOG=") || strings.HasPrefix(kv, "CHECKLOCKS_WALK_ORDER=")
	})
	cmd.Env = append(env, "GOCACHE="+t.TempDir(), "CHECKLOCKS_SUMMARY_DUMP="+dumpDir, "CHECKLOCKS_WALK_ORDER="+order)
	out, err := cmd.CombinedOutput()
	// Vet exits non-zero when it prints.
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || len(bytes.TrimSpace(out)) == 0) {
		t.Fatalf("vet failed: %v\n%s", err, out)
	}
	run := recordsRun{dump: make(map[string]string)}
	if s := strings.TrimSpace(string(out)); s != "" {
		run.rows = strings.Split(s, "\n")
		slices.Sort(run.rows)
	}
	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dumpDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		run.dump[e.Name()] = string(b)
	}
	return run
}

// TestLockOrderRecords runs every corpus case and pins that nothing is printed and that every site has a record.
func TestLockOrderRecords(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-tags=checklocks_parity", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	for _, tc := range analyzerCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := allEnabledArgs(tc.args)
			if tc.fileList {
				args = expandToFiles(t, args)
			}
			engine := recordsVet(t, bin, args, "")
			if len(engine.rows) > 0 {
				t.Errorf("engine state printed:\n%s", strings.Join(engine.rows, "\n"))
			}
			for _, order := range []string{"reverse", "7"} {
				if got := recordsVet(t, bin, args, order).rows; len(got) > 0 {
					t.Errorf("walk order %s: the engine state printed:\n%s", order, strings.Join(got, "\n"))
				}
			}

			for _, unit := range corpusUnits(tc.args, tc.fileList) {
				for _, ext := range []string{".summary", ".blocking"} {
					if _, ok := engine.dump[url.PathEscape(unit)+ext]; !ok {
						t.Errorf("engine state wrote no %s dump for %s", ext, unit)
					}
				}
			}
			notWalked, gaveUp := map[string]int{}, map[string]int{}
			for name, text := range engine.dump {
				unit, err := url.PathUnescape(strings.TrimSuffix(strings.TrimSuffix(name, ".summary"), ".blocking"))
				if err != nil {
					t.Fatal(err)
				}
				pkg := strings.TrimSuffix(unit, "@test")
				if !strings.HasPrefix(text, "# "+pkg+" state=engine\n") {
					t.Errorf("%s: header does not name the engine state:\n%.200s", name, text)
				}
				for _, line := range strings.Split(text, "\n") {
					if strings.HasPrefix(line, "# norecord walked, site unrecorded") || strings.HasPrefix(line, "# norecord nil view") || strings.HasPrefix(line, "# norecord return not run") {
						t.Errorf("%s: %s", name, line)
					}
					var n int
					if _, err := fmt.Sscanf(line, "# norecord not walked sites=%d", &n); err == nil {
						notWalked[filepath.Ext(name)] += n
					}
					if rest, ok := strings.CutPrefix(line, "# norecord gave up: "); ok {
						if _, tail, ok := strings.Cut(rest, " sites="); ok {
							fmt.Sscanf(tail, "%d", &n)
							gaveUp[filepath.Ext(name)] += n
						}
					}
					if strings.HasPrefix(line, "# norecord") && strings.HasSuffix(name, ".blocking") {
						t.Logf("%s: %s", name, line)
					}
				}
			}
			for _, ext := range []string{".summary", ".blocking"} {
				if n := notWalked[ext]; n != recordNotWalked[tc.name] {
					t.Errorf("%s dumps: %d sites not walked, want %d", ext, n, recordNotWalked[tc.name])
				}
				// A function the engine gave up in is skipped, so no site in one is left.
				if n := gaveUp[ext]; n != 0 {
					t.Errorf("%s dumps: %d sites in a function the engine gave up in, want 0", ext, n)
				}
			}
		})
	}
}

// corpusUnits is the units a case vets, named as the dump files name them.
func corpusUnits(args []string, fileList bool) []string {
	if fileList {
		return []string{"command-line-arguments"}
	}
	var units []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			units = append(units, recordsModule+strings.TrimPrefix(arg, "."))
		}
	}
	return units
}
