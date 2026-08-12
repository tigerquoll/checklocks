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

// Hierarchical ordering within a single class.
//
// A hierarchical class is exempt from the same-class rule because instances of it nest
// legitimately: a tree walks itself. What the class-level check cannot see is the DIRECTION
// of that nesting. Both sides are the same class, so parent-then-child and child-then-parent
// are indistinguishable to it, and only one of them is safe.
//
// This recovers the direction from the structure. The edge from an instance to its parent is
// a field, so an acquisition whose receiver was reached through that field of an instance
// whose lock is already held is a child-then-parent nesting, and that is the violation.
//
// The check is INTRAPROCEDURAL ONLY, and deliberately so. It rests on value identity, which
// does not survive a summary: a summary records the classes a function may acquire, not which
// instance they were reached from, so a parent acquired one call deeper cannot be tied back
// to the child held here. The cross-function case stays with the runtime checker, which sees
// instances. It is also off by default, since the approximation accepts escapes.

package checklocks

import (
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// lockHierarchyEdge marks the field linking an instance to its parent.
const lockHierarchyEdge = "// +lockhierarchyedge"

var (
	// enableHierarchy turns the check on. It is off by default: the analysis is an
	// approximation with known escapes, so it is opt-in rather than something an
	// existing user starts seeing without asking.
	enableHierarchy = false

	// enableHierarchyInfer treats a field of the type's own type as the parent edge when
	// no field is annotated. Convenient on a code base that has not annotated yet, but a
	// self-typed field is not necessarily a parent link, so it is separately opt-in.
	enableHierarchyInfer = false
)

func init() {
	LockOrderAnalyzer.Flags.BoolVar(&enableHierarchy, "hierarchy", false,
		"check parent-before-child ordering within a hierarchical class")
	LockOrderAnalyzer.Flags.BoolVar(&enableHierarchyInfer, "hierarchyinfer", false,
		"with -hierarchy, treat a self-typed field as the parent edge when none is annotated")
}

// hierarchyEdgeFact records the field that links an instance to its parent.
type hierarchyEdgeFact struct {
	// Field is the name of the field holding the parent.
	Field string
}

// AFact implements analysis.Fact.AFact.
func (*hierarchyEdgeFact) AFact() {}

// loadHierarchyEdges records the parent edge declared on the struct types of this package.
func (pc *lockOrderContext) loadHierarchyEdges() {
	if !enableHierarchy {
		return
	}
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
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				obj, ok := pc.pass.TypesInfo.Defs[ts.Name].(*types.TypeName)
				if !ok {
					continue
				}
				if name := edgeFieldName(st, obj); name != "" {
					pc.pass.ExportObjectFact(obj, &hierarchyEdgeFact{Field: name})
				}
			}
		}
	}
}

// edgeFieldName returns the name of the parent field of a struct type.
//
// An annotated field wins. Failing that, and only when inference is enabled, a single field
// whose type is the struct's own type is taken to be the parent link; more than one such
// field is ambiguous and is left alone.
func edgeFieldName(st *ast.StructType, obj *types.TypeName) string {
	for _, field := range st.Fields.List {
		for _, cg := range []*ast.CommentGroup{field.Doc, field.Comment} {
			if cg == nil {
				continue
			}
			for _, c := range cg.List {
				if !hasAnnotation(c.Text, lockHierarchyEdge) {
					continue
				}
				if len(field.Names) != 1 {
					continue
				}
				return field.Names[0].Name
			}
		}
	}
	if !enableHierarchyInfer {
		return ""
	}
	named, ok := types.Unalias(obj.Type()).(*types.Named)
	if !ok {
		return ""
	}
	st2, ok := named.Underlying().(*types.Struct)
	if !ok {
		return ""
	}
	found := ""
	for i := 0; i < st2.NumFields(); i++ {
		field := st2.Field(i)
		fieldNamed, ok := namedOf(field.Type())
		if !ok || fieldNamed != named {
			continue
		}
		if found != "" {
			// Ambiguous: two self-typed fields, and nothing says which is the parent.
			return ""
		}
		found = field.Name()
	}
	return found
}

// hasAnnotation reports whether a comment carries the given annotation.
func hasAnnotation(text, annotation string) bool {
	found := false
	extractAnnotations(text, map[string]func(string){
		annotation: func(string) { found = true },
	})
	return found
}

// edgeFieldIndex returns the index of the parent field for a value's type.
func (pc *lockOrderContext) edgeFieldIndex(typ types.Type) (int, string, bool) {
	named, ok := namedOf(typ)
	if !ok {
		return 0, "", false
	}
	obj := named.Obj()
	if obj == nil {
		return 0, "", false
	}
	var hef hierarchyEdgeFact
	if !pc.pass.ImportObjectFact(obj, &hef) {
		return 0, "", false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return 0, "", false
	}
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == hef.Field {
			return i, hef.Field, true
		}
	}
	return 0, "", false
}

// instanceSet tracks which instances are holding a lock, for the classes where the instance
// matters. It is kept beside the class set rather than inside it: the class set is what the
// summaries are built from, and an instance is meaningless outside the function it occurs in.
type instanceSet struct {
	held map[ssa.Value]string
}

