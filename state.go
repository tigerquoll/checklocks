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

package checklocks

import (
	"fmt"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"
	"sync/atomic"

	"golang.org/x/tools/go/ssa"
)

// lockInfo describes a held lock.
type lockInfo struct {
	exclusive bool
	object    types.Object

	// assumed marks a lock held by assertion rather than by acquisition: a force
	// or an ignore inserted it, so a return does not count it as left held.
	assumed bool
}

// lockState tracks the locking state and aliases.
type lockState struct {
	// lockedMutexes is used to track which mutexes in a given struct are
	// currently locked. Note that most of the heavy lifting is done by
	// valueAndObject below, which maps to specific structure fields, etc.
	//
	// The value indicates whether this is an exclusive lock.
	lockedMutexes map[string]lockInfo

	aliases map[string]string

	// stored stores values that have been stored in memory, bound to
	// FreeVars or passed as Parameterse.
	stored map[ssa.Value]ssa.Value

	// used is a temporary map, used only for valueAndObject. It prevents
	// multiple use of the same memory location.
	used map[ssa.Value]struct{}

	// substitutions overrides the resolution of a value. It is used for
	// the result of a call to a function that returns a package-level
	// variable, which resolves to that variable. See accessor.go.
	substitutions map[ssa.Value]substitution

	// consts names the constants reached by the functions analyzed so far.
	// It is read-only here and shared by every fork; see nameConstants.
	consts map[*ssa.Const]string

	// defers are the stack of defers that have been pushed.
	defers []*ssa.Defer

	// refs indicates the number of references on this structure. If it's
	// greater than one, we will do copy-on-write.
	refs *int32
}

// newLockState makes a new lockState.
func newLockState() *lockState {
	refs := int32(1) // Not shared.
	return &lockState{
		lockedMutexes: make(map[string]lockInfo),
		aliases:       make(map[string]string),
		used:          make(map[ssa.Value]struct{}),
		stored:        make(map[ssa.Value]ssa.Value),
		substitutions: make(map[ssa.Value]substitution),
		defers:        make([]*ssa.Defer, 0),
		refs:          &refs,
	}
}

// fork forks the locking state. When a lockState is forked, any modifications
// will cause maps to be copied.
func (l *lockState) fork() *lockState {
	if l == nil {
		return newLockState()
	}
	atomic.AddInt32(l.refs, 1)
	return &lockState{
		lockedMutexes: l.lockedMutexes,
		aliases:       l.aliases,
		used:          make(map[ssa.Value]struct{}),
		stored:        l.stored,
		substitutions: l.substitutions,
		consts:        l.consts,
		defers:        l.defers,
		refs:          l.refs,
	}
}

// modify indicates that this state will be modified.
func (l *lockState) modify() {
	if atomic.LoadInt32(l.refs) > 1 {
		// Copy the lockedMutexes.
		l.lockedMutexes = maps.Clone(l.lockedMutexes)

		// Copy the aliases.
		l.aliases = maps.Clone(l.aliases)

		// Copy the stored values.
		l.stored = maps.Clone(l.stored)

		// Copy the substitutions.
		l.substitutions = maps.Clone(l.substitutions)

		// Reset the used values.
		clear(l.used)

		// Copy the defers.
		l.defers = slices.Clone(l.defers)

		// Drop our reference.
		atomic.AddInt32(l.refs, -1)
		newRefs := int32(1) // Not shared.
		l.refs = &newRefs
	}
}

// isHeld indicates whether the field is held is not.
//
// Precondition: rv must be valid.
func (l *lockState) isHeld(rv resolvedValue, exclusiveRequired bool) (string, bool) {
	if !rv.valid() {
		panic("invalid resolvedValue passed to isHeld")
	}
	s, _ := rv.valueAndObject(l)
	s = l.aliasKey(s)
	info, ok := l.lockedMutexes[s]
	if !ok {
		return s, false
	}
	// Accept a weaker lock if exclusiveRequired is false.
	if exclusiveRequired && !info.exclusive {
		return s, false
	}
	return s, true
}

// lockField locks the given field.
//
// If false is returned, the field was already locked.
//
// Precondition: rv must be valid.
func (l *lockState) lockField(rv resolvedValue, exclusive bool) (string, bool) {
	if !rv.valid() {
		panic("invalid resolvedValue passed to isHeld")
	}
	s, obj := rv.valueAndObject(l)
	s = l.aliasKey(s)
	if _, ok := l.lockedMutexes[s]; ok {
		return s, false
	}
	l.modify()
	l.lockedMutexes[s] = lockInfo{
		exclusive: exclusive,
		object:    obj,
	}
	return s, true
}

