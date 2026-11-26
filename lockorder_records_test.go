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
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const (
	recordsModule    = "github.com/tigerquoll/checklocks"
	recordsLockorder = recordsModule + "/test/lockorder"
)

// recordRows is what the engine state prints in the corpora of lockorder and lockblocking, per case.
var recordRows = map[string][]string{
	"lockorder": {
		"test/lockorder/lockorder.go:535:14: missing expected failure \"acquiring App (via (*RWMutex).Lock) while holding App\"",
		"test/lockorder/lockorder.go:698:16: missing expected failure \"acquiring Partition (via (*Partition).Lock) while holding App\"",
		"test/lockorder/lockorder.go:700:14: missing expected failure \"acquiring App (via (*RWMutex).Lock) while holding App\"",
		"test/lockorder/lockorder.go:707:23: missing expected failure \"acquiring Partition (via (*App).seamDropBoth) while holding App\"",
		"test/lockorder/lockorder.go:718:11: acquiring App (via (*RWMutex).Lock) while holding App: two locks of one class must not nest",
		"test/lockorder/lockorder.go:793:11: missing expected failure \"acquiring Partition (via (*Partition).Lock) while holding App\"",
		"test/lockorder/records.go:122:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:144:12: missing expected failure \"acquiring Partition\"",
		"test/lockorder/records.go:196:9: acquiring App (via (*App).Lock) while holding App: two locks of one class must not nest",
		"test/lockorder/records.go:204:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:204:9: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
		"test/lockorder/records.go:224:10: acquiring Context (via (*Context).Lock) while holding App: the declared order has Context before App",
		"test/lockorder/records.go:51:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:61:9: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
		"test/lockorder/records.go:72:9: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
		"test/lockorder/records.go:81:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
	},
	"lockblocking": {
		"test/lockblocking/records.go:48:3: a channel receive while holding App: a wait under a lock stalls every other user of it",
		"test/lockblocking/records.go:58:19: missing expected failure \"while holding App\"",
		"test/lockblocking/records.go:58:19: missing expected failure \"while holding App\"",
	},
}

// recordNotWalked is the sites the engine state's dump counts as not walked, per case: lock-primitive bodies.
var recordNotWalked = map[string]int{"checklocks": 5, "fresh": 2, "lockgap": 2, "lockorder": 6, "locktype": 6}

// recordDumpDiffs is the functions whose summary line differs between the states.
var recordDumpDiffs = []string{
	recordsLockorder + ": (*App).seamDropBoth",
	recordsLockorder + ": seamRecursiveRead",
	recordsLockorder + ": seamReleaseThenReacquire",
	recordsLockorder + ": seamTwoHolds",
	recordsLockorder + ": zzAliasBodyLiteral$1",
	recordsLockorder + ": zzAliasEntryLiteral$1",
	recordsLockorder + ": zzAliasInAnnotatedLiteral$1",
	recordsLockorder + ": zzAnnotatedLiteralSameClass$1",
	recordsLockorder + ": zzInlineLiteralInherits$1",
	recordsLockorder + ": zzPlainBodyLiteral$1",
	recordsLockorder + ": zzProbeNoIgnore$2",
	recordsLockorder + ": zzTwoReturnsLiteral$1",
	recordsLockorder + ": zzWrapperTakenInLiteral$1",
}

type recordsRun struct {
	rows []string
	dump map[string]string
}

// recordsVet vets args with the lockorder state and walk order given, on its own cache so no run reads another's facts.
func recordsVet(t *testing.T, bin string, args []string, state, order string) recordsRun {
	t.Helper()
	dumpDir := t.TempDir()
	cmd := exec.Command("go", append([]string{"vet", "-vettool=" + bin}, args...)...)
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CHECKLOCKS_SHADOW_LOG=") || strings.HasPrefix(kv, "CHECKLOCKS_DECISION_LOG=")
	})
	cmd.Env = append(env, "GOCACHE="+t.TempDir(), "CHECKLOCKS_LOCKORDER_STATE="+state,
		"CHECKLOCKS_SUMMARY_DUMP="+dumpDir, "CHECKLOCKS_WALK_ORDER="+order)
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

