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
	"go/types"

	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

// globalAccessorFacts marks a function that returns a package-level variable
// and nothing else.
//
// The result of calling such a function is the variable, so an annotation
// naming the variable and an acquisition made through the accessor describe
// the same lock. Without this they resolve to different values and never
// unify: the annotation to the variable, the acquisition to an opaque call
// result.
type globalAccessorFacts struct {
	// ObjectName is the variable returned.
	ObjectName string

	// PackageName is the package where the variable lives.
	PackageName string

	// Deref indicates that the accessor returns the value held by the
	// variable rather than its address, so resolution must load it. See
	// makeDerefResolvedValue.
	Deref bool
}

// AFact implements analysis.Fact.AFact.
func (*globalAccessorFacts) AFact() {}

// returnedGlobal returns the package-level variable that fn returns, if fn
// does nothing but return one.
//
// The function must have exactly one return of exactly one value, and that
// value must be a package-level variable or a load of one. Anything else,
// including a second return, disqualifies it: the point is that the result is
// always the same variable.
//
// Note that statements preceding the return are not examined. The shape this
// exists for is a lazily initialised singleton, whose accessor runs an
// initialiser before returning the variable, and which returns the same
// variable either way.
func returnedGlobal(fn *ssa.Function) (*ssa.Global, bool, bool) {
	var ret *ssa.Return
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			r, ok := inst.(*ssa.Return)
			if !ok {
				continue
			}
			if ret != nil {
				// More than one return: the result is not
				// always the same variable.
				return nil, false, false
			}
			ret = r
		}
	}
	if ret == nil || len(ret.Results) != 1 {
		return nil, false, false
	}
	switch v := unwrapConversions(ret.Results[0]).(type) {
	case *ssa.Global:
		// The address of the variable is returned.
		return v, false, true
	case *ssa.UnOp:
		// The value held by the variable is returned.
		if v.Op != token.MUL {
			return nil, false, false
		}
		if g, ok := unwrapConversions(v.X).(*ssa.Global); ok {
			return g, true, true
		}
	}
	return nil, false, false
}

// unwrapConversions strips conversions that do not change the value.
func unwrapConversions(v ssa.Value) ssa.Value {
	for {
		switch x := v.(type) {
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		default:
			return v
		}
	}
}

// globalAccessorFactsFor exports the accessor fact for fn, if it applies.
func (pc *passContext) globalAccessorFactsFor(fn *ssa.Function) {
	obj := fn.Object()
	if obj == nil {
		return
	}
	funcObj, ok := obj.(*types.Func)
	if !ok {
		return
	}
	g, deref, ok := returnedGlobal(fn)
	if !ok || g.Pkg == nil || g.Object() == nil {
		return
	}
	pc.pass.ExportObjectFact(funcObj, &globalAccessorFacts{
		ObjectName:  g.Name(),
		PackageName: g.Pkg.Pkg.Path(),
		Deref:       deref,
	})
}

// substituteGlobalAccessor resolves the result of a call to a global accessor
// to the variable it returns.
//
// The substitution is recorded in the lock state, so every later use of the
// call's result resolves to the variable: an acquisition made through the
// accessor then matches an annotation that names the variable directly.
func (pc *passContext) substituteGlobalAccessor(call *ssa.Call, ls *lockState) {
	callee := call.Common().StaticCallee()
	if callee == nil {
		return
	}
	obj := callee.Object()
	if obj == nil {
		return
	}
	funcObj, ok := obj.(*types.Func)
	if !ok {
		return
	}
	var gaf globalAccessorFacts
	if !pc.pass.ImportObjectFact(funcObj, &gaf) {
		return
	}

	// Resolve the variable in this pass, exactly as a global guard does.
	// It may not exist here: export data does not carry unexported
	// package-level variables, so an unexported one cannot be resolved
	// outside its own package, and the substitution is simply not made.
	state := pc.pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	pkg := state.Pkg
	if gaf.PackageName != "" && gaf.PackageName != state.Pkg.Pkg.Path() {
		pkg = state.Pkg.Prog.ImportedPackage(gaf.PackageName)
	}
	if pkg == nil {
		return
	}
	v, ok := pkg.Members[gaf.ObjectName].(ssa.Value)
	if !ok {
		return
	}

	rv := makeResolvedValue(v, nil)
	if gaf.Deref {
		rv = makeDerefResolvedValue(v, nil)
	}
	key, keyObj := rv.valueAndObject(ls)
	ls.substitute(call, key, keyObj)
}
