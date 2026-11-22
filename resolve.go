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
	"go/token"

	"golang.org/x/tools/go/ssa"
)

// resolver answers where a value comes from without walking a body: it reads a
// state seeded from what the ssa builder introduced, never the walk's own.
// A closure capturing the receiver has it spilled to a local, and a closure
// receives it as a free variable; both are the receiver itself.
type resolver struct {
	states map[*ssa.Function]*lockState

	shadow shadowCounts
}

func newResolver() *resolver {
	return &resolver{states: make(map[*ssa.Function]*lockState)}
}

// state returns fn's static state. A local is seeded only when exactly one
// value is ever stored into it, so a variable that holds different objects at
// different times resolves to nothing rather than to the wrong one. A free
// variable is bound to the outermost value its chain of closures captured.
func (r *resolver) state(fn *ssa.Function) *lockState {
	if ls, ok := r.states[fn]; ok {
		return ls
	}
	ls := newLockState()
	var chain []*ssa.Function
	for f := fn; f != nil; f = f.Parent() {
		chain = append(chain, f)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		f := chain[i]
		for _, block := range f.Blocks {
			for _, inst := range block.Instrs {
				if alloc, ok := inst.(*ssa.Alloc); ok {
					if v, ok := singleStore(alloc); ok {
						ls.store(alloc, v)
					}
				}
			}
		}
		if parent := f.Parent(); parent != nil {
			bindFreeVars(ls, parent, f)
		}
	}
	r.states[fn] = ls
	return ls
}

// bindFreeVars binds the free variables of anon to the values parent's
// MakeClosure passed.
func bindFreeVars(ls *lockState, parent, anon *ssa.Function) {
	for _, block := range parent.Blocks {
		for _, inst := range block.Instrs {
			mc, ok := inst.(*ssa.MakeClosure)
			if !ok || mc.Fn != ssa.Value(anon) {
				continue
			}
			for i, fv := range anon.FreeVars {
				if i >= len(mc.Bindings) {
					break
				}
				b := mc.Bindings[i]
				if outer, ok := b.(*ssa.FreeVar); ok {
					if bound, ok := ls.stored[outer]; ok {
						b = bound
					}
				}
				ls.store(fv, b)
			}
		}
	}
}

// path returns the lock path of v under its function's static state, without
// the conversions around it.
func (r *resolver) path(v ssa.Value) lockPath {
	for {
		switch x := v.(type) {
		case *ssa.ChangeType:
			v = x.X
			continue
		case *ssa.Convert:
			v = x.X
			continue
		case *ssa.MakeInterface:
			v = x.X
			continue
		}
		break
	}
	p, _ := r.state(v.Parent()).valueAndObject(v)
	return p
}

// maxCaptureHops bounds the walk back to the receiver.
const maxCaptureHops = 16

// isReceiver reports whether v is the receiver or, when derefs is set, memory
// reached from it by loads alone.
func (r *resolver) isReceiver(v ssa.Value, recv *ssa.Parameter, derefs bool) bool {
	p := r.path(v)
	for _, step := range p.steps {
		if !derefs || step.kind != stepDeref {
			return false
		}
	}
	ls := r.state(v.Parent())
	cur := p.root.value
	for i := 0; i < maxCaptureHops && cur != nil; i++ {
		switch x := cur.(type) {
		case *ssa.Parameter:
			return x == recv
		case *ssa.FreeVar:
			bound, ok := ls.stored[x]
			if !ok {
				return false
			}
			cur = bound
		case *ssa.Alloc:
			stored, ok := singleStore(x)
			if !ok {
				return false
			}
			cur = stored
		case *ssa.ChangeType:
			cur = x.X
		case *ssa.Convert:
			cur = x.X
		case *ssa.MakeInterface:
			cur = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return false
			}
			cur = x.X
		default:
			return false
		}
	}
	return false
}

// singleStore returns the only value ever stored into a local.
func singleStore(alloc *ssa.Alloc) (ssa.Value, bool) {
	refs := alloc.Referrers()
	if refs == nil {
		return nil, false
	}
	var found ssa.Value
	for _, inst := range *refs {
		st, ok := inst.(*ssa.Store)
		if !ok || st.Addr != ssa.Value(alloc) {
			continue
		}
		if found != nil {
			// More than one value passes through it; which one is
			// live here is not something this can answer.
			return nil, false
		}
		found = st.Val
	}
	return found, found != nil
}
