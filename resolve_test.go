// Copyright 2026 The gVisor Authors.
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
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

const resolveSrc = `package probe

type T struct{ n int }

func (s *T) spilled() *int {
	func() { _ = s }()
	return &s.n
}

func (s *T) nested() {
	func() {
		func() { _ = s.n }()
	}()
}
`

// fieldAddrOf returns the first field address in fn.
func fieldAddrOf(t *testing.T, fn *ssa.Function) *ssa.FieldAddr {
	t.Helper()
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			if fa, ok := inst.(*ssa.FieldAddr); ok {
				return fa
			}
		}
	}
	t.Fatalf("%s has no field address", fn)
	return nil
}

func TestResolverRootsAtReceiver(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe/x.go", resolveSrc, 0)
	if err != nil {
		t.Fatalf("parsing probe: %v", err)
	}
	pkg, _, err := ssautil.BuildPackage(&types.Config{Importer: importer.Default()}, fset,
		types.NewPackage("probe", "probe"), []*ast.File{f}, ssa.BuilderMode(0))
	if err != nil {
		t.Fatalf("building probe: %v", err)
	}
	named := pkg.Pkg.Scope().Lookup("T").Type()
	for _, tc := range []struct {
		method string
		site   func(*ssa.Function) *ssa.Function
	}{
		{"spilled", func(fn *ssa.Function) *ssa.Function { return fn }},
		{"nested", func(fn *ssa.Function) *ssa.Function { return fn.AnonFuncs[0].AnonFuncs[0] }},
	} {
		t.Run(tc.method, func(t *testing.T) {
			sel := types.NewMethodSet(types.NewPointer(named)).Lookup(pkg.Pkg, tc.method)
			fn := pkg.Prog.MethodValue(sel)
			recv := fn.Params[0]
			fa := fieldAddrOf(t, tc.site(fn))
			r := newResolver()
			p := r.path(fa.X)
			if p.root.kind != pathRootParam || p.root.value != ssa.Value(recv) || len(p.steps) != 0 {
				t.Errorf("path(%s) = %s, want the receiver parameter alone", fa.X.Name(), p)
			}
			if !r.isReceiver(fa.X, recv, false) {
				t.Errorf("isReceiver(%s) is false", fa.X.Name())
			}
		})
	}
}