// unlockField unlocks the given field.
//
// If false is returned, the field was not locked.
//
// Precondition: rv must be valid.
func (l *lockState) unlockField(rv resolvedValue, exclusive bool) (string, bool) {
	if !rv.valid() {
		panic("invalid resolvedValue passed to isHeld")
	}
	s, _ := rv.valueAndObject(l)
	s = l.aliasKey(s)
	info, ok := l.lockedMutexes[s]
	if !ok {
		return s, false
	}
	if info.exclusive != exclusive {
		return s, false
	}
	l.modify()
	delete(l.lockedMutexes, s)
	return s, true
}

// downgradeField downgrades the given field.
//
// If false was returned, the field was not downgraded.
//
// Precondition: rv must be valid.
func (l *lockState) downgradeField(rv resolvedValue) (string, bool) {
	if !rv.valid() {
		panic("invalid resolvedValue passed to isHeld")
	}
	s, _ := rv.valueAndObject(l)
	s = l.aliasKey(s)
	info, ok := l.lockedMutexes[s]
	if !ok {
		return s, false
	}
	if !info.exclusive {
		return s, false
	}
	l.modify()
	info.exclusive = false
	l.lockedMutexes[s] = info // Downgraded.
	return s, true
}

// setAssumed marks a held lock as assumed or as acquired. The map is shared with the
// block memo and with sibling branches, so the write copies it first.
func (l *lockState) setAssumed(s string, assumed bool) {
	info, ok := l.lockedMutexes[s]
	if !ok || info.assumed == assumed {
		return
	}
	l.modify()
	info.assumed = assumed
	l.lockedMutexes[s] = info
}

// store records an alias.
func (l *lockState) store(addr ssa.Value, v ssa.Value) {
	l.modify()
	l.stored[addr] = v
}

// substitution is a resolution override for a single value.
type substitution struct {
	key string
	obj types.Object
}

// substitute records that v resolves to the given key and object.
func (l *lockState) substitute(v ssa.Value, key string, obj types.Object) {
	l.modify()
	l.substitutions[v] = substitution{key: key, obj: obj}
}

func (l *lockState) addAlias(left, right resolvedValue) {
	leftKey, _ := left.valueAndObject(l)
	rightKey, _ := right.valueAndObject(l)
	leftRoot := l.aliasKey(leftKey)
	rightRoot := l.aliasKey(rightKey)
	if leftRoot == rightRoot {
		return
	}
	l.modify()
	l.aliases[leftRoot] = rightRoot
	if leftInfo, ok := l.lockedMutexes[leftRoot]; ok {
		if rightInfo, ok := l.lockedMutexes[rightRoot]; ok {
			switch {
			case leftInfo.assumed && !rightInfo.assumed:
				leftInfo = rightInfo
			case leftInfo.assumed == rightInfo.assumed:
				leftInfo.exclusive = leftInfo.exclusive || rightInfo.exclusive
			}
		}
		l.lockedMutexes[rightRoot] = leftInfo
		delete(l.lockedMutexes, leftRoot)
	}
}

func (l *lockState) aliasKey(key string) string {
	for {
		parent, ok := l.aliases[key]
		if !ok {
			return key
		}
		key = parent
	}
}

// isSubset indicates other holds all the locks held by l.
func (l *lockState) isSubset(other *lockState) bool {
	for k, info := range l.lockedMutexes {
		otherInfo, otherOk := other.lockedMutexes[k]
		if !otherOk {
			return false
		}
		// Accept weaker locks as a subset.
		if info.exclusive && !otherInfo.exclusive {
			return false
		}
		// An assumed lock and an acquired one part ways at the return, so a block
		// reached with one is not covered by a walk that reached it with the other.
		if info.assumed != otherInfo.assumed {
			return false
		}
	}
	return true
}

// count indicates the number of locks held.
func (l *lockState) count() int {
	return len(l.lockedMutexes)
}

// countOnReturn counts the locks a return leaves held. An assumed lock was never
// acquired, and a lock in entry arrived with the caller of an inline body, so
// neither is this return's to release.
func (l *lockState) countOnReturn(entry map[string]bool) int {
	n := 0
	for k, info := range l.lockedMutexes {
		if info.assumed {
			continue
		}
		// A lock whose mode the body changed is not the one it was entered with.
		if exclusive, ok := entry[k]; ok && exclusive == info.exclusive {
			continue
		}
		n++
	}
	return n
}

