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

	"golang.org/x/tools/go/ssa"
)

// slotInfo is the stored-slot pre-pass of one function.
type slotInfo struct {
	// loaded holds the addresses a lock-relevant resolution in the function's walk may read.
	loaded map[ssa.Value]bool

	// params holds the parameters such a resolution reaches, which an inline caller binds.
	params map[*ssa.Parameter]bool

	// stores holds the addresses the function stores to.
	stores map[ssa.Value][]ssa.Value

	// slots are the stored addresses the function loads; nil until relevantSlots asks.
	slots map[ssa.Value]bool
}

// relevantSlots returns the slots of fn whose stored values a lock resolution may load,
// computed once from fn's code and facts, never from walk state. It resolves no values:
// it follows the operands valueAndObject follows.
func (pc *passContext) relevantSlots(fn *ssa.Function) *slotInfo {
	si := pc.slotPass(fn)
	if si == nil || si.slots != nil {
		return si
	}
	si.slots = map[ssa.Value]bool{}
	for addr := range si.stores {
		if si.loaded[addr] {
			si.slots[addr] = true
		}
	}
	return si
}

// slotPass returns the pre-pass of fn, run over its family (the top-level function enclosing
// fn and every literal within it) until no member loads a new global, so a global any member
// loads is a slot of every member that stores it.
func (pc *passContext) slotPass(fn *ssa.Function) *slotInfo {
	if fn == nil {
		return nil
	}
	if si, ok := pc.slotInfos[fn]; ok {
		return si
	}
	root := fn
	for root.Parent() != nil {
		root = root.Parent()
	}
	members := []*ssa.Function{fn}
	for work := []*ssa.Function{root}; len(work) > 0; {
		f := work[len(work)-1]
		work = append(work[:len(work)-1], f.AnonFuncs...)
		members = append(members, f)
	}
	seed := map[*ssa.Global]bool{}
	for {
		fam := map[*ssa.Function]*slotInfo{}
		for _, m := range members {
			pc.prePass(m, seed, fam)
		}
		n := len(seed)
		for _, si := range fam {
			for addr := range si.loaded {
				if g, ok := addr.(*ssa.Global); ok {
					seed[g] = true
				}
			}
		}
		if len(seed) == n {
			if pc.slotInfos == nil {
				pc.slotInfos = map[*ssa.Function]*slotInfo{}
			}
			maps.Copy(pc.slotInfos, fam)
			return fam[fn]
		}
	}
}

// prePass computes the loaded addresses and parameters of fn, loading the seed globals it stores.
func (pc *passContext) prePass(fn *ssa.Function, seed map[*ssa.Global]bool, fam map[*ssa.Function]*slotInfo) *slotInfo {
	if si, ok := fam[fn]; ok {
		return si
	}
	si := &slotInfo{loaded: map[ssa.Value]bool{}, params: map[*ssa.Parameter]bool{}}
	fam[fn] = si
	sub := func(f *ssa.Function) *slotInfo { return pc.prePass(f, seed, fam) }

	stores := map[ssa.Value][]ssa.Value{}
	si.stores = stores
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			if st, ok := inst.(*ssa.Store); ok {
				stores[st.Addr] = append(stores[st.Addr], st.Val)
			}
		}
	}

	seen := map[ssa.Value]bool{}
	var visit func(v ssa.Value)
	load := func(addr ssa.Value) {
		si.loaded[addr] = true
		for _, val := range stores[addr] {
			if _, ok := addr.(*ssa.FreeVar); ok {
				si.loaded[val] = true // a free variable resolves through stored[stored[fv]]
			}
			visit(val)
		}
	}
	visit = func(v ssa.Value) {
		if v == nil || seen[v] {
			return
		}
		seen[v] = true
		switch x := v.(type) {
		case *ssa.Parameter:
			si.params[x] = true
		case *ssa.FreeVar:
			load(x)
		case *ssa.Convert:
			visit(x.X)
		case *ssa.ChangeType:
			visit(x.X)
		case *ssa.UnOp:
			if x.Op == token.MUL {
				load(x.X)
				visit(x.X)
			}
		case *ssa.Field:
			visit(x.X)
		case *ssa.FieldAddr:
			visit(x.X)
		case *ssa.Index:
			visit(x.X)
			visit(x.Index)
		case *ssa.IndexAddr:
			visit(x.X)
			visit(x.Index)
		case *ssa.Lookup:
			visit(x.X)
			visit(x.Index)
		case *ssa.Extract:
			visit(x.Tuple)
		}
	}

	var own lockFunctionFacts
	if tf, ok := fn.Object().(*types.Func); ok {
		pc.importLockFunctionFacts(tf, &own)
	} else if annotated, ok := pc.closureFactsFor(fn.Syntax()); ok {
		own = *annotated
	}
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			switch x := inst.(type) {
			case *ssa.Field:
				pc.accessRoot(x.X, x.Field, visit)
			case *ssa.FieldAddr:
				pc.accessRoot(x.X, x.Field, visit)
			case ssa.CallInstruction:
				pc.callRoots(x, visit, load, sub)
			case *ssa.Return:
				for _, g := range own.HeldOnExit {
					if rg, ok := g.Resolver.(*returnGuard); ok && rg.Index < len(x.Results) {
						visit(x.Results[rg.Index])
					}
				}
			}
		}
	}
	for addr := range stores {
		if g, ok := addr.(*ssa.Global); ok && seed[g] {
			load(g)
		}
	}
	return si
}

