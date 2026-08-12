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
	"go/types"

	"golang.org/x/tools/go/analysis"
)

// lockPrimitiveFacts marks a type whose lock methods are lock primitives.
//
// A project that wraps a lock, to add instrumentation or to fix the API it
// exposes, ends up with a type that behaves exactly like the lock it wraps but
// is not one as far as this analysis is concerned. Recognition is otherwise by
// name, which such a wrapper may or may not match, and the wrapper's own
// methods are ordinary functions that take a lock and do not release it.
//
// Declaring the type makes its Lock, Unlock and friends primitives: they are
// intercepted at the call site exactly as the standard ones are, and their own
// bodies are not analyzed, because the body of a primitive is not a critical
// section, it is the implementation of the lock.
type lockPrimitiveFacts struct {
	// Read indicates the type offers a read lock, i.e. it behaves as an
	// RWMutex rather than a Mutex.
	Read bool
}

// AFact implements analysis.Fact.AFact.
func (*lockPrimitiveFacts) AFact() {}

// lockPrimitiveTypeFacts records a type declared to be a lock primitive.
func (pc *passContext) lockPrimitiveTypeFacts(ts *ast.TypeSpec, decl *ast.GenDecl) {
	declared := false
	for _, cg := range []*ast.CommentGroup{ts.Doc, ts.Comment, decl.Doc} {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			pc.extractAnnotations(c.Text, map[string]func(string){
				checkLocksLockType: func(string) { declared = true },
			})
		}
	}
	if !declared {
		return
	}
	obj, ok := pc.pass.TypesInfo.Defs[ts.Name].(*types.TypeName)
	if !ok {
		return
	}
	named, ok := types.Unalias(obj.Type()).(*types.Named)
	if !ok {
		pc.maybeFail(ts.Pos(), "annotation %s is only valid on named types", checkLocksLockType)
		return
	}
	// Whether the type offers a read lock is a property of the type rather
	// than something to be restated in the annotation: it has an RLock
	// method or it does not.
	read := false
	for i := 0; i < named.NumMethods(); i++ {
		if named.Method(i).Name() == "RLock" {
			read = true
			break
		}
	}
	pc.pass.ExportObjectFact(obj, &lockPrimitiveFacts{Read: read})
}

// lockPrimitiveFor returns the declaration for a type, if it has one.
func (pc *passContext) lockPrimitiveFor(typ types.Type) (*lockPrimitiveFacts, bool) {
	return lockPrimitiveIn(pc.pass, typ)
}

// lockPrimitiveIn returns the declaration for a type in a given pass. The other
// analyzers in this module share it, so that a declared lock is a lock to all
// of them rather than only to the guard analysis.
func lockPrimitiveIn(pass *analysis.Pass, typ types.Type) (*lockPrimitiveFacts, bool) {
	named, ok := namedOf(typ)
	if !ok {
		return nil, false
	}
	obj := named.Obj()
	if obj == nil {
		return nil, false
	}
	var lpf lockPrimitiveFacts
	if !pass.ImportObjectFact(obj, &lpf) {
		return nil, false
	}
	return &lpf, true
}

// isLockTypeIn reports whether a type is a lock: either by the name matching
// that the standard types are recognised by, or by declaration.
func isLockTypeIn(pass *analysis.Pass, typ types.Type) bool {
	s := typ.String()
	if rwMutexRE.MatchString(s) || mutexRE.MatchString(s) || lockerRE.MatchString(s) {
		return true
	}
	_, ok := lockPrimitiveIn(pass, typ)
	return ok
}

// isLockPrimitiveMethod reports whether fn is a lock operation of a declared
// lock primitive.
//
// Only the operations this analysis knows how to interpret count. Any other
// method of the type is an ordinary method and is analyzed as one.
func (pc *passContext) isLockPrimitiveMethod(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	if !isLockOpName(fn.Name()) {
		return false
	}
	_, ok = pc.lockPrimitiveFor(sig.Recv().Type())
	return ok
}

// isLockOpName reports whether a method name is a lock operation this analysis
// interprets. It matches the names handled at the call site in checkFunctionCall.
func isLockOpName(name string) bool {
	switch name {
	case "Lock", "NestedLock", "RLock", "Unlock", "NestedUnlock", "RUnlock", "DowngradeLock":
		return true
	}
	return false
}
