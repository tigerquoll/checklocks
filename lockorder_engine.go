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
	"maps"
	"slices"

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

// engineEntry returns the engine's entry locks of fn whose guard this analysis classes, and the
// class it entered each under.
func (pc *lockOrderContext) engineEntry(fn *ssa.Function) map[pathKey]string {
	if own, ok := pc.entries[fn]; ok {
		return own
	}
	own := make(map[pathKey]string)
	if pc.entries == nil {
		pc.entries = make(map[*ssa.Function]map[pathKey]string)
	}
	pc.entries[fn] = own
	obj := funcObject(fn)
	if obj == nil || fn.Parent() != nil {
		return own
	}
	held, ok := pc.engineView().entryOf(fn)
	if !ok {
		return own
	}
	for _, l := range held {
		for _, g := range l.guards {
			if class := pc.classOfGuard(obj, g); class != "" {
				own[l.path.Key()] = class
				break
			}
		}
	}
	return own
}

// engineKeyClass returns the class of an engine lock: the class lockorder entered it under, else
// the projection of its path.
func (pc *lockOrderContext) engineKeyClass(fn *ssa.Function, key pathKey, path lockPath) string {
	if class := pc.engineEntry(fn)[key]; class != "" {
		return class
	}
	class, _ := pc.engineClass(path)
	return class
}

// acquiredClass returns the class a call acquires as a lock operation visitCall recognises, or "".
func (pc *lockOrderContext) acquiredClass(call ssa.CallInstruction) string {
	obj := calledFunc(staticCallee(call))
	if obj == nil || lockOpName(obj.Name()) != opAcquire {
		return ""
	}
	if recv, ok := receiverOf(call); ok {
		return pc.classOf(recv)
	}
	return ""
}

// labels returns, for each engine lock a call of fn or of a literal inside it took, the classes lockorder
// acquired at those calls.
func (pc *lockOrderContext) labels(fn *ssa.Function) map[pathKey]map[string]struct{} {
	if own, ok := pc.labelSets[fn]; ok {
		return own
	}
	own := make(map[pathKey]map[string]struct{})
	if pc.labelSets == nil {
		pc.labelSets = make(map[*ssa.Function]map[pathKey]map[string]struct{})
	}
	pc.labelSets[fn] = own
	view := pc.engineView()
	fns := []*ssa.Function{fn}
	for i := 0; i < len(fns); i++ {
		fns = append(fns, fns[i].AnonFuncs...)
		for _, b := range fns[i].Blocks {
			for _, inst := range b.Instrs {
				call, ok := inst.(ssa.CallInstruction)
				if !ok {
					continue
				}
				class := pc.acquiredClass(call)
				if class == "" {
					continue
				}
				for _, gained := range []map[pathKey]lockPath{view.gainsOf(call), view.opGainsOf(call)} {
					for k := range gained {
						if own[k] == nil {
							own[k] = make(map[string]struct{})
						}
						own[k][class] = struct{}{}
					}
				}
			}
		}
	}
	return own
}

// engineClasses returns the classes of a held engine lock and the rule that found them: 'e' for
// the class lockorder entered it under, 'l' for the classes of the acquires that took it, else
// the projection's rule. A forced lock is only projected.
func (pc *lockOrderContext) engineClasses(fn *ssa.Function, l heldLock) ([]string, byte) {
	if !l.assumed {
		k := l.path.Key()
		if class := pc.engineEntry(fn)[k]; class != "" {
			return []string{class}, 'e'
		}
		if set := pc.labels(fn)[k]; len(set) > 0 {
			return slices.Sorted(maps.Keys(set)), 'l'
		}
	}
	if class, rule := pc.engineClass(l.path); class != "" {
		return []string{class}, rule
	}
	return nil, 'U'
}

// applyEffect adds the locks the annotations of a call this analysis did not treat as a lock
// operation acquired, and takes back its own earlier additions the call released. It never
// lowers a count below this analysis's own.
func (pc *lockOrderContext) applyEffect(fn *ssa.Function, call ssa.CallInstruction, cs *classSet) {
	view := pc.engineView()
	if view == nil || fn.Parent() != nil {
		return
	}
	gains := view.gainsOf(call)
	for k := range view.releasesOf(call) {
		if _, gained := gains[k]; gained {
			continue
		}
		if class, counted := cs.eng[k]; counted {
			cs.undo(class)
			delete(cs.eng, k)
		}
	}
	for k, p := range gains {
		if class := pc.engineKeyClass(fn, k, p); class != "" {
			cs.gain(class)
			cs.eng[k] = class
		}
	}
}

// entryOverlay sets cs.site to the classes of the entry locks only the engine has, as it still
// held them at inst.
func (pc *lockOrderContext) entryOverlay(fn *ssa.Function, inst ssa.Instruction, cs *classSet) {
	view := pc.engineView()
	if view == nil || fn.Parent() != nil {
		return
	}
	held, ok := view.heldSet(inst)
	if !ok {
		cs.site = nil
		return
	}
	var site map[string]int
	for k, l := range held {
		if l.assumed || !l.entry {
			continue
		}
		if _, counted := cs.eng[k]; counted {
			continue
		}
		if class := pc.engineKeyClass(fn, k, l.path); class != "" {
			if site == nil {
				site = make(map[string]int)
			}
			site[class]++
		}
	}
	cs.site = site
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
	// A free variable holds the captured variable's address; for a bound wrapper's receiver the dereference names the same type.
	if p.root.kind == pathRootFreeVar {
		t = deref(t)
	}
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
