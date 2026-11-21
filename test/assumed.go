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

package test

// +checklocks:t.mu
func assumedNeedsLock(t *oneGuardStruct) {
	t.guardedField = 1
}

// +checklocks:t.mu
func assumedNeedsWrite(t *oneReadGuardStruct) {
	t.guardedField = 1
}

// +checklocksread:t.mu
func assumedNeedsRead(t *oneReadGuardStruct) {
	_ = t.guardedField
}

// +checklocksacquire:t.mu
func assumedAcquires(t *oneGuardStruct) {
	t.mu.Lock()
}

// A forced access asserts the lock, so the return leaves nothing held.
func testAssumedForcedAccess(t *oneGuardStruct) {
	t.guardedField = 1 // +checklocksforce
	return
}

// The same at a forced call to a function that keeps the lock.
func testAssumedForcedCall(t *oneGuardStruct) {
	assumedNeedsLock(t) // +checklocksforce
	return
}

// A return with a lock assumed is the same return state as one without.
func testAssumedReturnStates(t *oneGuardStruct, x int) {
	if x > 0 {
		t.guardedField = 1 // +checklocksforce
		return
	}
	return
}

// An assumed lock is held for the memo, so the access after the join is checked.
func testAssumedOneBranch(t *oneGuardStruct, x int) {
	if x > 0 {
		t.guardedField = 1 // +checklocksforce
	}
	t.guardedField = 2 // +checklocksfail
	return
}

// A forced call wanting the lock exclusively does not retag the shared lock held.
func testAssumedForcedUpgrade(t *oneReadGuardStruct) {
	t.mu.RLock()
	assumedNeedsWrite(t) // +checklocksforce
	return               // +checklocksfail=return with unexpected locks held
}

// A forced write to a field the shared lock guards does not retag it either.
func testAssumedForcedWrite(t *oneReadGuardStruct) {
	t.mu.RLock()
	t.guardedField = 1 // +checklocksforce
	return             // +checklocksfail=return with unexpected locks held
}

// A lock taken after a force is taken, so it is left held.
func testAssumedThenLock(t *oneGuardStruct) {
	t.guardedField = 1 // +checklocksforce
	t.mu.Lock()        // +checklocksfail=already locked
	return             // +checklocksfail=return with unexpected locks held
}

// The same for a lock an acquire function takes.
func testAssumedThenAcquireCall(t *oneGuardStruct) {
	t.guardedField = 1 // +checklocksforce
	assumedAcquires(t) // +checklocksfail=attempt to acquire
	return             // +checklocksfail=return with unexpected locks held
}

// A promotion on one branch does not reach the other.
func testAssumedBranchPromotion(t *oneGuardStruct, x int) { // +checklocksforce
	t.guardedField = 1 // +checklocksforce
	if x > 0 {
		t.mu.Lock() // +checklocksfail=already locked
		return      // +checklocksfail=return with unexpected locks held
	}
	return
}

// A block entered with the lock assumed is no cover for entering it with the lock held.
func testAssumedMemoTag(t *oneGuardStruct, x int) { // +checklocksforce
	if x > 0 {
		t.guardedField = 1 // +checklocksforce
	} else {
		t.mu.Lock()
	}
	t.guardedField = 2
	return // +checklocksfail=return with unexpected locks held
}

// Intended: the path that took the lock leaks it, the path that assumed it does not.
func testAssumedOneBranchLock(t *oneGuardStruct, c bool) { // +checklocksfail=incompatible return states
	if c {
		t.mu.Lock()
	}
	t.guardedField = 1 // +checklocksforce
	return             // +checklocksfail=return with unexpected locks held
}

// The lock an acquire function hands back may be one a force asserted.
//
// +checklocksacquire:t.mu
func testAssumedForcedAcquire(t *oneGuardStruct) {
	t.guardedField = 1 // +checklocksforce
	return
}

// Or one asserted by the force at the return itself.
//
// +checklocksacquire:t.mu
func testAssumedForcedExit(t *oneGuardStruct) {
	return // +checklocksforce
}

// Or one assumed shared before a forced return wants it exclusively.
//
// +checklocksacquire:t.mu
func testAssumedSharedForcedExit(t *oneReadGuardStruct) {
	assumedNeedsRead(t) // +checklocksforce
	return              // +checklocksforce
}
