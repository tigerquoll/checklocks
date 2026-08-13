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
	"go/ast"
	"go/token"
	"go/types"
)

// A structure whose fields are guarded by one lock says so once per field, and a large one
// says it thirty times. The repetition is not saying anything: it is the same sentence about
// the same lock, and the one field where it is missing is invisible among the ones where it
// is not.
//
// A guard written on the STRUCTURE says it once. What it expands to is exactly what was
// written by hand before — a guard on each field — so nothing downstream can tell the
// difference, and no analysis changes.
//
// This is gVisor issue #12648.
const (
	// checkLocksStruct is the sugar. Written on a structure type, every field of it is
	// guarded by the named lock:
	//
	//	// +checklocks:mu
	//	type queue struct {
	//		mu    sync.Mutex
	//		name  string
	//		state int
	//	}
	//
	// A field that carries its own guard keeps it, and a field that carries
	// +checklocksunguarded has none.
	checkLocksStruct = "// +checklocks:"

	// checkLocksGuardedBy is the same expansion, declared as the structure's rule rather
	// than as shorthand for writing it out:
	//
	//	// +checklocksguardedby:mu
	//
	// The difference is what it says about the fields it does NOT cover. Under the sugar
	// a field with its own guard is simply left alone; under this a per field guard
	// naming the same lock is a restatement of the rule and is reported as redundant, so
	// that the annotations it replaces do not quietly survive it.
	checkLocksGuardedBy = "// +checklocksguardedby:"

	// checkLocksUnguarded exempts a field from the structure's guard:
	//
	//	// +checklocksunguarded
	//	parent *queue // set at construction, read without the lock
	//
	// This is the direction that fails loudly. A field that should have been exempt and
	// was not is reported at every unlocked access, which is a false positive and looks
	// like one; a field that should have been guarded and was not, under the per field
	// annotations this replaces, is silence.
	checkLocksUnguarded = "// +checklocksunguarded"
)

// structGuard is a guard declared on a structure rather than on its fields.
type structGuard struct {
	// name is the guard as it was written, which is the key the per field facts use.
	name string

	// resolver is the resolution of that name against this structure, worked out once.
	resolver fieldGuardResolver

	// lockObj is the field the name resolves to, which the expansion skips: a lock does
	// not guard itself.
	lockObj types.Object

	// strict is the +checklocksguardedby spelling, under which a per field guard naming
	// the same lock is redundant.
	strict bool

	// pos is where the declaration is, for reporting against.
	pos token.Pos
}

// structGuardFor reads the guard declared on a structure, if there is one.
//
// The annotation is accepted in either place a type's documentation can be, as the other
// type level annotations are: on the specification, and on a declaration that holds only
// that specification.
func (pc *passContext) structGuardFor(structType *types.Struct, docs []*ast.CommentGroup) *structGuard {
	var (
		out   *structGuard
		found bool
	)
	set := func(name string, strict bool, pos token.Pos) {
		if found {
			pc.maybeFail(pos, "structure guard specified more than once")
			return
		}
		found = true
		fl, _, objs, ok := pc.resolveFieldListParts(pos, structType, splitGuardName(name))
		if !ok {
			pc.maybeFail(pos, "annotation %s cannot be resolved", name)
			return
		}
		lockObj := objs[len(objs)-1]
		if !pc.validateMutex(pos, lockObj, true /* exclusive */) {
			return
		}
		out = &structGuard{
			name:     name,
			resolver: &fieldGuard{FieldList: fl},
			lockObj:  lockObj,
			strict:   strict,
			pos:      pos,
		}
	}
	for _, cg := range docs {
		if cg == nil {
			continue
		}
		for _, l := range cg.List {
			pos := l.Pos()
			pc.extractAnnotations(l.Text, map[string]func(string){
				checkLocksStruct:    func(name string) { set(name, false /* strict */, pos) },
				checkLocksGuardedBy: func(name string) { set(name, true /* strict */, pos) },
			})
		}
	}
	return out
}