// isCompatible returns true if the states are compatible.
func (l *lockState) isCompatible(other *lockState) bool {
	return l.isSubset(other) && other.isSubset(l)
}

// elemType is a type that implements the Elem function.
type elemType interface {
	Elem() types.Type
}

// valueAndObject returns a string for a given value, along with a source level
// object (if available and relevant).
//
// This decomposes the value into the simplest possible representation in terms
// of parameters, free variables and globals. During resolution, stored values
// may be transferred, as well as bound free variables.
//
// Nil may not be passed here.
func (l *lockState) valueAndObject(v ssa.Value) (string, types.Object) {
	// An overridden value resolves to whatever it was substituted with,
	// regardless of its shape. See accessor.go.
	if s, ok := l.substitutions[v]; ok {
		return s.key, s.obj
	}
	switch x := v.(type) {
	case *ssa.Parameter:
		// Was this provided as a parameter for a local anonymous
		// function invocation?
		v, ok := l.stored[x]
		if ok {
			return l.valueAndObject(v)
		}
		return fmt.Sprintf("{param:%s}", x.Name()), x.Object()
	case *ssa.Global:
		return fmt.Sprintf("{global:%s}", x.Name()), x.Object()
	case *ssa.FreeVar:
		// Attempt to resolve this, in case we are being invoked in a
		// scope where all the variables are bound.
		v, ok := l.stored[x]
		if ok {
			// The FreeVar is typically bound to a location, so we
			// check what's been stored there. Note that the second
			// may map to the same FreeVar, which we can check.
			stored, ok := l.stored[v]
			if ok {
				return l.valueAndObject(stored)
			}
		}
		// FreeVar does not have a corresponding source-level object
		// that we can return here.
		return fmt.Sprintf("{freevar:%s}", x.Name()), nil
	case *ssa.Convert:
		// Just disregard conversion.
		return l.valueAndObject(x.X)
	case *ssa.ChangeType:
		// Ditto, disregard.
		return l.valueAndObject(x.X)
	case *ssa.UnOp:
		if x.Op != token.MUL {
			break
		}
		// Is this loading a free variable? If yes, then this can be
		// resolved in the original isAlias function.
		if fv, ok := x.X.(*ssa.FreeVar); ok {
			return l.valueAndObject(fv)
		}
		// Should be try to resolve via a memory address? This needs to
		// be done since a memory location can hold its own value.
		if _, ok := l.used[x.X]; !ok {
			// Check if we know what the accessed location holds.
			// This is used to disambiguate memory locations.
			v, ok := l.stored[x.X]
			if ok {
				l.used[x.X] = struct{}{}
				defer func() { delete(l.used, x.X) }()
				return l.valueAndObject(v)
			}
		}
		// x.X.Type is pointer. We must construct this type
		// dynamically, since the ssa.Value could be synthetic.
		s, obj := l.valueAndObject(x.X)
		return fmt.Sprintf("*(%s)", s), obj
	case *ssa.Field:
		structType, ok := resolveStruct(x.X.Type())
		if !ok {
			// This should not happen.
			panic(fmt.Sprintf("structType not available for struct: %#v", x.X))
		}
		fieldObj := structType.Field(x.Field)
		s, _ := l.valueAndObject(x.X)
		return fmt.Sprintf("%s.%s", s, fieldObj.Name()), fieldObj
	case *ssa.FieldAddr:
		structType, ok := resolveStruct(x.X.Type())
		if !ok {
			// This should not happen.
			panic(fmt.Sprintf("structType not available for struct: %#v", x.X))
		}
		fieldObj := structType.Field(x.Field)
		s, _ := l.valueAndObject(x.X)
		return fmt.Sprintf("&(%s.%s)", s, fieldObj.Name()), fieldObj
	case *ssa.Index:
		s, _ := l.valueAndObject(x.X)
		i, _ := l.valueAndObject(x.Index)
		return fmt.Sprintf("%s[%s]", s, i), nil
	case *ssa.IndexAddr:
		s, _ := l.valueAndObject(x.X)
		i, _ := l.valueAndObject(x.Index)
		return fmt.Sprintf("&(%s[%s])", s, i), nil
	case *ssa.Lookup:
		s, _ := l.valueAndObject(x.X)
		i, _ := l.valueAndObject(x.Index)
		return fmt.Sprintf("%s[%s]", s, i), nil
	case *ssa.Extract:
		s, _ := l.valueAndObject(x.Tuple)
		return fmt.Sprintf("%s[%d]", s, x.Index), nil
	}

	// Any other value (an alloc, a return value, ...) is named by its place in the
	// ssa graph, which is unique and the same in every run.
	return l.valueToken(v), nil
}

