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
func isStandardLock(pass *analysis.Pass, fn *types.Func) bool {
	name := fn.FullName()
	if mutexRE.MatchString(name) || lockerRE.MatchString(name) {
		return true
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		if _, declared := lockPrimitiveIn(pass, sig.Recv().Type()); declared {
			return true
		}
	}
	return false
}

// isLockAcquire reports whether the call acquires a lock.
func isLockAcquire(pass *analysis.Pass, common *ssa.CallCommon) bool {
	var fn *types.Func
	if common.Method != nil {
		fn = common.Method
	} else if sf, ok := common.Value.(*ssa.Function); ok && sf.Object() != nil {
		fn, _ = sf.Object().(*types.Func)
	}
	return fn != nil && lockOpName(fn.Name()) == opAcquire && isStandardLock(pass, fn)
}
