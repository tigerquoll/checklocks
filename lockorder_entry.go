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
	"go/types"
	"strings"
)

// The guard facts of the checklocks analyzer are read here, so that a code base annotated
// for guarded fields gets the ordering check without a second set of annotations. A
// function declared to run with a lock held starts the ordering walk with that lock's class
// held, which is what carries the order across a boundary this analysis cannot follow on
// its own: a callback invoked by machinery after the caller took the lock.
//
// The facts are inherited from the required analyzer rather than re-read from the comments.
// That keeps one source of truth for what an annotation means, and it picks up the guards
// of functions in other packages, which a scan of this package's syntax cannot see.

// heldOnEntry returns the classes a function is annotated as holding when it is called.
func (pc *lockOrderContext) heldOnEntry(obj *types.Func) []string {
	var lff lockFunctionFacts
	if !pc.pass.ImportObjectFact(obj, &lff) {
		return nil
	}
	if len(lff.HeldOnEntry) == 0 {
		return nil
	}
	out := make([]string, 0, len(lff.HeldOnEntry))
	for guard := range lff.HeldOnEntry {
		if class := pc.classOfGuard(obj, guard); class != "" {
			out = append(out, class)
		}
	}
	return out
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
func (pc *lockOrderContext) classOfGuard(obj *types.Func, guard string) string {
	if guard == "" {
		return ""
	}
	parts := strings.Split(guard, ".")

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
		named, st, ok := namedStructOf(current)
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			if _, ok := structFieldType(st, field); !ok {
				return ""
			}
			return pc.classForNamed(named, field)
		}
		next, ok := structFieldType(st, field)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

// namedStructOf unwraps a type to the named struct underneath it.
func namedStructOf(t types.Type) (*types.Named, *types.Struct, bool) {
	named, ok := namedOf(t)
	if !ok {
		return nil, nil, false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return nil, nil, false
	}
	return named, st, true
}

// structFieldType returns the type of a named field.
func structFieldType(st *types.Struct, name string) (types.Type, bool) {
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i).Name() == name {
			return st.Field(i).Type(), true
		}
	}
	return nil, false
}
