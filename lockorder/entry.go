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

package lockorder

import (
	"go/ast"
	"go/types"
	"strings"
)

// The guard annotations of the checklocks analyzer are read here as well, so that a code
// base annotated for guarded fields gets the ordering check without a second set of
// annotations. A function declared to run with a lock held starts the ordering walk with
// that lock's class held, which is what carries the order across a boundary this analysis
// cannot follow on its own: a callback invoked by machinery after the caller took the lock.
const (
	checkLocksAnnotation     = "// +checklocks:"
	checkLocksAnnotationRead = "// +checklocksread:"
)

// heldOnEntry returns the classes a function is annotated as holding when it is called.
func (pc *passContext) heldOnEntry(obj *types.Func) []string {
	guards := pc.entryGuards[obj]
	if len(guards) == 0 {
		return nil
	}
	out := make([]string, 0, len(guards))
	for _, g := range guards {
		if class := pc.classOfGuard(obj, g); class != "" {
			out = append(out, class)
		}
	}
	return out
}

// loadEntryGuards records the guard annotations of every function in the package.
//
// They are collected from the syntax rather than imported as facts because the fact type
// belongs to the checklocks analyzer and is not exported from it; re-reading the comment is
// cheaper than widening that API while it is being reworked.
func (pc *passContext) loadEntryGuards() {
	pc.entryGuards = make(map[*types.Func][]string)
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Doc == nil {
				continue
			}
			obj, ok := pc.pass.TypesInfo.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			for _, c := range fd.Doc.List {
				pc.extractAnnotations(c.Text, map[string]func(string){
					checkLocksAnnotation: func(p string) {
						pc.entryGuards[obj] = append(pc.entryGuards[obj], strings.TrimSpace(p))
					},
					checkLocksAnnotationRead: func(p string) {
						pc.entryGuards[obj] = append(pc.entryGuards[obj], strings.TrimSpace(p))
					},
				})
			}
		}
	}
}

// classOfGuard resolves a guard expression to the class of the object that owns the lock.
//
// A guard names a path to a lock field, rooted at the receiver or a parameter:
//
//	RWMutex               the lock field of the receiver
//	sa.RWMutex            the lock field of the named receiver or parameter
//	p.application.RWMutex a lock reached through a field of it
//
// The class belongs to the type that owns the final lock field, so the path is walked and
// the owner of the last step is what is looked up.
func (pc *passContext) classOfGuard(obj *types.Func, guard string) string {
	parts := strings.Split(guard, ".")
	if len(parts) == 0 || guard == "" {
		return ""
	}

	sig, ok := obj.Type().(*types.Signature)
	if !ok {
		return ""
	}

	// Resolve the root of the path.
	var current types.Type
	if len(parts) == 1 {
		// A bare field name is a field of the receiver.
		if sig.Recv() == nil {
			return ""
		}
		current = sig.Recv().Type()
	} else {
		name := parts[0]
		if sig.Recv() != nil && sig.Recv().Name() == name {
			current = sig.Recv().Type()
		} else {
			for i := 0; i < sig.Params().Len(); i++ {
				if sig.Params().At(i).Name() == name {
					current = sig.Params().At(i).Type()
					break
				}
			}
		}
		if current == nil {
			return ""
		}
		parts = parts[1:]
	}

	// Walk the field path. The owner of the last field is what carries the class.
	for i, field := range parts {
		named, st, ok := structOf(current)
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			return pc.classForOwner(named, field, st)
		}
		next, ok := fieldType(st, field)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

// classForOwner looks up the class declared on the type owning the named lock field.
func (pc *passContext) classForOwner(named *types.Named, field string, st *types.Struct) string {
	if _, ok := fieldType(st, field); !ok {
		return ""
	}
	tn := named.Obj()
	if tn == nil {
		return ""
	}
	var cf classFact
	if !pc.pass.ImportObjectFact(tn, &cf) {
		return ""
	}
	if cf.Field != "" && cf.Field != field {
		return ""
	}
	return cf.Class
}

// structOf unwraps a type to the named struct underneath it.
func structOf(t types.Type) (*types.Named, *types.Struct, bool) {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil, nil, false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, nil, false
	}
	return named, st, true
}

// fieldType returns the type of a named field.
func fieldType(st *types.Struct, name string) (types.Type, bool) {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return st.Field(i).Type(), true
		}
	}
	return nil, false
}
