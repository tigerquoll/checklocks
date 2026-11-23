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

// A lock-op method value whose method has a precondition is still walked, and
// its positionless report stays.
func TestPreconditionWrapperStillReports(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod": "module pin\n\ngo 1.22\n",
		"pin.go": preconditionPin,
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
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "-: must hold p.gate exclusively") || !strings.Contains(string(out), "to call Lock") {
		t.Errorf("want a positionless precondition report for Lock, got:\n%s", out)
	}
}