// splitGuardName splits a guard path into the parts the resolver walks.
func splitGuardName(name string) []string {
	var (
		parts []string
		cur   string
	)
	for _, r := range name {
		if r == '.' {
			parts = append(parts, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(parts, cur)
}

// applyStructGuard gives a field the structure's guard, unless the field has said
// otherwise.
//
// The fact it produces is the one a per field annotation produces, so everything that
// consumes field guards — the access checks, and the analyses that derive preconditions
// and exclusions from them — sees a structure guard and a hand written one as the same
// thing.
func (pc *passContext) applyStructGuard(fieldObj *types.Var, field *ast.Field, lgf *lockGuardFacts, sg *structGuard) {
	exempt := fieldHasAnnotation(field, checkLocksUnguarded)
	if sg == nil {
		if exempt {
			pc.maybeFail(fieldObj.Pos(), "annotation %s has nothing to exempt this field from: the structure declares no guard", checkLocksUnguarded)
		}
		return
	}
	if exempt {
		return
	}
	// A lock does not guard itself, and no other lock in the structure is guarded by it
	// either. A lock is not data: putting one under another declares an order between
	// them, which is a statement about how they nest and not one the structure's guard
	// was making. Locking the second lock would need the first one held, which is the
	// opposite of what the code that declared them is doing.
	if fieldObj == sg.lockObj || pc.isLockField(fieldObj) {
		return
	}
	if _, ok := lgf.GuardedBy[sg.name]; ok {
		if sg.strict {
			pc.maybeFail(fieldObj.Pos(), "annotation %s is redundant, the structure is guarded by %s", sg.name, sg.name)
		}
		return
	}
	if len(lgf.GuardedBy) > 0 {
		// The field named a different lock, which is more specific than the rule for
		// the structure and wins.
		return
	}
	if lgf.GuardedBy == nil {
		lgf.GuardedBy = make(map[string]fieldGuardResolver)
	}
	lgf.GuardedBy[sg.name] = sg.resolver
	pc.pass.ExportObjectFact(fieldObj, lgf)
}

// isLockField reports whether a field is itself a lock.
//
// This asks the same question the guard resolution asks of the lock it is given, and
// quietly: a field that is not a lock is the ordinary case here, not an error.
func (pc *passContext) isLockField(fieldObj *types.Var) bool {
	if _, ok := pc.lockPrimitiveFor(fieldObj.Type()); ok {
		return true
	}
	s := fieldObj.Type().String()
	return rwMutexRE.MatchString(s) || mutexRE.MatchString(s) || lockerRE.MatchString(s)
}

// fieldHasAnnotation reports whether a field carries an annotation, in either of the two
// places a field's comments live.
func fieldHasAnnotation(field *ast.Field, annotation string) bool {
	if field == nil {
		return false
	}
	for _, cg := range []*ast.CommentGroup{field.Doc, field.Comment} {
		if cg == nil {
			continue
		}
		for _, l := range cg.List {
			if hasAnnotation(l.Text, annotation) {
				return true
			}
		}
	}
	return false
}

// structFields pairs each field of a structure type with the syntax that declared it.
//
// One syntactic field declares more than one when the names are written together, as in
// "a, b int", and an embedded field declares one with no name at all. Walking the two lists
// in step without accounting for that pairs the wrong syntax with the wrong field, which
// puts an annotation on a field that was not annotated and takes it off the one that was.
func structFields(structType *types.Struct, ss *ast.StructType) []structFieldBinding {
	out := make([]structFieldBinding, 0, structType.NumFields())
	index := 0
	for _, field := range ss.Fields.List {
		count := len(field.Names)
		if count == 0 {
			count = 1 // Embedded.
		}
		for i := 0; i < count; i++ {
			if index >= structType.NumFields() {
				return out
			}
			out = append(out, structFieldBinding{
				obj:   structType.Field(index),
				field: field,
			})
			index++
		}
	}
	return out
}

// structFieldBinding is one field of a structure and the syntax that declared it.
type structFieldBinding struct {
	obj   *types.Var
	field *ast.Field
}
