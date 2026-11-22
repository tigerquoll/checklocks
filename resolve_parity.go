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
	"fmt"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// shadowCounts holds the sites compared and the mismatches found in one call.
type shadowCounts struct {
	direct, own int
	mismatches  []string
}

func (r *resolver) shadowDirect(call ssa.CallInstruction, v ssa.Value, recv *ssa.Parameter, captured map[*ssa.FreeVar]ssa.Value) {
	r.shadow.direct++
	r.shadowCompare("direct", call, resolvesToReceiver(v, recv, captured), r.isReceiver(v, recv, false))
}

func (r *resolver) shadowOwn(call ssa.CallInstruction, v ssa.Value, recv *ssa.Parameter, captured map[*ssa.FreeVar]ssa.Value) {
	r.shadow.own++
	r.shadowCompare("own", call, resolvesToReceiver(v, recv, captured), r.isReceiver(v, recv, true))
}

func (r *resolver) shadowCompare(leg string, call ssa.CallInstruction, old, resolved bool) {
	if old == resolved {
		return
	}
	fn := call.Parent()
	line := fmt.Sprintf("M %s old=%v new=%v fn=%s pos=%s", leg, old, resolved, fn.String(), fn.Prog.Fset.Position(call.Pos()))
	if os.Getenv("CHECKLOCKS_SYNTH_SHADOW") == "" {
		panic(line)
	}
	r.shadow.mismatches = append(r.shadow.mismatches, line)
}

// shadowReport appends the unit's site counts and mismatches in one write.
func (r *resolver) shadowReport(pkg string) {
	lines := append([]string{fmt.Sprintf("N %s direct=%d own=%d", pkg, r.shadow.direct, r.shadow.own)}, r.shadow.mismatches...)
	appendLines(os.Getenv("CHECKLOCKS_SYNTH_SHADOW"), lines)
}

func appendLines(path string, lines []string) {
	if path == "" || len(lines) == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		panic(err)
	}
}

// dumpSynthFacts appends the lock facts of fns, one line per entry, in a form
// that does not depend on map order or positions.
func (pc *passContext) dumpSynthFacts(fns []*ssa.Function) {
	path := os.Getenv("CHECKLOCKS_SYNTH_DUMP")
	if path == "" {
		return
	}
	var lines []string
	for _, fn := range fns {
		obj, ok := fn.Object().(*types.Func)
		if !ok {
			continue
		}
		var lff lockFunctionFacts
		if !pc.pass.ImportObjectFact(obj, &lff) {
			continue
		}
		for _, sec := range []struct {
			name string
			m    map[string]functionGuardInfo
		}{{"HE", lff.HeldOnEntry}, {"HX", lff.HeldOnExit}, {"XE", lff.ExcludedOnEntry}} {
			for key, g := range sec.m {
				lines = append(lines, fmt.Sprintf("%s %s %q %s excl=%v", obj.FullName(), sec.name, key, dumpGuard(g.Resolver), g.Exclusive))
			}
		}
		if lff.Ignore {
			lines = append(lines, obj.FullName()+" IGN")
		}
	}
	sort.Strings(lines)
	appendLines(path, lines)
}

func dumpFieldList(fl fieldList) string {
	var b strings.Builder
	for _, e := range fl {
		switch x := e.(type) {
		case *fieldStruct:
			fmt.Fprintf(&b, ".S%d", x.Field)
		case *fieldStructPtr:
			fmt.Fprintf(&b, ".P%d", x.Field)
		default:
			fmt.Fprintf(&b, ".%T", e)
		}
	}
	return b.String()
}

func dumpGuard(g functionGuardResolver) string {
	switch x := g.(type) {
	case *parameterGuard:
		return fmt.Sprintf("param%d%s", x.Index, dumpFieldList(x.FieldList))
	case *returnGuard:
		return fmt.Sprintf("ret%d/%v%s", x.Index, x.NeedsExtract, dumpFieldList(x.FieldList))
	case *globalGuard:
		return fmt.Sprintf("global %s.%s/%v%s", x.PackageName, x.ObjectName, x.Deref, dumpFieldList(x.FieldList))
	}
	return fmt.Sprintf("%T", g)
}

// resolvesToReceiver is the receiver test synthexclude used before the resolver, kept as the
// shadow's reference.
func resolvesToReceiver(v ssa.Value, recv *ssa.Parameter, captured map[*ssa.FreeVar]ssa.Value) bool {
	cur := v
	for i := 0; i < maxCaptureHops; i++ {
		switch x := unwrapAssertValue(cur).(type) {
		case *ssa.Parameter:
			return x == recv
		case *ssa.FreeVar:
			outer, ok := captured[x]
			if !ok {
				return false
			}
			cur = outer
		case *ssa.Alloc:
			stored, ok := singleStore(x)
			if !ok {
				return false
			}
			cur = stored
		default:
			return false
		}
	}
	return false
}
