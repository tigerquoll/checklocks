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
)

// closureAnnotations resolves the annotations attached to function literals.
//
// A function literal has no declaration to carry a doc comment, so a comment
// written above one is not attached to anything by the parser. It is matched
// to the enclosing syntax here, and only in the three positions where a
// comment can be read as documenting the literal and nothing else:
//
//   - immediately above the literal itself, when exactly one literal begins
//     on that line;
//   - immediately above an assignment or declaration whose right hand side is
//     that one literal, which is how a named callback is written;
//   - immediately above a key and value in a composite literal whose value is
//     that literal, which is how a table of callbacks is written.
//
// A literal passed directly as an argument is covered by the first of those,
// since the comment then precedes the literal itself. A comment above a line
// on which several literals begin binds to none of them: it does not say which
// it means, and guessing would attach a lock requirement to the wrong body.
//
// Note that this deliberately does not use the parser's own comment
// association, which attaches a comment to the largest node beginning on the
// following line and would bind a comment above a multi-literal statement to
// all of it.
func (pc *passContext) closureAnnotations() map[*ast.FuncLit]*lockFunctionFacts {
	out := make(map[*ast.FuncLit]*lockFunctionFacts)
	for _, f := range pc.pass.Files {
		// The comment groups of the file, by the line they end on, so
		// that the group directly above a node can be found.
		byEndLine := make(map[int]*ast.CommentGroup, len(f.Comments))
		for _, cg := range f.Comments {
			byEndLine[pc.pass.Fset.Position(cg.End()).Line] = cg
		}
		// The number of literals beginning on each line, so that a
		// line carrying more than one can be left alone.
		perLine := make(map[int]int)
		ast.Inspect(f, func(n ast.Node) bool {
			if fl, ok := n.(*ast.FuncLit); ok {
				perLine[pc.pass.Fset.Position(fl.Pos()).Line]++
			}
			return true
		})
		// preceding returns the comment group on the line above a node.
		preceding := func(n ast.Node) *ast.CommentGroup {
			return byEndLine[pc.pass.Fset.Position(n.Pos()).Line-1]
		}
		record := func(carrier ast.Node, fl *ast.FuncLit) {
			cg := preceding(carrier)
			if cg == nil {
				return
			}
			if lff := pc.closureFacts(fl, cg); lff != nil {
				out[fl] = lff
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				// The comment directly above the literal, when
				// it is the only one starting on its line.
				if perLine[pc.pass.Fset.Position(x.Pos()).Line] == 1 {
					record(x, x)
				}
			case *ast.AssignStmt:
				if len(x.Rhs) != 1 {
					return true
				}
				if fl, ok := x.Rhs[0].(*ast.FuncLit); ok {
					record(x, fl)
				}
			case *ast.ValueSpec:
				if len(x.Values) != 1 {
					return true
				}
				if fl, ok := x.Values[0].(*ast.FuncLit); ok {
					record(x, fl)
				}
			case *ast.KeyValueExpr:
				if fl, ok := x.Value.(*ast.FuncLit); ok {
					record(x, fl)
				}
			}
			return true
		})
	}
	return out
}

// closureFacts reads the annotations of one comment group as the facts of a
// function literal.
//
// The literal is wrapped in a declaration so that the guard resolution used
// for a declared function applies unchanged: a guard names a parameter of the
// literal, or a package-level variable, exactly as it would for a function.
// There is no receiver, and no object to export a fact to, so the facts are
// returned rather than published; a literal is analyzed where it is written.
func (pc *passContext) closureFacts(fl *ast.FuncLit, cg *ast.CommentGroup) *lockFunctionFacts {
	// N.B. FuncDecl.Pos reports the position of the type, so the synthetic
	// declaration reports diagnostics against the literal itself.
	d := &ast.FuncDecl{Type: fl.Type, Body: fl.Body}
	var lff lockFunctionFacts
	found := false
	for _, l := range cg.List {
		pc.extractAnnotations(l.Text, map[string]func(string){
			checkLocksIgnore: func(string) {
				lff.Ignore = true
				found = true
			},
			checkLocksAnnotation: func(g string) {
				lff.addGuardedBy(pc, d, g, true /* exclusive */)
				found = true
			},
			checkLocksAnnotationRead: func(g string) {
				lff.addGuardedBy(pc, d, g, false /* exclusive */)
				found = true
			},
			checkLocksAcquires: func(g string) {
				lff.addAcquires(pc, d, g, true /* exclusive */)
				found = true
			},
			checkLocksAcquiresRead: func(g string) {
				lff.addAcquires(pc, d, g, false /* exclusive */)
				found = true
			},
			checkLocksReleases: func(g string) {
				lff.addReleases(pc, d, g, true /* exclusive */)
				found = true
			},
			checkLocksReleasesRead: func(g string) {
				lff.addReleases(pc, d, g, false /* exclusive */)
				found = true
			},
			checkLocksExcludes: func(g string) {
				lff.addExcludes(pc, d, g, false /* exclusiveOnly */)
				found = true
			},
			checkLocksExcludesWrite: func(g string) {
				lff.addExcludes(pc, d, g, true /* exclusiveOnly */)
				found = true
			},
		})
	}
	if !found {
		return nil
	}
	return &lff
}

// closureFactsFor returns the annotated facts of a function, if it is an
// annotated literal.
func (pc *passContext) closureFactsFor(syntax ast.Node) (*lockFunctionFacts, bool) {
	fl, ok := syntax.(*ast.FuncLit)
	if !ok {
		return nil, false
	}
	lff, ok := pc.closures[fl]
	return lff, ok
}
