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

	"golang.org/x/tools/go/ssa"
)

// lockOp is the kind of lock operation a call performs.
type lockOp int

const (
	opNone lockOp = iota
	opAcquire
	opRelease
)

// lockOpName reports what an operation of this name does to a lock.
//
// Only the operations that change whether a lock is held matter here: a downgrade leaves
// the lock held and does not change the class held, so it is not an event for this
// analysis.
func lockOpName(name string) lockOp {
	switch name {
	case "Lock", "RLock", "NestedLock":
		return opAcquire
	case "Unlock", "RUnlock", "NestedUnlock":
		return opRelease
	}
	return opNone
}

// isStandardLock reports whether the function is a lock method of a standard lock type,
// recognised by name the way the checklocks analyzer recognises them.
func isStandardLock(fn *types.Func) bool {
	name := fn.FullName()
	return mutexRE.MatchString(name) || lockerRE.MatchString(name)
}

// classOf resolves the lock class of the object whose lock is being operated on.
//
// The value passed in is the receiver of the lock call. A lock is reached in one of two
// shapes, and the class is declared on the type that OWNS the lock in both:
//
//   - x.mu.Lock(), and the promoted x.Lock() of an embedded lock, reach the lock through a
//     field of x, so the owner is the type of x.
//   - x.Lock() on a type that wraps its own lock has the owner as the receiver itself.
//
// The empty string means the lock does not participate in the order.
func (pc *lockOrderContext) classOf(v ssa.Value) string {
	// The lock reached through a field: the owner is the type holding the field.
	if fa, ok := underlyingFieldAddr(v); ok {
		if named, fieldName, ok := ownerOf(fa); ok {
			if class := pc.classForNamed(named, fieldName); class != "" {
				return class
			}
		}
	}
	// The receiver is the owner: a type that wraps its own lock and forwards to it.
	if named, ok := namedOf(v.Type()); ok {
		return pc.classForNamed(named, "")
	}
	return ""
}

// classForNamed looks up the class declared on a type. A type carrying more than one lock
// names the field its class applies to; fieldName is empty when the caller cannot say which
// field was used, in which case only an unqualified declaration matches.
func (pc *lockOrderContext) classForNamed(named *types.Named, fieldName string) string {
	obj := named.Obj()
	if obj == nil {
		return ""
	}
	var cf classFact
	if !pc.pass.ImportObjectFact(obj, &cf) {
		return ""
	}
	if cf.Field != "" && cf.Field != fieldName {
		return ""
	}
	return cf.Class
}

// namedOf unwraps a type to the named type underneath it.
func namedOf(t types.Type) (*types.Named, bool) {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	named, ok := t.(*types.Named)
	return named, ok
}

// underlyingFieldAddr unwraps the conversions a lock receiver can be wrapped in and returns
// the field access that reached it.
func underlyingFieldAddr(v ssa.Value) (*ssa.FieldAddr, bool) {
	for {
		switch t := v.(type) {
		case *ssa.FieldAddr:
			return t, true
		case *ssa.ChangeType:
			v = t.X
		case *ssa.Convert:
			v = t.X
		case *ssa.MakeInterface:
			v = t.X
		case *ssa.UnOp:
			// A dereference on the way to the field, as produced by a value receiver.
			v = t.X
		default:
			return nil, false
		}
	}
}

// ownerOf returns the named struct type the field belongs to and the field name.
func ownerOf(fa *ssa.FieldAddr) (*types.Named, string, bool) {
	typ := fa.X.Type()
	// The field is reached through a pointer to the struct in every form the SSA builder
	// produces for a lock call.
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok {
		return nil, "", false
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok || fa.Field >= st.NumFields() {
		return nil, "", false
	}
	return named, st.Field(fa.Field).Name(), true
}

// receiverOf returns the value a lock call operates on.
func receiverOf(call ssa.CallInstruction) (ssa.Value, bool) {
	common := call.Common()
	if common.Method != nil {
		// An interface dispatch on sync.Locker: the receiver is the interface value.
		return common.Value, true
	}
	if len(common.Args) == 0 {
		return nil, false
	}
	return common.Args[0], true
}

// isReceiver reports whether a value is the receiver of the function it appears in.
//
// It answers "is this the object the method was called on", which is what scopes a release
// to the receiver: the lock a method drops with recv.mu.Unlock() is its caller's lock,
// while the lock of some other object of the same class is not.
func isReceiver(fn *ssa.Function, v ssa.Value) bool {
	if fn == nil || v == nil || fn.Signature.Recv() == nil || len(fn.Params) == 0 {
		return false
	}
	return rootValue(v) == fn.Params[0]
}

// rootValue unwraps the field accesses and conversions on the way to a value.
func rootValue(v ssa.Value) ssa.Value {
	for {
		switch t := v.(type) {
		case *ssa.FieldAddr:
			v = t.X
		case *ssa.Field:
			v = t.X
		case *ssa.ChangeType:
			v = t.X
		case *ssa.Convert:
			v = t.X
		case *ssa.MakeInterface:
			v = t.X
		case *ssa.UnOp:
			v = t.X
		default:
			return v
		}
	}
}

// staticCallee returns the function a call statically dispatches to, if any.
//
// Interface dispatch has no callee to consult, so it is skipped: that is the same boundary
// the checklocks analyzer has, and the fix on both sides is to annotate the implementation.
func staticCallee(call ssa.CallInstruction) *ssa.Function {
	return call.Common().StaticCallee()
}

// funcObject returns the object of a function, which is the key its facts hang off. A
// closure has no object.
func funcObject(fn *ssa.Function) *types.Func {
	if fn == nil {
		return nil
	}
	obj, _ := fn.Object().(*types.Func)
	return obj
}

// isSelfRecursive reports whether a call targets the function containing it.
func isSelfRecursive(fn *ssa.Function, callee *ssa.Function) bool {
	return fn != nil && callee != nil && fn == callee
}

// displayName renders a callee for a diagnostic.
//
// The package path is dropped: the position already says which file the call is in, and the
// qualified name is long enough to bury the part that matters.
func displayName(fn *ssa.Function) string {
	if fn == nil {
		return "an indirect call"
	}
	name := fn.String()
	if recv := fn.Signature.Recv(); recv != nil {
		return "(" + shortType(recv.Type()) + ")." + fn.Name()
	}
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// shortType renders a receiver type without its package path.
func shortType(t types.Type) string {
	s := t.String()
	star := ""
	if strings.HasPrefix(s, "*") {
		star = "*"
		s = s[1:]
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return star + s
}