// valueToken names a value that decomposes no further.
//
// The name is the identity of a lock rooted at the value, so it must be distinct for
// distinct values, and it is printed in diagnostics, so it must not vary between runs.
func (l *lockState) valueToken(v ssa.Value) string {
	if c, ok := v.(*ssa.Const); ok {
		if token, ok := l.consts[c]; ok {
			return token
		}
		// A constant no analyzed function reached is named by its value, which can
		// merge two of them; nameConstants covers every function that is walked.
		return fmt.Sprintf("{%T:%s}", v, c.String())
	}
	if inst, ok := v.(ssa.Instruction); ok && inst.Block() == nil {
		// Parent panics on an instruction outside a block, which a built function
		// has none of; the position is all there is to name such a value by.
		return fmt.Sprintf("{%T:%d}", v, int(v.Pos()))
	}
	if fn := v.Parent(); fn != nil {
		// A register is unique only within its function: without the function, a
		// closure's t2 would be the same lock as its caller's t2.
		return fmt.Sprintf("{%T:%s:%s}", v, relFuncName(fn), v.Name())
	}
	// A top-level function or a builtin belongs to no function, and its own name is
	// already unique.
	return fmt.Sprintf("{%T:%s}", v, v.String())
}

// nameConstants names fn's constants by order of first use, the same in every run. A lifted
// local's constant serves all its uses, so naming by site would split one lock. Names a caller
// recorded are kept: a constant passed into a closure is one constant.
func (l *lockState) nameConstants(fn *ssa.Function) {
	var (
		stackLocal [16]*ssa.Value
		named      map[*ssa.Const]string
	)
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			for _, v := range inst.Operands(stackLocal[:0]) {
				if v == nil {
					continue
				}
				c, ok := (*v).(*ssa.Const)
				if !ok {
					continue
				}
				if _, ok := l.consts[c]; ok {
					continue
				}
				if _, ok := named[c]; ok {
					continue
				}
				if named == nil {
					named = make(map[*ssa.Const]string)
				}
				named[c] = fmt.Sprintf("{%T:%s:k%d}", c, relFuncName(fn), len(named))
			}
		}
	}
	if named == nil {
		return
	}
	// The table is shared with every state forked from this one, so it is replaced
	// rather than written to.
	consts := make(map[*ssa.Const]string, len(l.consts)+len(named))
	maps.Copy(consts, l.consts)
	maps.Copy(consts, named)
	l.consts = consts
}

// relFuncName names a function relative to its own package, which keeps the package path
// out of the name without dropping the receiver or the enclosing function.
func relFuncName(fn *ssa.Function) string {
	if fn.Pkg != nil {
		return fn.RelString(fn.Pkg.Pkg)
	}
	return fn.String()
}

// String returns the full lock state.
func (l *lockState) String() string {
	if l.count() == 0 {
		return "no locks held"
	}
	keys := make([]string, 0, len(l.lockedMutexes))
	for k, info := range l.lockedMutexes {
		// Include the exclusive status of each lock.
		keys = append(keys, fmt.Sprintf("%s %s", k, exclusiveStr(info.exclusive)))
	}
	slices.Sort(keys) // The map order is not an order; the text must be one.
	return strings.Join(keys, ",")
}

// acquiredString renders the locks a return state is compared by, in String()'s format.
// An assumed lock is left out unless an inline body's entry held it in the same mode.
func (l *lockState) acquiredString(entry map[string]bool) string {
	held := make(map[string]lockInfo, len(l.lockedMutexes))
	for k, info := range l.lockedMutexes {
		if exclusive, ok := entry[k]; !info.assumed || ok && exclusive == info.exclusive {
			held[k] = info
		}
	}
	return (&lockState{lockedMutexes: held}).String()
}

// pushDefer pushes a defer onto the stack.
func (l *lockState) pushDefer(d *ssa.Defer) {
	l.modify()
	l.defers = append(l.defers, d)
}

// popDefer pops a defer from the stack.
func (l *lockState) popDefer() *ssa.Defer {
	// Does not technically modify the underlying slice.
	count := len(l.defers)
	if count == 0 {
		return nil
	}
	d := l.defers[count-1]
	l.defers = l.defers[:count-1]
	return d
}
