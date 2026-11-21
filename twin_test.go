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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// TestTwinLog pins that each op logs one twin for locks of one rendering and two identities.
func TestTwinLog(t *testing.T) {
	pkg := buildTestPackage(t, "a", keySource)
	a := makeResolvedValue(pkg.Func("A").Params[0], nil)
	b := makeResolvedValue(pkg.Func("B").Params[0], nil)
	other := makeResolvedValue(ssa.NewConst(nil, pkg.Func("A").Params[0].Type()), nil)

	holding := func(rv resolvedValue) *lockState {
		ls := newLockState()
		ls.lockField(rv, true)
		return ls
	}
	cases := map[string]func(){
		"held":      func() { holding(a).isHeld(b, false) },
		"lock":      func() { holding(a).lockField(b, true) },
		"unlock":    func() { holding(a).unlockField(b, true) },
		"downgrade": func() { holding(a).downgradeField(b) },
		"alias":     func() { newLockState().addAlias(a, b) },
		"aliaspath": func() {
			ls := newLockState()
			ls.addAlias(a, other)
			path, _ := b.valueAndObject(ls)
			ls.aliasPath(path)
		},
		"subset": func() { holding(a).isSubset(holding(b)) },
	}
	for op, run := range cases {
		log := filepath.Join(t.TempDir(), "twin.log")
		t.Setenv("CHECKLOCKS_TWIN_LOG", log)
		run()
		data, _ := os.ReadFile(log)
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(lines) != 1 {
			t.Errorf("%s: logged %d lines, want 1: %q", op, len(lines), data)
			continue
		}
		fields := strings.Split(lines[0], "\t")
		if len(fields) != 5 || fields[0] != "twin" || fields[1] != op || fields[3] == fields[4] {
			t.Errorf("%s: logged %q, want a twin line with two keys", op, lines[0])
		}
	}
}
