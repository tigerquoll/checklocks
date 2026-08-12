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

// Package lockorder checks that locks are acquired in the declared order.
//
// The order is declared over lock CLASSES rather than lock instances, which is what lets it
// catch the case a runtime detector keyed on instances cannot: two objects of the same kind
// taken in opposite orders by two goroutines. The classes and the order between them are
// declared with annotations, see annotations.go.
//
// The analysis is modular, because go/analysis is: there is no whole program call graph, so
// each function exports a summary of the classes it may acquire and a call site consults the
// summary of its callee. Dependencies are analyzed before dependents, so the summary of a
// statically dispatched callee is always available.
//
// Known limits, all shared with the checklocks analyzer it sits beside:
//   - Interface dispatch has no callee to consult, so it is skipped. Annotate the
//     implementation instead.
//   - Only static dispatch contributes to a summary.
//   - Acquisitions inside a function started with "go" belong to the new goroutine and are
//     deliberately not attributed to the spawner: that is the sanctioned way to break a
//     nesting, and reporting it would punish the fix.
//   - A callee that RELEASES its caller's lock before acquiring, the unlock-relock gap, is
//     modelled for the lock of its own receiver only, which is the shape the idiom takes.
//     The summary records what had been released at each acquisition and the call site
//     subtracts it. That subtraction is by class, as everything here is, so a caller holding
//     a different OBJECT of the released class is let off with it: the one place this
//     analysis errs towards silence rather than towards reporting.
//
// +checkalignedignore
package checklocks

import (
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
)

// Analyzer is the entrypoint.
var LockOrderAnalyzer = &analysis.Analyzer{
	Name:     "lockorder",
	Doc:      "checks that lock classes are acquired in the declared order",
	Run:      runLockOrder,
	Requires: []*analysis.Analyzer{buildssa.Analyzer, Analyzer},
	FactTypes: []analysis.Fact{
		(*classFact)(nil),
		(*hierarchyEdgeFact)(nil),
		(*orderFact)(nil),
		(*summaryFact)(nil),
		(*funcFact)(nil),
	},
}

// checkKind is the question the shared walk is asked to report on.
//
// The walk, the lock set and the summaries are the same for both analyzers built on them;
// what differs is which finding is a diagnostic and which is only recorded.
type checkKind int

const (
	// checkOrder reports an acquisition that breaks the declared order.
	checkOrder checkKind = iota

	// checkBlocking reports reaching a blocking sink while a class is held.
	checkBlocking
)

// lockOrderContext carries the per pass state.
//
// The expectation machinery is shared with the other analyzers in this package, so a corpus
// can state expectations for this analyzer without the others acting on them.
type lockOrderContext struct {
	*expectations
	pass *analysis.Pass

	// order is the taxonomy, assembled from the declarations of this package and the
	// order facts of its dependencies.
	order *order

	// check is the analyzer the walk is reporting for.
	check checkKind
}

// run is the main entrypoint.
func runLockOrder(pass *analysis.Pass) (any, error) {
	pc := &lockOrderContext{
		expectations: newExpectations(pass, lockOrderAnnotations, true /* reportInvalidPos */),
		pass:         pass,
		order:        newOrder(),
		check:        checkOrder,
	}
	pc.extractLineFailures()

	// Assemble the taxonomy: the declarations in this package, then the ones imported
	// from dependencies.
	pc.loadDeclaredOrder()
	pc.importOrderFacts()
	if err := pc.order.close(); err != nil {
		// A cycle is a declaration bug rather than a finding in the analyzed code, so it
		// is reported once against the package rather than at a call site.
		pc.pass.Reportf(token.NoPos, "%s", err.Error())
		return nil, nil
	}
	pc.exportOrderFact()

	// Class declarations on the types of this package.
	pc.loadDeclaredClasses()

	// The parent edge of each hierarchical type, for the intra-function direction check.
	pc.loadHierarchyEdges()

	// Function level annotations.
	pc.loadFunctionAnnotations()

	// Walk the package to a fixpoint and report against the settled summaries.
	state := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	pc.analyzePackage(state.SrcFuncs)

	pc.checkFailures()
	return nil, nil
}

