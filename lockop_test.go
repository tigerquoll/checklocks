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
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

var probeMethods = []string{
	"Lock", "RLock", "NestedLock", "Unlock", "RUnlock", "NestedUnlock", "DowngradeLock", "TryLock", "Close",
}

type probeRecv struct {
	typ     string
	methods []string
}

var probeRecvs = []probeRecv{
	{"*sync.Mutex", []string{"Lock", "Unlock", "TryLock"}},
	{"*sync.RWMutex", []string{"Lock", "RLock", "Unlock", "RUnlock", "TryLock"}},
	{"sync.Locker", []string{"Lock", "Unlock"}},
	{"*Guard", probeMethods},
	{"GuardIface", probeMethods},
	{"*Plain", probeMethods},
	{"*G[*sync.Mutex]", probeMethods},
	{"*G[int]", probeMethods},
}

// probeCall is one call in the probe package: its key is the callee and the call shape.
type probeCall struct {
	key    string
	common *ssa.CallCommon
}

// buildProbe returns a pass declaring Guard and GuardIface lock primitives, and one call per grid row.
func buildProbe(t *testing.T) (*analysis.Pass, []probeCall) {
	t.Helper()
	var src strings.Builder
	src.WriteString("package probe\n\nimport \"sync\"\n\n")
	src.WriteString("type Guard struct{}\ntype Plain struct{}\ntype G[L any] struct{ l L }\n")
	src.WriteString("type GuardIface interface {\n")
	for _, m := range probeMethods {
		fmt.Fprintf(&src, "\t%s()\n", m)
	}
	src.WriteString("}\n")
	for _, m := range probeMethods {
		fmt.Fprintf(&src, "func (*Guard) %s() {}\nfunc (*Plain) %s() {}\nfunc (*G[L]) %s() {}\n", m, m, m)
	}
	var keys []string
	for _, r := range probeRecvs {
		shape := "static"
		if r.typ == "sync.Locker" || r.typ == "GuardIface" {
			shape = "interface"
		}
		for _, m := range r.methods {
			fmt.Fprintf(&src, "func d%d(r %s) { r.%s() }\n", len(keys), r.typ, m)
			keys = append(keys, fmt.Sprintf("%s.%s/%s", r.typ, m, shape))
			fmt.Fprintf(&src, "func d%d(r %s) { f := r.%s; f() }\n", len(keys), r.typ, m)
			keys = append(keys, fmt.Sprintf("%s.%s/value", r.typ, m))
		}
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe/x.go", src.String(), 0)
	if err != nil {
		t.Fatalf("parsing probe: %v", err)
	}
	pkg, _, err := ssautil.BuildPackage(&types.Config{Importer: importer.Default()}, fset,
		types.NewPackage("probe", "probe"), []*ast.File{f}, ssa.BuilderMode(0))
	if err != nil {
		t.Fatalf("building probe: %v", err)
	}

	guard := pkg.Pkg.Scope().Lookup("Guard")
	guardIface := pkg.Pkg.Scope().Lookup("GuardIface")
	pass := &analysis.Pass{ImportObjectFact: func(o types.Object, fact analysis.Fact) bool {
		_, ok := fact.(*lockPrimitiveFacts)
		return ok && (o == guard || o == guardIface)
	}}

	var calls []probeCall
	for i, key := range keys {
		fn := pkg.Func(fmt.Sprintf("d%d", i))
		for _, b := range fn.Blocks {
			for _, inst := range b.Instrs {
				if ci, ok := inst.(ssa.CallInstruction); ok {
					calls = append(calls, probeCall{key: key, common: ci.Common()})
				}
			}
		}
	}
	if len(calls) != len(keys) {
		t.Fatalf("probe has %d calls for %d drivers", len(calls), len(keys))
	}
	return pass, calls
}

func probeCallee(common *ssa.CallCommon) *types.Func {
	if common.Method != nil {
		return common.Method
	}
	if sf, ok := common.Value.(*ssa.Function); ok && sf.Object() != nil {
		fn, _ := sf.Object().(*types.Func)
		return fn
	}
	return nil
}

// oldIsLockAcquire is a verbatim copy of isLockAcquire at 154c721.
func oldIsLockAcquire(pass *analysis.Pass, common *ssa.CallCommon) bool {
	var fn *types.Func
	if common.Method != nil {
		fn = common.Method
	} else if sf, ok := common.Value.(*ssa.Function); ok && sf.Object() != nil {
		fn, _ = sf.Object().(*types.Func)
	}
	if fn == nil {
		return false
	}
	switch fn.Name() {
	case "Lock", "RLock", "NestedLock":
	default:
		return false
	}
	full := fn.FullName()
	if rwMutexRE.MatchString(full) || mutexRE.MatchString(full) || lockerRE.MatchString(full) {
		return true
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		_, declared := lockPrimitiveIn(pass, sig.Recv().Type())
		return declared
	}
	return false
}

// oldIsStandardLock is a verbatim copy of isStandardLock at 154c721.
func oldIsStandardLock(pass *analysis.Pass, fn *types.Func) bool {
	name := fn.FullName()
	if mutexRE.MatchString(name) || lockerRE.MatchString(name) {
		return true
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if _, declared := lockPrimitiveIn(pass, sig.Recv().Type()); declared {
			return true
		}
	}
	return false
}

// oldIsLockWrapper is lockgap's isLockWrapper at 154c721, taking the pass as a parameter.
func oldIsLockWrapper(pass *analysis.Pass, obj *types.Func) bool {
	if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil {
		if _, declared := lockPrimitiveIn(pass, sig.Recv().Type()); declared {
			return true
		}
	}
	return false
}

func TestLockAcquireMatchesOldMatcher(t *testing.T) {
	pass, calls := buildProbe(t)
	for _, c := range calls {
		if got, want := isLockAcquire(pass, c.common), oldIsLockAcquire(pass, c.common); got != want {
			t.Errorf("%s: isLockAcquire = %v, old matcher = %v", c.key, got, want)
		}
		fn := probeCallee(c.common)
		if fn == nil {
			continue
		}
		if got, want := isStandardLock(pass, fn), oldIsStandardLock(pass, fn) || oldIsLockWrapper(pass, fn); got != want {
			t.Errorf("%s: isStandardLock = %v, old lockgap condition = %v", c.key, got, want)
		}
	}
}

// A method value is called through a closure, which carries no callee, so those rows are false.
func TestIsLockAcquireTable(t *testing.T) {
	want := map[string]bool{
		"*sync.Mutex.Lock/static":              true,
		"*sync.Mutex.Lock/value":               false,
		"*sync.Mutex.Unlock/static":            false,
		"*sync.Mutex.Unlock/value":             false,
		"*sync.Mutex.TryLock/static":           false,
		"*sync.Mutex.TryLock/value":            false,
		"*sync.RWMutex.Lock/static":            true,
		"*sync.RWMutex.Lock/value":             false,
		"*sync.RWMutex.RLock/static":           true,
		"*sync.RWMutex.RLock/value":            false,
		"*sync.RWMutex.Unlock/static":          false,
		"*sync.RWMutex.Unlock/value":           false,
		"*sync.RWMutex.RUnlock/static":         false,
		"*sync.RWMutex.RUnlock/value":          false,
		"*sync.RWMutex.TryLock/static":         false,
		"*sync.RWMutex.TryLock/value":          false,
		"sync.Locker.Lock/interface":           true,
		"sync.Locker.Lock/value":               false,
		"sync.Locker.Unlock/interface":         false,
		"sync.Locker.Unlock/value":             false,
		"*Guard.Lock/static":                   true,
		"*Guard.Lock/value":                    false,
		"*Guard.RLock/static":                  true,
		"*Guard.RLock/value":                   false,
		"*Guard.NestedLock/static":             true,
		"*Guard.NestedLock/value":              false,
		"*Guard.Unlock/static":                 false,
		"*Guard.Unlock/value":                  false,
		"*Guard.RUnlock/static":                false,
		"*Guard.RUnlock/value":                 false,
		"*Guard.NestedUnlock/static":           false,
		"*Guard.NestedUnlock/value":            false,
		"*Guard.DowngradeLock/static":          false,
		"*Guard.DowngradeLock/value":           false,
		"*Guard.TryLock/static":                false,
		"*Guard.TryLock/value":                 false,
		"*Guard.Close/static":                  false,
		"*Guard.Close/value":                   false,
		"GuardIface.Lock/interface":            true,
		"GuardIface.Lock/value":                false,
		"GuardIface.RLock/interface":           true,
		"GuardIface.RLock/value":               false,
		"GuardIface.NestedLock/interface":      true,
		"GuardIface.NestedLock/value":          false,
		"GuardIface.Unlock/interface":          false,
		"GuardIface.Unlock/value":              false,
		"GuardIface.RUnlock/interface":         false,
		"GuardIface.RUnlock/value":             false,
		"GuardIface.NestedUnlock/interface":    false,
		"GuardIface.NestedUnlock/value":        false,
		"GuardIface.DowngradeLock/interface":   false,
		"GuardIface.DowngradeLock/value":       false,
		"GuardIface.TryLock/interface":         false,
		"GuardIface.TryLock/value":             false,
		"GuardIface.Close/interface":           false,
		"GuardIface.Close/value":               false,
		"*Plain.Lock/static":                   false,
		"*Plain.Lock/value":                    false,
		"*Plain.RLock/static":                  false,
		"*Plain.RLock/value":                   false,
		"*Plain.NestedLock/static":             false,
		"*Plain.NestedLock/value":              false,
		"*Plain.Unlock/static":                 false,
		"*Plain.Unlock/value":                  false,
		"*Plain.RUnlock/static":                false,
		"*Plain.RUnlock/value":                 false,
		"*Plain.NestedUnlock/static":           false,
		"*Plain.NestedUnlock/value":            false,
		"*Plain.DowngradeLock/static":          false,
		"*Plain.DowngradeLock/value":           false,
		"*Plain.TryLock/static":                false,
		"*Plain.TryLock/value":                 false,
		"*Plain.Close/static":                  false,
		"*Plain.Close/value":                   false,
		"*G[*sync.Mutex].Lock/static":          true,
		"*G[*sync.Mutex].Lock/value":           false,
		"*G[*sync.Mutex].RLock/static":         true,
		"*G[*sync.Mutex].RLock/value":          false,
		"*G[*sync.Mutex].NestedLock/static":    true,
		"*G[*sync.Mutex].NestedLock/value":     false,
		"*G[*sync.Mutex].Unlock/static":        false,
		"*G[*sync.Mutex].Unlock/value":         false,
		"*G[*sync.Mutex].RUnlock/static":       false,
		"*G[*sync.Mutex].RUnlock/value":        false,
		"*G[*sync.Mutex].NestedUnlock/static":  false,
		"*G[*sync.Mutex].NestedUnlock/value":   false,
		"*G[*sync.Mutex].DowngradeLock/static": false,
		"*G[*sync.Mutex].DowngradeLock/value":  false,
		"*G[*sync.Mutex].TryLock/static":       false,
		"*G[*sync.Mutex].TryLock/value":        false,
		"*G[*sync.Mutex].Close/static":         false,
		"*G[*sync.Mutex].Close/value":          false,
		"*G[int].Lock/static":                  false,
		"*G[int].Lock/value":                   false,
		"*G[int].RLock/static":                 false,
		"*G[int].RLock/value":                  false,
		"*G[int].NestedLock/static":            false,
		"*G[int].NestedLock/value":             false,
		"*G[int].Unlock/static":                false,
		"*G[int].Unlock/value":                 false,
		"*G[int].RUnlock/static":               false,
		"*G[int].RUnlock/value":                false,
		"*G[int].NestedUnlock/static":          false,
		"*G[int].NestedUnlock/value":           false,
		"*G[int].DowngradeLock/static":         false,
		"*G[int].DowngradeLock/value":          false,
		"*G[int].TryLock/static":               false,
		"*G[int].TryLock/value":                false,
		"*G[int].Close/static":                 false,
		"*G[int].Close/value":                  false,
	}
	pass, calls := buildProbe(t)
	if len(calls) != len(want) {
		t.Fatalf("probe has %d calls, table has %d rows", len(calls), len(want))
	}
	for _, c := range calls {
		w, ok := want[c.key]
		if !ok {
			t.Errorf("%s: no row in the table", c.key)
			continue
		}
		if got := isLockAcquire(pass, c.common); got != w {
			t.Errorf("%s: isLockAcquire = %v, want %v", c.key, got, w)
		}
	}
}
