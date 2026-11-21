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

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// lockOp is the kind of lock operation a call performs.
type lockOp int

const (
	opNone lockOp = iota
	opAcquire
	opRelease
	opDowngrade
)

// lockOpName reports what an operation of this name does to a lock.
func lockOpName(name string) lockOp {
	switch name {
	case "Lock", "RLock", "NestedLock":
		return opAcquire
	case "Unlock", "RUnlock", "NestedUnlock":
		return opRelease
	case "DowngradeLock":
		return opDowngrade
	}
	return opNone
}

// lockOpShared reports whether an operation of this name takes or drops the read side.
func lockOpShared(name string) bool {
	return name == "RLock" || name == "RUnlock"
}

// declaredLockMethod reports whether fn is a method of a declared lock primitive.
func declaredLockMethod(pass *analysis.Pass, fn *types.Func) bool {
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		_, declared := lockPrimitiveIn(pass, sig.Recv().Type())
		return declared
	}
	return false
}

// isStandardLock reports whether the function is a lock method of a standard lock type,
// recognised by name the way the checklocks analyzer recognises them.
func isStandardLock(pass *analysis.Pass, fn *types.Func) bool {
	name := fn.FullName()
	if mutexRE.MatchString(name) || lockerRE.MatchString(name) {
		return true
	}
	return declaredLockMethod(pass, fn)
}

// lockCall reports what a call of fn does to a lock, when fn is a lock method.
func lockCall(pass *analysis.Pass, fn *types.Func) lockOp {
	if op := lockOpName(fn.Name()); op != opNone && isStandardLock(pass, fn) {
		return op
	}
	return opNone
}

// lockCallSite recognises a lock operation: its callee, what it does, and the lock it is
// done to. ok is false for any other call.
func lockCallSite(pass *analysis.Pass, common *ssa.CallCommon) (fn *types.Func, op lockOp, recv ssa.Value, ok bool) {
	if common.Method != nil {
		fn = common.Method
		recv = common.Value
	} else {
		if sf, isFn := common.Value.(*ssa.Function); isFn && sf.Object() != nil {
			fn, _ = sf.Object().(*types.Func)
		}
		if len(common.Args) > 0 {
			recv = common.Args[0]
		}
	}
	if fn == nil {
		return nil, opNone, nil, false
	}
	op = lockCall(pass, fn)
	return fn, op, recv, recv != nil && op != opNone
}

// isLockAcquire reports whether the call acquires a lock.
func isLockAcquire(pass *analysis.Pass, common *ssa.CallCommon) bool {
	_, op, _, ok := lockCallSite(pass, common)
	return ok && op == opAcquire
}
