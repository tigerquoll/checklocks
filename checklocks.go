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
	"cmp"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"runtime/debug"
	"slices"
	"strings"

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

	// collect holds the reports back until flushReports emits them, and pending is
	// what has been held. Only checklocks' own run collects; see maybeFail.
	collect bool
	pending []pendingReport

	// counted names the observations already recorded for the inferred hints, so
	// that a site the walk reaches again adds nothing; see recordObservation.
	counted map[observationKey]struct{}

	// emit says where reports and observations go; see emitMode.
	emit emitMode

	// records holds the blocks the walk entered, for the replay, and current the one
	// being walked or replayed.
	records []*blockRecord
	current *blockRecord

	// panicked is set when contained recovered a panic; observations are then partial.
	panicked bool

	// decisions is what the tagged build keeps about check decisions; see decide.
	decisions decisionState
}

// emitMode says what maybeFail and recordObservation do with what they are given.
type emitMode int

const (
	// emitDirect reports at once, or holds the report for flushReports.
	emitDirect emitMode = iota
	// emitWalk drops the report: the replay raises it.
	emitWalk
	// emitReplay reports as emitDirect does; checkFunction returns at once in it, so
	// the replay never re-enters a nested walk.
	emitReplay
)

// decisionKind names what a check decides.
type decisionKind string

const (
	guardResolve       decisionKind = "guardResolve"
	guardHeld          decisionKind = "guardHeld"
	atomicAccess       decisionKind = "atomicAccess"
	atomicWrite        decisionKind = "atomicWrite"
	goHeld             decisionKind = "goHeld"
	goRelease          decisionKind = "goRelease"
	effectResolve      decisionKind = "effectResolve"
	release            decisionKind = "release"
	acquire            decisionKind = "acquire"
	acquireDiscarded   decisionKind = "acquireDiscarded"
	callExcluded       decisionKind = "callExcluded"
	callHeld           decisionKind = "callHeld"
	freshArg           decisionKind = "freshArg"
	lockOpLock         decisionKind = "lockOpLock"
	lockOpUnlock       decisionKind = "lockOpUnlock"
	lockOpDowngrade    decisionKind = "lockOpDowngrade"
	returnResolve      decisionKind = "returnResolve"
	returnHeld         decisionKind = "returnHeld"
	returnCount        decisionKind = "returnCount"
	entryResolve       decisionKind = "entryResolve"
	entryDuplicate     decisionKind = "entryDuplicate"
	incompatibleReturn decisionKind = "incompatibleReturn"
	loopOverflow       decisionKind = "loopOverflow"
)

// decisionAt says where a check was made: the instruction checked or, for a check of a
// whole function, the function, a label and an index; the position it reports at; and its context.
type decisionAt struct {
	inst    ssa.Instruction
	fn      *ssa.Function
	label   string
	index   int
	pos     token.Pos
	context string
}

// contained runs the check of one top-level function; only the top-level loops call it.
// A panic in it fails that function alone, with the walk state reset; with recoverPanics
// off the panic propagates.
func (pc *passContext) contained(fn *ssa.Function, check func()) {
	if !recoverPanics {
		check()
		return
	}
	done := false
	defer func() {
		r := recover()
		if done {
			return
		}
		pc.panicked = true
		pc.current, pc.emit, pc.records = nil, emitDirect, nil
		// Loop B must not analyze the closures of a failed function out of context.
		for work := []*ssa.Function{fn}; len(work) > 0; {
			f := work[len(work)-1]
			work = append(work[:len(work)-1], f.AnonFuncs...)
			pc.functions[f] = struct{}{}
		}
		fmt.Fprintf(os.Stderr, "checklocks: analysis of %s panicked: %v\n%s", fn, r, debug.Stack())
		pc.pass.Report(analysis.Diagnostic{
			Pos:     fn.Pos(),
			Message: fmt.Sprintf("analysis of %s failed with an internal error; its diagnostics may be incomplete", fn),
		})
	}()
	check()
	done = true
}

// pendingReport is a report held back by the collector.
type pendingReport struct {
	pos token.Pos
	msg string
}

// maybeFail collects a potential failure where this pass collects, and otherwise
// reports it immediately as the embedded expectations do.
func (pc *passContext) maybeFail(pos token.Pos, fmtStr string, args ...any) {
	pc.parityFinding(pos, fmtStr, args...)
	if pc.emit == emitWalk {
		return
	}
	if !pc.collect {
		pc.expectations.maybeFail(pos, fmtStr, args...)
		return
	}
	pc.pending = append(pc.pending, pendingReport{pos: pos, msg: fmt.Sprintf(fmtStr, args...)})
}

// flushReports emits the collected reports. The walk reaches a site once per path, in
// block-graph order, so exact repeats are dropped and the rest sorted by position.
func (pc *passContext) flushReports() {
	pending := pc.pending
	pc.pending = nil
	slices.SortStableFunc(pending, func(a, b pendingReport) int {
		return cmp.Or(comparePos(pc.pass.Fset, a.pos, b.pos), strings.Compare(a.msg, b.msg))
	})
	for _, r := range slices.Compact(pending) {
		if msg, ok := pc.consider(r.pos, "%s", r.msg); ok {
			pc.pass.Report(analysis.Diagnostic{Pos: r.pos, Message: msg})
		}
	}
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
		collect:      true,
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

	// Derive the exclusions of the methods that take their own receiver's
	// lock. This must precede the scan, which consults the facts at each
	// call site.
	pc.synthesizeExcludes(state.SrcFuncs)

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
		pc.contained(fn, func() {
			pc.checkFunction(callSite{kind: siteEntry}, nil, fn, &lff, nil, false /* force */)
		})
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
		pc.contained(fn, func() {
			if pc.literalIgnored(fn) && !lff.Ignore {
				c := *lff
				c.Ignore = true
				lff = &c
			}
			pc.checkFunction(callSite{kind: siteEntry}, nil, fn, lff, nil, false /* force */)
		})
	}

	// Check for inferred checklocks annotations.
	// A failed function's partial observations would skew the hints for other functions' fields.
	if enableInferred && !pc.panicked {
		pc.checkInferred()
	}

	pc.flushReports()
	pc.writeDecisions()

	// Check for expected failures.
	pc.checkFailures()

	return nil, nil
}