// loadDeclaredOrder reads the order declarations out of the package doc comments.
func (pc *lockOrderContext) loadDeclaredOrder() {
	for _, f := range pc.pass.Files {
		if f.Doc == nil {
			continue
		}
		for _, c := range f.Doc.List {
			extractAnnotations(c.Text, map[string]func(string){
				lockOrderAnnotation: func(p string) {
					e, err := parseOrderEdge(p)
					if err != nil {
						pc.pass.Reportf(c.Pos(), "invalid lockorder annotation: %v", err)
						return
					}
					pc.order.addEdge(e)
				},
				lockHierarchicalAnnotation: func(p string) {
					name, err := parseClassName(p)
					if err != nil {
						pc.pass.Reportf(c.Pos(), "invalid lockhierarchical annotation: %v", err)
						return
					}
					pc.order.hierarchical[name] = true
				},
				lockOrderWithheldAnnotation: func(p string) {
					name, err := parseClassName(p)
					if err != nil {
						pc.pass.Reportf(c.Pos(), "invalid lockorderwithheld annotation: %v", err)
						return
					}
					pc.order.withheld[name] = true
				},
			})
		}
	}
}

// importOrderFacts folds the taxonomy declared by dependencies into this pass.
func (pc *lockOrderContext) importOrderFacts() {
	for _, pkg := range pc.pass.Pkg.Imports() {
		var of orderFact
		if !pc.pass.ImportPackageFact(pkg, &of) {
			continue
		}
		for _, e := range of.Edges {
			pc.order.addEdge(e)
		}
		for _, c := range of.Hierarchical {
			pc.order.hierarchical[c] = true
		}
		for _, c := range of.Withheld {
			pc.order.withheld[c] = true
		}
	}
}

// exportOrderFact publishes the taxonomy so dependents see it. The closure is not exported,
// only the declared edges, so that a dependent recomputes it and a cycle introduced by two
// packages declaring halves of it is still caught.
func (pc *lockOrderContext) exportOrderFact() {
	if len(pc.order.edges) == 0 && len(pc.order.hierarchical) == 0 && len(pc.order.withheld) == 0 {
		return
	}
	of := &orderFact{Edges: pc.order.edges}
	for c := range pc.order.hierarchical {
		of.Hierarchical = append(of.Hierarchical, c)
	}
	for c := range pc.order.withheld {
		of.Withheld = append(of.Withheld, c)
	}
	sort.Strings(of.Hierarchical)
	sort.Strings(of.Withheld)
	pc.pass.ExportPackageFact(of)
}

// loadDeclaredClasses reads the class declarations on the types of this package and exports
// them as facts.
func (pc *lockOrderContext) loadDeclaredClasses() {
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				// A single type declaration attaches its doc to the declaration, a
				// grouped one to the spec; check both or the annotation is silently
				// dropped.
				pc.classFromDoc(ts, ts.Doc)
				if len(d.Specs) == 1 {
					pc.classFromDoc(ts, d.Doc)
				}
			}
		}
	}
}

// classFromDoc reads a class annotation from a doc comment and attaches it to the type.
func (pc *lockOrderContext) classFromDoc(ts *ast.TypeSpec, doc *ast.CommentGroup) {
	if doc == nil {
		return
	}
	for _, c := range doc.List {
		extractAnnotations(c.Text, map[string]func(string){
			lockClassAnnotation: func(p string) {
				name, err := parseClassName(p)
				if err != nil {
					pc.pass.Reportf(c.Pos(), "invalid lockclass annotation: %v", err)
					return
				}
				obj, ok := pc.pass.TypesInfo.Defs[ts.Name].(*types.TypeName)
				if !ok {
					return
				}
				pc.pass.ExportObjectFact(obj, &classFact{Class: name})
			},
		})
	}
}

// loadFunctionAnnotations records the function level annotations of the analyses built on
// this summary, so that a call site in another package sees them.
func (pc *lockOrderContext) loadFunctionAnnotations() {
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Doc == nil {
				continue
			}
			var ff funcFact
			for _, c := range fd.Doc.List {
				extractAnnotations(c.Text, map[string]func(string){
					lockOrderIgnore:    func(string) { ff.Ignore = true },
					blockingAnnotation: func(string) { ff.Blocking = true },
					lockBlockingIgnore: func(string) { ff.BlockingIgnore = true },
				})
			}
			if !ff.Ignore && !ff.Blocking && !ff.BlockingIgnore {
				continue
			}
			if obj, ok := pc.pass.TypesInfo.Defs[fd.Name].(*types.Func); ok {
				pc.pass.ExportObjectFact(obj, &ff)
			}
		}
	}
}
