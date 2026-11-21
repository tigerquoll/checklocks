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
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const decisionPkg = "github.com/tigerquoll/checklocks/test/decisionlog"

// decisionLogOf vets the decision corpus with a fresh build cache and returns the log it wrote.
func decisionLogOf(t *testing.T, bin string) []byte {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("go", "vet", "-vettool="+bin, "./test/decisionlog")
	cmd.Env = append(os.Environ(), "GOCACHE="+t.TempDir(), "CHECKLOCKS_DECISION_LOG="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("vet failed: %v\n%s", err, out)
	}
	log, err := os.ReadFile(filepath.Join(dir, url.PathEscape(decisionPkg)+".log"))
	if err != nil {
		t.Fatalf("reading the log failed: %v", err)
	}
	return log
}

// TestDecisionLog reads the log of the decision corpus for the checks at known lines.
func TestDecisionLog(t *testing.T) {
	src, err := os.ReadFile("test/decisionlog/decisionlog.go")
	if err != nil {
		t.Fatal(err)
	}
	lineOf := func(prefix string) int {
		for i, l := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(l, prefix) {
				return i + 1
			}
		}
		t.Fatalf("no line starts with %q", prefix)
		return 0
	}

	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-tags=checklocks_parity", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}
	first := decisionLogOf(t, bin)
	if second := decisionLogOf(t, bin); !bytes.Equal(first, second) {
		t.Errorf("two runs wrote different logs:\n%s\n---\n%s", first, second)
	}

	var lines [][]string
	for _, l := range strings.Split(strings.TrimSuffix(string(first), "\n"), "\n") {
		lines = append(lines, strings.Split(l, "\t"))
	}
	// A line is pos, function, coord, kind, subject, context, ok.
	find := func(match func(f []string) bool) [][]string {
		var out [][]string
		for _, f := range lines {
			if len(f) != 7 {
				t.Fatalf("malformed line %q", f)
			}
			if match(f) {
				out = append(out, f)
			}
		}
		return out
	}

	for _, c := range []struct {
		fn   string
		line int
		ok   string
	}{
		{"heldAccess", lineOf("func heldAccess(") + 2, "true"},
		{"unheldAccess", lineOf("func unheldAccess(") + 1, "false"},
	} {
		got := find(func(f []string) bool {
			return f[1] == c.fn && f[3] == "guardHeld" && strings.HasPrefix(f[0], fmt.Sprintf("decisionlog.go:%d:", c.line))
		})
		if len(got) != 1 || got[0][6] != c.ok {
			t.Errorf("%s: want one guardHeld decision %s, got %q", c.fn, c.ok, got)
		}
	}

	coords := make(map[string]bool)
	for _, f := range find(func(f []string) bool { return f[0] == "-:0:0" && f[1] == "needsLock" && f[3] == "returnHeld" }) {
		coords[f[2]] = true
	}
	if len(coords) < 2 {
		t.Errorf("want the position-less returns of needsLock on two coordinates, got %v", coords)
	}

	line := lineOf("\tval.val = 4")
	var subjects []string
	for _, f := range find(func(f []string) bool {
		return f[1] == "sameName" && f[3] == "guardHeld" && strings.HasPrefix(f[0], fmt.Sprintf("decisionlog.go:%d:", line))
	}) {
		subjects = append(subjects, f[4])
	}
	slices.Sort(subjects)
	if want := []string{"mu decisionlog.named.val", "mu decisionlog.val"}; !slices.Equal(subjects, want) {
		t.Errorf("want the guardHeld subjects %q at %d, got %q", want, line, subjects)
	}

	var contexts []string
	for _, f := range find(func(f []string) bool { return f[1] == "literalTwice$1" && f[3] == "guardHeld" }) {
		contexts = append(contexts, f[5])
	}
	slices.Sort(contexts)
	if len(slices.Compact(contexts)) != 2 {
		t.Errorf("want the literal's guardHeld decisions under two contexts, got %q", contexts)
	}
}
