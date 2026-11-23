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
	"slices"
	"testing"
)

// TestEngineViewJoin pins how states join at a site: assumed only if every state assumes it,
// entry if any state holds it from entry, and an empty set is still a record.
func TestEngineViewJoin(t *testing.T) {
	pkg := buildTestPackage(t, "join", "package join\n\nfunc f() {}\n\nfunc g() {}\n")
	f, g := pkg.Func("f"), pkg.Func("g")
	at, other := f.Blocks[0].Instrs[0], g.Blocks[0].Instrs[0]

	p := newLockPath(pathRootValue, nil, nil, "mu")
	held := func(assumed bool) map[pathKey]lockInfo {
		return map[pathKey]lockInfo{p.Key(): {path: p, assumed: assumed}}
	}
	var e engineView
	e.add(at, held(true), nil)
	e.add(at, held(false), held(false))
	e.add(other, nil, nil)

	got, ok := e.heldAt(at)
	if !ok || len(got) != 1 || got[0].assumed || !got[0].entry {
		t.Errorf("joined record = %+v, %v; want one lock, not assumed, held from entry", got, ok)
	}
	e.add(at, held(true), nil)
	if got, _ := e.heldAt(at); got[0].assumed {
		t.Error("a state that assumes the lock made it assumed again")
	}
	if got, ok := e.heldAt(other); !ok || len(got) != 0 {
		t.Errorf("empty state = %v, %v; want a record with no locks", got, ok)
	}
}

// TestEntryRaisesBaselineOnly pins that the engine's entry classes join the function's own
// without counting as held.
func TestEntryRaisesBaselineOnly(t *testing.T) {
	cs := newClassSet()
	cs.enter("A")
	cs.raiseEntry("B")
	cs.raiseEntry("A")
	if cs.entry["A"] != 1 || cs.entry["B"] != 1 {
		t.Errorf("entry = %v, want A and B at 1", cs.entry)
	}
	if got := cs.held(); !slices.Equal(got, []string{"A"}) {
		t.Errorf("held = %v, want only A", got)
	}
}
