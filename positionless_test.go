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

package checklocks

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const preconditionPin = `package pin

import "sync"

// +checklockslocktype
type P struct{ gate sync.Mutex }

// +checklocks:p.gate
func (p *P) Lock() {}

func escape(p *P) func() { return p.Lock }
`

const goPinPrelude = `package pin

import "sync"

type T struct {
	mu sync.Mutex
	rw sync.RWMutex
}

`

// vetPositionless vets one scratch package and returns its non-header output lines.
func vetPositionless(t *testing.T, src string) []string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module pin\n\ngo 1.22\n",
		"pin.go": src,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "checklocks")
	if out, err := exec.Command("go", "build", "-o", bin, "./cmd/checklocks").CombinedOutput(); err != nil {
		t.Fatalf("building the vettool failed: %v\n%s", err, out)
	}
	cmd := exec.Command("go", "vet", "-vettool="+bin, "-lockstringer=false", "-lockorder=false", "-lockblocking=false", "-checklocks.inferred=false", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("want vet to exit with code 1, got %v\n%s", err, out)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "-:") {
			t.Fatalf("want only positionless lines, got %q in:\n%s", line, out)
		}
		lines = append(lines, line)
	}
	return lines
}

// A lock-op method value whose method has a precondition is walked and reports at -.
func TestPreconditionWrapperStillReports(t *testing.T) {
	n := 0
	for _, line := range vetPositionless(t, preconditionPin) {
		if strings.HasPrefix(line, "-: must hold p.gate exclusively") && strings.Contains(line, "to call Lock") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want one positionless precondition report for Lock, got %d", n)
	}
}

// A lock method value started with go is walked and reports at -.
func TestGoStartedWrapperStillReports(t *testing.T) {
	for name, fn := range map[string]string{
		"direct":  "func g(t *T) { f := t.mu.Unlock; go f() }\n",
		"phi":     "func g(t *T, c bool) { f := t.mu.Unlock; if c { f = t.rw.Unlock }; go f() }\n",
		"capture": "func g(t *T) { f := t.mu.Unlock; go func() { f() }() }\n",
	} {
		t.Run(name, func(t *testing.T) {
			lines := vetPositionless(t, goPinPrelude+fn)
			if len(lines) != 1 || !strings.HasPrefix(lines[0], "-: {freevar:recv} already unlocked or locked differently") {
				t.Errorf("want one positionless already-unlocked report, got %q", lines)
			}
		})
	}
}
