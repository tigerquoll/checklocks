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

type T struct {
	n  int
	in struct{ n int }
}

func (s *T) spilled() *int {
	func() { _ = s }()
	return &s.n
}

func (s *T) nested() {
	func() {
		func() { _ = s.n }()
	}()
}

func (s *T) field() { _ = s.in.n }

func sink(any) {}

func (s T) copies() {
	c1 := s; c2 := c1; c3 := c2; c4 := c3; c5 := c4; c6 := c5; c7 := c6; c8 := c7; c9 := c8
	sink(&c1);
	sink(&c2);
	sink(&c3);
	sink(&c4);
	sink(&c5);
	sink(&c6);
	sink(&c7);
	sink(&c8)
	_ = c9.n
}
`

func fieldAddrOf(t *testing.T, fn *ssa.Function) *ssa.FieldAddr {
	t.Helper()
	var last *ssa.FieldAddr
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			if fa, ok := inst.(*ssa.FieldAddr); ok {
				last = fa
			}
		}
	}
	if last == nil {
		t.Fatalf("%s has no field address", fn)
	}
	return last
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
		value  bool // look the method up on the value method set
		rooted bool // path is the receiver parameter alone
		want   bool
	}{
		{"spilled", func(fn *ssa.Function) *ssa.Function { return fn }, false, true, true},
		{"nested", func(fn *ssa.Function) *ssa.Function { return fn.AnonFuncs[0].AnonFuncs[0] }, false, true, true},
		{"copies", func(fn *ssa.Function) *ssa.Function { return fn }, true, false, true},
		{"field", func(fn *ssa.Function) *ssa.Function { return fn }, false, false, false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			var recvType types.Type = types.NewPointer(named)
			if tc.value {
				recvType = named
			}
			sel := types.NewMethodSet(recvType).Lookup(pkg.Pkg, tc.method)
			fn := pkg.Prog.MethodValue(sel)
			recv := fn.Params[0]
			fa := fieldAddrOf(t, tc.site(fn))
			r := newResolver()
			if tc.rooted {
				p := r.path(fa.X)
				if p.root.kind != pathRootParam || p.root.value != ssa.Value(recv) || len(p.steps) != 0 {
					t.Errorf("path(%s) = %s, want the receiver parameter alone", fa.X.Name(), p)
				}
			}
			if got := r.isReceiver(fa.X, recv); got != tc.want {
				t.Errorf("isReceiver(%s) = %v, want %v", fa.X.Name(), got, tc.want)
			}
		})
	}
}
