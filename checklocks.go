// Copyright 2020 The gVisor Authors.
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

// Package checklocks performs lock analysis to identify and flag unprotected
// access to annotated fields.
//
// For detailed usage refer to README.md in the same directory.
//
// Note that this package uses the built-in atomics, in order to avoid the use
// of our own atomic package. This is because our own atomic package depends on
// our own sync package, which includes lock dependency analysis. This in turn
// requires goid, which introduces a dependency cycle. To avoid this, we simply
// use the simpler, built-in sync package.
//
// +checkalignedignore
package checklocks

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

// Analyzer is the main entrypoint.
var Analyzer = &analysis.Analyzer{
	Name:     "checklocks",
	Doc:      "checks lock preconditions on functions and fields",
	Run:      run,
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	FactTypes: []analysis.Fact{
		(*atomicAlignment)(nil),
		(*freshFacts)(nil),
		(*globalAccessorFacts)(nil),
		(*lockGuardFacts)(nil),
		(*lockPrimitiveFacts)(nil),
		(*lockFunctionFacts)(nil),
		(*lockTypeFacts)(nil),
	},
}

var (
	enableInferred = true
	enableAtomic   = true
	enableWrappers = true
)

func init() {
	Analyzer.Flags.BoolVar(&enableInferred, "inferred", true, "enable inferred locks")
	Analyzer.Flags.BoolVar(&enableAtomic, "atomic", true, "enable atomic checks")
	Analyzer.Flags.BoolVar(&enableWrappers, "wrappers", true, "enable analysis of wrappers")
}

// objectObservations tracks lock correlations.
type objectObservations struct {
	counts map[types.Object]int
	total  int
}

// passContext is a pass with additional expected failures.
//
// The embedded expectations carry the self-check annotations, and are shared
// with the other analyzers in this module; see expectations.go.
type passContext struct {
	*expectations
	pass         *analysis.Pass
	closures     map[*ast.FuncLit]*lockFunctionFacts
	functions    map[*ssa.Function]struct{}
	observations map[types.Object]*objectObservations

	// fresh is what the function being checked knows about the objects under
	// construction in it, see fresh.go. It belongs to one function at a time and is
	// saved and restored around the analysis of another.
	fresh *freshState
}

// observationsFor retrieves observations for the given object.
func (pc *passContext) observationsFor(obj types.Object) *objectObservations {
	obj = originObject(obj)
	if pc.observations == nil {
		pc.observations = make(map[types.Object]*objectObservations)
	}
	oo, ok := pc.observations[obj]
	if !ok {
		oo = &objectObservations{
			counts: make(map[types.Object]int),
		}
		pc.observations[obj] = oo
	}
	return oo
}

// forAllGlobals applies the given function to all globals.
func (pc *passContext) forAllGlobals(fn func(ts *ast.ValueSpec, decl *ast.GenDecl)) {
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.VAR {
				continue
			}
			for _, gs := range d.Specs {
				fn(gs.(*ast.ValueSpec), d)
			}
		}
	}
}

// forAllTypes applies the given function over all types.
func (pc *passContext) forAllTypes(fn func(ts *ast.TypeSpec, decl *ast.GenDecl)) {
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.TYPE {
				continue
			}
			for _, gs := range d.Specs {
				fn(gs.(*ast.TypeSpec), d)
			}
		}
	}
}

// forAllFunctions applies the given function over all functions.
func (pc *passContext) forAllFunctions(fn func(fn *ast.FuncDecl)) {
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			fn(d)
		}
	}
}