// TestLockOrderRecords runs every corpus case with the walk state and the engine state, and pins what differs.
func TestLockOrderRecords(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-tags=checklocks_parity", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}

	var mu sync.Mutex
	diffs := make(map[string]bool)
	ran := 0
	t.Run("cases", func(t *testing.T) {
		for _, tc := range analyzerCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				mu.Lock()
				ran++
				mu.Unlock()
				args := allEnabledArgs(tc.args)
				if tc.fileList {
					args = expandToFiles(t, args)
				}
				walk := recordsVet(t, bin, args, "walk", "")
				engine := recordsVet(t, bin, args, "engine", "")

				if len(walk.rows) > 0 {
					t.Errorf("walk state printed:\n%s", strings.Join(walk.rows, "\n"))
				}
				want := recordRows[strings.TrimSuffix(tc.name, "-filelist")]
				if !slices.Equal(engine.rows, want) {
					t.Errorf("engine state rows differ:\ngot:\n%s\nwant:\n%s", strings.Join(engine.rows, "\n"), strings.Join(want, "\n"))
				}
				if want != nil {
					for _, order := range []string{"reverse", "7"} {
						if got := recordsVet(t, bin, args, "engine", order).rows; !slices.Equal(got, engine.rows) {
							t.Errorf("walk order %s: rows differ from the default order:\n%s", order, strings.Join(got, "\n"))
						}
					}
				}

				if !slices.Equal(slices.Sorted(maps.Keys(walk.dump)), slices.Sorted(maps.Keys(engine.dump))) {
					t.Errorf("the states wrote different dump files: %q and %q", slices.Sorted(maps.Keys(walk.dump)), slices.Sorted(maps.Keys(engine.dump)))
				}
				for _, unit := range corpusUnits(tc.args, tc.fileList) {
					for arm, run := range map[string]recordsRun{"walk": walk, "engine": engine} {
						if _, ok := run.dump[url.PathEscape(unit)+".summary"]; !ok {
							t.Errorf("%s state wrote no summary dump for %s", arm, unit)
						}
					}
					if _, ok := engine.dump[url.PathEscape(unit)+".blocking"]; !ok {
						t.Errorf("engine state wrote no blocking dump for %s", unit)
					}
				}
				notWalked := map[string]int{}
				for name, text := range engine.dump {
					unit, err := url.PathUnescape(strings.TrimSuffix(strings.TrimSuffix(name, ".summary"), ".blocking"))
					if err != nil {
						t.Fatal(err)
					}
					pkg := strings.TrimSuffix(unit, "@test")
					if !strings.HasPrefix(text, "# "+pkg+" state=engine\n") {
						t.Errorf("%s: header does not name the engine state:\n%.200s", name, text)
					}
					if !strings.HasPrefix(walk.dump[name], "# "+pkg+" state=walk\n") {
						t.Errorf("%s: header does not name the walk state:\n%.200s", name, walk.dump[name])
					}
					for _, line := range strings.Split(text, "\n") {
						if strings.HasPrefix(line, "# norecord walked, site unrecorded") || strings.HasPrefix(line, "# norecord nil view") || strings.HasPrefix(line, "# norecord return not run") {
							t.Errorf("%s: %s", name, line)
						}
						var n int
						if _, err := fmt.Sscanf(line, "# norecord not walked sites=%d", &n); err == nil {
							notWalked[filepath.Ext(name)] += n
						}
						if strings.HasPrefix(line, "# norecord") && strings.HasSuffix(name, ".blocking") {
							t.Logf("%s: %s", name, line)
						}
					}
					w, e := summaryLines(walk.dump[name]), summaryLines(text)
					mu.Lock()
					for fn := range maps.Keys(w) {
						if w[fn] != e[fn] {
							diffs[unit+": "+fn] = true
						}
					}
					for fn := range maps.Keys(e) {
						if w[fn] != e[fn] {
							diffs[unit+": "+fn] = true
						}
					}
					mu.Unlock()
				}
				for _, ext := range []string{".summary", ".blocking"} {
					if n := notWalked[ext]; n != recordNotWalked[tc.name] {
						t.Errorf("%s dumps: %d sites not walked, want %d", ext, n, recordNotWalked[tc.name])
					}
				}
			})
		}
	})
	if ran != len(analyzerCases) {
		return
	}
	if got := slices.Sorted(maps.Keys(diffs)); !slices.Equal(got, recordDumpDiffs) {
		t.Errorf("summaries that differ between the states:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(recordDumpDiffs, "\n"))
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

func summaryLines(dump string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(dump, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, _ := strings.Cut(line, "\t")
		out[name] = line
	}
	return out
}
