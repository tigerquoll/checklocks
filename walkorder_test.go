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
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	modulePrefix = "github.com/tigerquoll/checklocks/"
	testPkg      = modulePrefix + "test"
	orderPkg     = modulePrefix + "test/order"
)

// walkOrders are the successor orders compared; the first is SSA order.
var walkOrders = []string{"", "reverse", "1", "2"}

// orderDiff is a decision log line that one order has and SSA order lacks (+) or the reverse (-).
type orderDiff struct {
	pkg, line, sign string
}

// knownOrderDiffs are the differences measured today, each with the orders it occurs in.
// The keys hold source positions: a line shift in test/loops.go or test/order/order.go means re-measuring them.
var knownOrderDiffs = map[orderDiff][]string{
	// loopIrreducible: the on-stack cut makes the decisions depend on the edge walked first.
	{testPkg, "loops.go:201:6\tloopIrreducible\texit:0\tincompatibleReturn\t\tloopIrreducible\tfalse", "+"}: {"1", "reverse"},
	{testPkg, "loops.go:201:6\tloopIrreducible\texit:0\tincompatibleReturn\t\tloopIrreducible\ttrue", "-"}:  {"1", "reverse"},
	{testPkg, "loops.go:211:1\tloopIrreducible\tb3.i0\treturnCount\t\tloopIrreducible\tfalse", "+"}:         {"1", "reverse"},
	// memoDefersFirstBad: the defer list of the branch walked first decides the join.
	{orderPkg, "order.go:50:3\tmemoDefersFirstBad\tb3.i1\tlockOpUnlock\t\tmemoDefersFirstBad\ttrue", "+"}: {"2", "reverse"},
	{orderPkg, "order.go:53:2\tmemoDefersFirstBad\tb2.i3\treturnCount\t\tmemoDefersFirstBad\tfalse", "-"}: {"2", "reverse"},
	{orderPkg, "order.go:53:2\tmemoDefersFirstBad\tb2.i3\treturnCount\t\tmemoDefersFirstBad\ttrue", "+"}:  {"2", "reverse"},
	// memoDefersFirstGood: the defer list of the branch walked first decides the join.
	{orderPkg, "order.go:38:3\tmemoDefersFirstGood\tb1.i1\tlockOpUnlock\t\tmemoDefersFirstGood\ttrue", "-"}: {"2", "reverse"},
	{orderPkg, "order.go:41:2\tmemoDefersFirstGood\tb2.i3\treturnCount\t\tmemoDefersFirstGood\tfalse", "+"}: {"2", "reverse"},
	{orderPkg, "order.go:41:2\tmemoDefersFirstGood\tb2.i3\treturnCount\t\tmemoDefersFirstGood\ttrue", "-"}:  {"2", "reverse"},
	// memoStoredFirstBad: the path stored in p is the one from the branch walked first.
	{orderPkg, "order.go:78:4\tmemoStoredFirstBad\tb2.i4\tguardHeld\tmu order.T.g\tmemoStoredFirstBad\tfalse", "-"}: {"2", "reverse"},
	{orderPkg, "order.go:78:4\tmemoStoredFirstBad\tb2.i4\tguardHeld\tmu order.T.g\tmemoStoredFirstBad\ttrue", "+"}:  {"2", "reverse"},
	// memoStoredFirstGood: the path stored in p is the one from the branch walked first.
	{orderPkg, "order.go:66:4\tmemoStoredFirstGood\tb2.i4\tguardHeld\tmu order.T.g\tmemoStoredFirstGood\tfalse", "+"}: {"2", "reverse"},
	{orderPkg, "order.go:66:4\tmemoStoredFirstGood\tb2.i4\tguardHeld\tmu order.T.g\tmemoStoredFirstGood\ttrue", "-"}:  {"2", "reverse"},
	// retargetElse: the report moves with the branch walked first.
	{orderPkg, "order.go:106:4\tretargetElse\tb2.i1\tguardHeld\tmu order.T.g\tretargetElse\tfalse", "+"}: {"1", "reverse"},
	{orderPkg, "order.go:106:4\tretargetElse\tb2.i1\tguardHeld\tmu order.T.g\tretargetElse\ttrue", "-"}:  {"1", "reverse"},
	// retargetThen: the report moves with the branch walked first.
	{orderPkg, "order.go:92:4\tretargetThen\tb2.i1\tguardHeld\tmu order.T.g\tretargetThen\tfalse", "-"}: {"1", "reverse"},
	{orderPkg, "order.go:92:4\tretargetThen\tb2.i1\tguardHeld\tmu order.T.g\tretargetThen\ttrue", "+"}:  {"1", "reverse"},
}

