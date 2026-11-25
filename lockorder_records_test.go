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

const recordsLockorder = "github.com/tigerquoll/checklocks/test/lockorder"

// recordRows is what the engine state prints in the corpora of lockorder and lockblocking, per case.
var recordRows = map[string][]string{
	"lockorder": {
		"test/lockorder/lockorder.go:535:14: missing expected failure \"acquiring App (via (*RWMutex).Lock) while holding App\"",
		"test/lockorder/lockorder.go:698:16: missing expected failure \"acquiring Partition (via (*Partition).Lock) while holding App\"",
		"test/lockorder/lockorder.go:700:14: missing expected failure \"acquiring App (via (*RWMutex).Lock) while holding App\"",
		"test/lockorder/lockorder.go:707:23: missing expected failure \"acquiring Partition (via (*App).seamDropBoth) while holding App\"",
		"test/lockorder/lockorder.go:718:11: acquiring App (via (*RWMutex).Lock) while holding App: two locks of one class must not nest",
		"test/lockorder/lockorder.go:793:11: missing expected failure \"acquiring Partition (via (*Partition).Lock) while holding App\"",
		"test/lockorder/records.go:102:12: missing expected failure \"acquiring Partition\"",
		"test/lockorder/records.go:122:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:21:12: missing expected failure \"acquiring Partition\"",
		"test/lockorder/records.go:31:12: missing expected failure \"acquiring Partition\"",
		"test/lockorder/records.go:41:12: missing expected failure \"acquiring Partition\"",
		"test/lockorder/records.go:51:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:61:9: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
		"test/lockorder/records.go:72:9: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
		"test/lockorder/records.go:81:9: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:90:2: acquiring Partition (via (*Partition).Lock) while holding App: the declared order has Partition before App",
		"test/lockorder/records.go:90:2: acquiring Partition (via (*Partition).Lock) while holding SeamInner: the declared order has Partition before SeamInner",
	},
	"lockblocking": {
		"test/lockblocking/records.go:27:2: calling Sleep, which blocks while holding App, ZZOther: a wait under a lock stalls every other user of it",
		"test/lockblocking/records.go:27:2: calling Sleep, which blocks while holding App, ZZOther: a wait under a lock stalls every other user of it",
		"test/lockblocking/records.go:27:37: missing expected failure \"while holding App:\"",
		"test/lockblocking/records.go:27:37: missing expected failure \"while holding ZZOther:\"",
		"test/lockblocking/records.go:39:7: missing expected failure \"a channel receive\"",
		"test/lockblocking/records.go:48:3: a channel receive while holding App: a wait under a lock stalls every other user of it",
	},
}

// recordDumpDiffs is the functions whose summary line differs between the states.
var recordDumpDiffs = []string{
	recordsLockorder + ": (*App).seamDropBoth",
	recordsLockorder + ": seamRecursiveRead",
	recordsLockorder + ": seamReleaseThenReacquire",
	recordsLockorder + ": seamTwoHolds",
	recordsLockorder + ": zzAliasBodyLiteral$1",
	recordsLockorder + ": zzAliasEntryLiteral$1",
	recordsLockorder + ": zzAliasTakenInLiteral$1",
	recordsLockorder + ": zzCallbackInverts$1",
	recordsLockorder + ": zzDeferredLiteralInverts$1",
	recordsLockorder + ": zzGoLiteralInverts$1",
	recordsLockorder + ": zzInlineLiteralInherits$1",
	recordsLockorder + ": zzPlainBodyLiteral$1",
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
	cmd.Env = append(os.Environ(), "GOCACHE="+t.TempDir(), "CHECKLOCKS_LOCKORDER_STATE="+state,
		"CHECKLOCKS_SUMMARY_DUMP="+dumpDir, "CHECKLOCKS_WALK_ORDER="+order)
	// Vet exits non-zero when it prints.
	out, _ := cmd.CombinedOutput()
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
	t.Run("cases", func(t *testing.T) {
		for _, tc := range analyzerCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
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
				for name, text := range engine.dump {
					unit, err := url.PathUnescape(strings.TrimSuffix(name, ".summary"))
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
						if strings.HasPrefix(line, "# norecord walked, site unrecorded") || strings.HasPrefix(line, "# norecord nil view") {
							t.Errorf("%s: %s", name, line)
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
			})
		}
	})
	if got := slices.Sorted(maps.Keys(diffs)); !slices.Equal(got, recordDumpDiffs) {
		t.Errorf("summaries that differ between the states:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(recordDumpDiffs, "\n"))
	}
}

// summaryLines maps each function of a dump to its line.
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
