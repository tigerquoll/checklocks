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
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// engineView returns what the checklocks engine recorded for this package, or nil.
func (pc *lockOrderContext) engineView() *engineView {
	res, _ := pc.pass.ResultOf[Analyzer].(*ownerResult)
	if res == nil {
		return nil
	}
	return res.engine
}

// drive replaces the classes held at inst with the engine's, where it recorded the site.
//
// The engine's count of a class excludes assumed locks. The locks only this analyzer recognises
// are added back from cs.own, since the engine has no record of them.
func (pc *lockOrderContext) drive(fn *ssa.Function, inst ssa.Instruction, cs *classSet) {
	view := pc.engineView()
	if view == nil || fn.Parent() != nil {
		return
	}
	held, ok := view.heldAt(inst)
	if !ok {
		return
	}
	counts := make(map[string]int, len(held)+len(cs.own))
	for _, l := range held {
		if l.assumed {
			continue
		}
		if class, _ := pc.engineClass(l.path); class != "" {
			counts[class]++
		}
	}
	for class, n := range cs.own {
		if n > 0 {
			counts[class] += n
		}
	}
	cs.counts = counts
}

// engineEntryClasses returns the classes of the locks the engine entered fn with.
func (pc *lockOrderContext) engineEntryClasses(fn *ssa.Function) []string {
	view := pc.engineView()
	if view == nil {
		return nil
	}
	held, ok := view.entryOf(fn)
	if !ok {
		return nil
	}
	var out []string
	for _, l := range held {
		if l.assumed {
			continue
		}
		if class, _ := pc.engineClass(l.path); class != "" {
			out = append(out, class)
		}
	}
	return out
}

// pathOwner walks the types along a lock path to the struct that owns its last field step,
// and returns that owner, the field, and the type of the lock itself.
func pathOwner(p lockPath) (owner *types.Named, field string, final types.Type) {
	if p.root.value == nil {
		return nil, "", nil
	}
	deref := func(t types.Type) types.Type {
		if ptr, ok := types.Unalias(t).Underlying().(*types.Pointer); ok {
			return ptr.Elem()
		}
		return t
	}
	t := p.root.value.Type()
	for i, s := range p.steps {
		switch s.kind {
		case stepDeref:
			t = deref(t)
		case stepField, stepFieldAddr:
			base := t
			if s.kind == stepFieldAddr {
				base = deref(t)
			}
			st, ok := types.Unalias(base).Underlying().(*types.Struct)
			if !ok || s.n >= st.NumFields() {
				return nil, "", nil
			}
			owner, _ = types.Unalias(base).(*types.Named)
			field = st.Field(s.n).Name()
			t = st.Field(s.n).Type()
			if s.kind == stepFieldAddr {
				t = types.NewPointer(t)
			}
		case stepIndexAddr:
			switch x := deref(t).Underlying().(type) {
			case *types.Array:
				t = types.NewPointer(x.Elem())
			case *types.Slice:
				t = types.NewPointer(x.Elem())
			default:
				return nil, "", nil
			}
		case stepIndex, stepConstIndex:
			if tup, ok := t.(*types.Tuple); ok && s.n < tup.Len() {
				t = tup.At(s.n).Type()
				continue
			}
			switch x := types.Unalias(t).Underlying().(type) {
			case *types.Array:
				t = x.Elem()
			case *types.Slice:
				t = x.Elem()
			case *types.Map:
				t = x.Elem()
			default:
				return nil, "", nil
			}
		case stepLookup:
			m, ok := types.Unalias(t).Underlying().(*types.Map)
			if !ok {
				return nil, "", nil
			}
			t = m.Elem()
			// A comma-ok lookup is followed by an extract of its (element, ok) tuple.
			if i+1 < len(p.steps) && p.steps[i+1].kind == stepConstIndex {
				t = types.NewTuple(types.NewVar(token.NoPos, nil, "", t), types.NewVar(token.NoPos, nil, "", types.Typ[types.Bool]))
			}
		}
	}
	return owner, field, t
}

// engineClass maps an engine lock to a class by the field rule 'F', else the type rule 'T', else 'U' for none.
func (pc *lockOrderContext) engineClass(p lockPath) (string, byte) {
	owner, field, final := pathOwner(p)
	if owner != nil {
		if c := pc.classForNamed(owner, field); c != "" {
			return c, 'F'
		}
	}
	if final != nil {
		if named, ok := namedOf(final); ok {
			if c := pc.classForNamed(named, ""); c != "" {
				return c, 'T'
			}
		}
	}
	return "", 'U'
}