func newInstanceSet() *instanceSet {
	return &instanceSet{held: make(map[ssa.Value]string)}
}

// fork copies the set, for the separate paths of a branch.
func (is *instanceSet) fork() *instanceSet {
	out := &instanceSet{held: make(map[ssa.Value]string, len(is.held))}
	for k, v := range is.held {
		out.held[k] = v
	}
	return out
}

// acquire records that an instance's lock has been taken.
func (is *instanceSet) acquire(v ssa.Value, class string) {
	if v == nil || class == "" {
		return
	}
	is.held[v] = class
}

// release records that an instance's lock has been dropped.
func (is *instanceSet) release(v ssa.Value) {
	if v == nil {
		return
	}
	delete(is.held, v)
}

// merge folds another set into this one.
//
// A value held on either path into a block is treated as held afterwards, which matches how
// the class set merges and errs towards reporting rather than towards silence.
func (is *instanceSet) merge(other *instanceSet) {
	for k, v := range other.held {
		is.held[k] = v
	}
}

// checkHierarchy reports an acquisition that runs up the hierarchy while a lower instance is
// held.
func (pc *lockOrderContext) checkHierarchy(pos token.Pos, acquired ssa.Value, class, via string, is *instanceSet, report bool) {
	// The walk is shared with the blocking analyzer, which asks its own question of it;
	// the direction of a nesting is this analyzer's to report on.
	if !enableHierarchy || !report || pc.check != checkOrder || acquired == nil {
		return
	}
	if !pc.order.hierarchical[class] {
		// Only a hierarchical class has a direction to get wrong. Two instances of a
		// class that is not hierarchical are the same-class case, which the class level
		// check already owns.
		return
	}
	// Normalise both sides to the instance that owns the lock, since the hierarchy is
	// defined over instances rather than over the lock fields inside them.
	acquiredOwner := lockOwner(pc.pass, acquired)
	index, fieldName, ok := pc.edgeFieldIndex(acquiredOwner.Type())
	if !ok {
		return
	}
	for held, heldClass := range is.held {
		if heldClass != class {
			continue
		}
		heldOwner := lockOwner(pc.pass, held)
		if heldOwner == acquiredOwner {
			continue
		}
		if !derivesFromParent(acquiredOwner, heldOwner, index) {
			continue
		}
		pc.maybeFail(pos, "acquiring %s through the %s field of an instance whose lock is held (via %s): a hierarchical class must be locked parent first", class, fieldName, via)
		return
	}
}

// lockOwner returns the instance whose lock a receiver refers to.
//
// A lock held as a field of a struct, reached by a promoted or forwarded method, has that
// struct as its owner, and the struct is what the hierarchy is defined over. Only a lock
// field is stripped: any other field on the way is part of how the instance was reached, and
// removing it would erase the very step this analysis looks for.
//
// A type that wraps its own lock and forwards to it is already the owner.
func lockOwner(pass *analysis.Pass, v ssa.Value) ssa.Value {
	fa, ok := underlyingFieldAddr(v)
	if !ok {
		return unwrapConversions(v)
	}
	named, _, ok := ownerOf(fa)
	if !ok {
		return unwrapConversions(v)
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok || fa.Field >= st.NumFields() {
		return unwrapConversions(v)
	}
	if !isLockTypeIn(pass, st.Field(fa.Field).Type()) {
		return unwrapConversions(v)
	}
	return unwrapConversions(fa.X)
}

// maxParentHops bounds the walk up a parent chain. A chain longer than this in one function
// is not a shape worth chasing, and the bound keeps a malformed graph from spinning.
const maxParentHops = 16

// derivesFromParent reports whether acquired was reached from base by following the parent
// field one or more times.
//
// Only the shapes a field read produces are followed: the load of a pointer field, a field of
// a struct value, and the conversions that do not change a value. A parent that arrived by
// any other route, notably as a function result or through a variable this walk cannot tie
// back, is not recognised, and the acquisition is not reported. That is the main escape.
func derivesFromParent(acquired, base ssa.Value, field int) bool {
	cur := unwrapConversions(acquired)
	for i := 0; i < maxParentHops; i++ {
		next, ok := stripParentField(cur, field)
		if !ok {
			return false
		}
		if next == unwrapConversions(base) {
			return true
		}
		cur = next
	}
	return false
}

// stripParentField peels one read of the parent field off a value.
func stripParentField(v ssa.Value, field int) (ssa.Value, bool) {
	switch x := unwrapConversions(v).(type) {
	case *ssa.UnOp:
		// The load of a pointer typed parent field.
		if x.Op != token.MUL {
			return nil, false
		}
		if fa, ok := unwrapConversions(x.X).(*ssa.FieldAddr); ok && fa.Field == field {
			return unwrapConversions(fa.X), true
		}
	case *ssa.FieldAddr:
		// The address of the parent field, for a struct valued parent.
		if x.Field == field {
			return unwrapConversions(x.X), true
		}
	case *ssa.Field:
		if x.Field == field {
			return unwrapConversions(x.X), true
		}
	}
	return nil, false
}