// walkLogsOf vets the checklocks case under one order and returns the log lines of each module package
// and the output of vet.
func walkLogsOf(t *testing.T, bin, order string) (map[string]map[string]bool, []byte) {
	t.Helper()
	dir := t.TempDir()
	args := []string{"vet", "-vettool=" + bin, "-lockstringer=false", "-checklocks.wrappers=false", "./test", "./test/crosspkg", "./test/order"}
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOCACHE="+t.TempDir(), "CHECKLOCKS_DECISION_LOG="+dir, "CHECKLOCKS_WALK_ORDER="+order)
	// Reports differ by order, so the exit status says nothing.
	out, _ := cmd.CombinedOutput()

	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	logs := make(map[string]map[string]bool)
	for _, f := range files {
		name, err := url.PathUnescape(strings.TrimSuffix(f.Name(), ".log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(name, modulePrefix) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lines := make(map[string]bool)
		for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if l != "" {
				lines[l] = true
			}
		}
		logs[name] = lines
	}
	return logs, out
}

// TestWalkOrder compares the decision logs of the module's packages across successor orders.
func TestWalkOrder(t *testing.T) {
	// The analyzer runs in a subprocess; reading the corpus registers it as an input of this test's cache entry.
	files := 0
	if err := filepath.WalkDir("test", func(path string, d fs.DirEntry, err error) error {
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

	forward, out := walkLogsOf(t, bin, walkOrders[0])
	if len(forward[testPkg]) == 0 {
		t.Logf("vet output:\n%s", out)
		t.Fatalf("the forward log of %s is empty", testPkg)
	}

	found := make(map[orderDiff][]string)
	for _, order := range walkOrders[1:] {
		logs, out := walkLogsOf(t, bin, order)
		for pkg := range forward {
			if logs[pkg] == nil {
				t.Errorf("order %q wrote no log for %s", order, pkg)
				t.Logf("vet output:\n%s", out)
			}
		}
		for pkg := range logs {
			if forward[pkg] == nil {
				t.Errorf("order %q wrote a log for %s that forward did not", order, pkg)
			}
		}
		for pkg, lines := range logs {
			for l := range lines {
				if !forward[pkg][l] {
					key := orderDiff{pkg, l, "+"}
					found[key] = append(found[key], order)
				}
			}
			for l := range forward[pkg] {
				if !lines[l] {
					key := orderDiff{pkg, l, "-"}
					found[key] = append(found[key], order)
				}
			}
		}
	}

	for key, orders := range found {
		want, ok := knownOrderDiffs[key]
		switch {
		case !ok:
			t.Errorf("unexpected difference in orders %q: %s %q %q", orders, key.pkg, key.sign, key.line)
		case !slices.Equal(slices.Sorted(slices.Values(orders)), slices.Sorted(slices.Values(want))):
			t.Errorf("difference in orders %q, want %q: %s %q %q", orders, want, key.pkg, key.sign, key.line)
		}
	}
	for key := range knownOrderDiffs {
		if _, ok := found[key]; !ok {
			t.Errorf("known difference no longer occurs: %s %q %q", key.pkg, key.sign, key.line)
		}
	}
}

// TestSuccOrder checks the orders permuteSuccs gives a function with many two-way branches.
func TestSuccOrder(t *testing.T) {
	pkg := buildTestPackage(t, "p", `package p

func f(a, b, c, d, e, g, h, i bool) int {
	n := 0
	if a {
		n++
	}
	if b {
		n++
	}
	if c {
		n++
	}
	if d {
		n++
	}
	if e {
		n++
	}
	if g {
		n++
	}
	if h {
		n++
	}
	if i {
		n++
	}
	return n
}
`)
	fn := pkg.Func("f")
	reversedIn := func(order string) []bool {
		var out []bool
		for _, b := range fn.Blocks {
			if len(b.Succs) != 2 {
				continue
			}
			before := slices.Clone(b.Succs)
			got := permuteSuccs(order, fn, b)
			if !slices.Equal(b.Succs, before) {
				t.Fatalf("order %q changed the successors of block %d", order, b.Index)
			}
			switch {
			case slices.Equal(got, before):
				out = append(out, false)
			case got[0] == before[1] && got[1] == before[0]:
				out = append(out, true)
			default:
				t.Fatalf("order %q gave block %d successors %v, from %v", order, b.Index, got, before)
			}
		}
		return out
	}

	if n := len(reversedIn("")); n < 6 {
		t.Fatalf("want at least six two-way blocks, got %d", n)
	}
	if slices.Contains(reversedIn(""), true) {
		t.Errorf("want SSA order to reverse no block")
	}
	if slices.Contains(reversedIn("reverse"), false) {
		t.Errorf("want reverse to reverse every block")
	}
	one, two := reversedIn("1"), reversedIn("2")
	if !slices.Contains(one, true) || !slices.Contains(one, false) {
		t.Errorf("want seed 1 to reverse some blocks and keep others, got %v", one)
	}
	if slices.Equal(one, two) {
		t.Errorf("want seeds 1 and 2 to differ, both gave %v", one)
	}
}