// accessRoot visits the base of a field access whose guards or aliases resolve through it.
func (pc *passContext) accessRoot(base ssa.Value, field int, visit func(ssa.Value)) {
	if freshAlloc(base) {
		return
	}
	if obj, ok := findField(base.Type(), field); ok {
		var lgf lockGuardFacts
		pc.importLockGuardFacts(obj, &lgf)
		if len(lgf.GuardedBy) != 0 {
			visit(base)
		}
	}
	if pc.hasAliasFacts(base) {
		visit(base)
	}
}

// visitGuardArgs visits the arguments the parameter guards of lff name.
func visitGuardArgs(lff *lockFunctionFacts, args []ssa.Value, visit func(ssa.Value)) {
	for _, guards := range []map[string]functionGuardInfo{lff.HeldOnEntry, lff.HeldOnExit, lff.ExcludedOnEntry} {
		for _, g := range guards {
			if pg, ok := g.Resolver.(*parameterGuard); ok && pg.Index < len(args) {
				visit(args[pg.Index])
			}
		}
	}
}

// visitParamArgs visits the arguments bound to the parameters of fn that its own pre-pass reaches.
func visitParamArgs(si *slotInfo, fn *ssa.Function, args []ssa.Value, visit func(ssa.Value)) {
	for i, arg := range args {
		if i < len(fn.Params) && si.params[fn.Params[i]] {
			visit(arg)
		}
	}
}

// callRoots visits the operands of a call, go or defer whose checking resolves them.
func (pc *passContext) callRoots(call ssa.CallInstruction, visit, load func(ssa.Value), sub func(*ssa.Function) *slotInfo) {
	common := call.Common()
	args := common.Args
	aliasArgs := func() {
		for _, arg := range args {
			if pc.hasAliasFacts(arg) {
				visit(arg)
			}
		}
	}
	for _, arg := range args {
		mc, ok := arg.(*ssa.MakeClosure)
		if !ok {
			continue
		}
		clfn := mc.Fn.(*ssa.Function)
		if !isRangeBody(clfn) {
			continue
		}
		for i, fv := range clfn.FreeVars {
			if sub(clfn).loaded[fv] || pc.hasAliasFacts(fv) {
				load(mc.Bindings[i])
				visit(mc.Bindings[i])
			}
		}
	}
	var tf *types.Func
	if common.Method != nil {
		tf = common.Method
		args = append([]ssa.Value{common.Value}, args...)
	} else {
		switch fn := common.Value.(type) {
		case *ssa.Function:
			if fn.Object() == nil {
				visitParamArgs(sub(fn), fn, args, visit)
				aliasArgs()
				if _, isGo := call.(*ssa.Go); isGo {
					if annotated, ok := pc.closureFactsFor(fn.Syntax()); ok {
						visitGuardArgs(annotated, args, visit)
					}
				}
				return
			}
			var ok bool
			if tf, ok = fn.Object().(*types.Func); !ok {
				return
			}
		case *ssa.MakeClosure:
			clfn := fn.Fn.(*ssa.Function)
			for i, fv := range clfn.FreeVars {
				if sub(clfn).loaded[fv] || pc.hasAliasFacts(fv) {
					load(fn.Bindings[i])
					visit(fn.Bindings[i])
				}
			}
			visitParamArgs(sub(clfn), clfn, args, visit)
			aliasArgs()
			return
		default:
			return
		}
	}

	var lff lockFunctionFacts
	pc.importLockFunctionFacts(tf, &lff)
	if _, _, recv, ok := lockCallSite(pc.pass, common); ok {
		visit(recv)
	}
	visitGuardArgs(&lff, args, visit)
	aliasArgs()
}