// run is the main entrypoint.
func run(pass *analysis.Pass) (any, error) {
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, enableWrappers),
		pass:         pass,
		functions:    make(map[*ssa.Function]struct{}),
	}

	// Find all line failure annotations.
	pc.extractLineFailures()

	// Find all struct declarations and export relevant facts.
	pc.forAllGlobals(func(vs *ast.ValueSpec, decl *ast.GenDecl) {
		if ss, ok := vs.Type.(*ast.StructType); ok {
			structType := pc.pass.TypesInfo.TypeOf(vs.Type).Underlying().(*types.Struct)
			docs := []*ast.CommentGroup{vs.Doc}
			if len(decl.Specs) == 1 {
				docs = append(docs, decl.Doc)
			}
			pc.structLockGuardFacts(structType, ss, docs...)
		}
		pc.globalLockGuardFacts(vs, decl)
	})
	pc.forAllTypes(func(ts *ast.TypeSpec, decl *ast.GenDecl) {
		if ss, ok := ts.Type.(*ast.StructType); ok {
			structType := pc.pass.TypesInfo.TypeOf(ts.Name).Underlying().(*types.Struct)
			// A single type declaration attaches its documentation to the
			// declaration rather than to the specification inside it, so a
			// guard declared for the structure is looked for in both.
			docs := []*ast.CommentGroup{ts.Doc}
			if len(decl.Specs) == 1 {
				docs = append(docs, decl.Doc)
			}
			pc.structLockGuardFacts(structType, ss, docs...)
		}
		pc.typeAliasFacts(ts, decl)
		pc.lockPrimitiveTypeFacts(ts, decl)
	})

	// Check all alignments.
	pc.forAllTypes(func(ts *ast.TypeSpec, _ *ast.GenDecl) {
		typ, ok := types.Unalias(pass.TypesInfo.TypeOf(ts.Name)).(*types.Named)
		if !ok {
			return
		}
		pc.checkTypeAlignment(pass.Pkg, typ)
	})

	// Find all function declarations and export relevant facts.
	pc.forAllFunctions(func(fn *ast.FuncDecl) {
		pc.functionFacts(fn)
	})

	// Resolve the annotations on function literals. These are not facts:
	// a literal has no object to attach one to, and is analyzed only where
	// it is written.
	pc.closures = pc.closureAnnotations()

	// Mark the functions that do nothing but return a package-level
	// variable. This must precede the scan below, which resolves calls to
	// them using the facts exported here.
	state := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	for _, fn := range state.SrcFuncs {
		pc.globalAccessorFactsFor(fn)
	}

	// Work out which parameters each function publishes, which is what a caller of it
	// consults to know whether an object it passed is still unreachable afterwards.
	// This is a fixpoint: a function publishes what its callees publish.
	for _, fn := range state.SrcFuncs {
		pc.seedPublishes(fn)
	}
	for round := 0; round < maxPublishRounds; round++ {
		changed := false
		for _, fn := range state.SrcFuncs {
			if pc.computePublishes(fn) {
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Check the declarations that a function returns an unpublished object. A call site
	// trusts the annotation, so it is verified where it is written.
	for _, fn := range state.SrcFuncs {
		pc.checkFreshReturns(fn)
	}

	// Scan all code looking for invalid accesses.
	for _, fn := range state.SrcFuncs {
		// Import function facts generated above.
		//
		// Note that anonymous(closures) functions do not have an
		// object but do show up in the SSA. They can only be invoked
		// by named functions in the package, and they are analyzing
		// inline on every call. Thus we skip the analysis here. They
		// will be hit on calls, or picked up in the pass below.
		obj := fn.Object()
		if obj == nil {
			continue
		}
		// The body of a lock primitive is the implementation of the
		// lock, not a critical section: it takes a lock and does not
		// release it, which is exactly what it is for. Analyzing it
		// reports a balance error against the wrapper's author, and
		// silencing that with an ignore would suppress the call site
		// checks for every user of the lock.
		if pc.isLockPrimitiveMethod(obj.(*types.Func)) {
			continue
		}

		var lff lockFunctionFacts
		pc.importLockFunctionFacts(obj.(*types.Func), &lff)

		// Check the basic blocks in the function.
		pc.checkFunction(nil, fn, &lff, nil, false /* force */)
	}
	for _, fn := range state.SrcFuncs {
		// Ensure all anonymous functions are hit. They are not
		// permitted to have any lock preconditions.
		if obj := fn.Object(); obj != nil {
			continue
		}
		// An annotated literal is analyzed with the state its
		// annotation describes, which is what lets a callback invoked
		// by machinery this analysis cannot follow state the lock its
		// caller holds.
		lff := &lockFunctionFacts{}
		if annotated, ok := pc.closureFactsFor(fn.Syntax()); ok {
			lff = annotated
		}
		pc.checkFunction(nil, fn, lff, nil, false /* force */)
	}

	// Check for inferred checklocks annotations.
	if enableInferred {
		pc.checkInferred()
	}

	// Check for expected failures.
	pc.checkFailures()

	return nil, nil
}
