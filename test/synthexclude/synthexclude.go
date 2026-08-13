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

// Package synthexclude is the corpus for derived exclusions.
//
// A method that takes its own receiver's lock cannot be called by anyone
// holding it. None of the methods below say so; it is derived from what they
// do.
package synthexclude

import "sync"

type target struct {
	mu sync.RWMutex
	// +checklocks:mu
	value int

	other *target
}

// selfLocking takes the write lock. No annotation.
func (t *target) selfLocking() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.value++
}

// selfRLocking takes the read lock. No annotation.
func (t *target) selfRLocking() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.value
}

// viaCallee reaches the lock through a method of the same receiver.
func (t *target) viaCallee() { t.selfLocking() }

// viaTwoCallees reaches it one step further out.
func (t *target) viaTwoCallees() { t.viaCallee() }

// locksAnother takes a different object's lock, which says nothing about this
// receiver's.
func (t *target) locksAnother() {
	t.other.mu.Lock()
	t.other.mu.Unlock()
}

// requiresHeld is declared to run with the lock held, so it does not acquire
// it and must not be given the opposite meaning.
//
// +checklocks:t.mu
func (t *target) requiresHeld() { t.value++ }

// The write lock excludes a caller holding it in any mode.

func callSelfLockingUnlocked(t *target) {
	t.selfLocking()
}

func callSelfLockingHeld(t *target) {
	t.mu.Lock()
	t.selfLocking() // +checklocksfail=must not hold
	t.mu.Unlock()
}

func callSelfLockingRHeld(t *target) {
	t.mu.RLock()
	t.selfLocking() // +checklocksfail=must not hold
	t.mu.RUnlock()
}

// The read lock excludes only a caller holding it for writing.

func callSelfRLockingUnlocked(t *target) int {
	return t.selfRLocking()
}

func callSelfRLockingRHeld(t *target) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.selfRLocking()
}

func callSelfRLockingHeld(t *target) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.selfRLocking() // +checklocksfail=must not hold
}

// The derivation travels through callees.

func callViaCallee(t *target) {
	t.mu.Lock()
	t.viaCallee() // +checklocksfail=must not hold
	t.mu.Unlock()
}

func callViaTwoCallees(t *target) {
	t.mu.Lock()
	t.viaTwoCallees() // +checklocksfail=must not hold
	t.mu.Unlock()
}

// A method that locks another object is not excluded on this one.

func callLocksAnother(t *target) {
	t.mu.Lock()
	t.locksAnother()
	t.mu.Unlock()
}

// A method declared to need the lock is called with it held, as declared.

func callRequiresHeld(t *target) {
	t.mu.Lock()
	t.requiresHeld()
	t.mu.Unlock()
}

func callRequiresHeldUnlocked(t *target) {
	t.requiresHeld() // +checklocksfail=must hold
}

// A closure that captures the receiver makes the ssa builder spill it to a
// local, so an acquisition written plainly in the method reaches the lock
// through that local rather than through the parameter. The lock is the same
// one, and the method is still self locking.

func (t *target) locksWithCapturingClosure() {
	defer func() {
		_ = t.other
	}()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.value++
}

func callLocksWithCapturingClosure(t *target) {
	t.mu.Lock()
	t.locksWithCapturingClosure() // +checklocksfail=must not hold
	t.mu.Unlock()
}

// The acquisition may be inside the closure itself.

func (t *target) locksInsideClosure() {
	f := func() {
		t.mu.Lock()
		t.value++
		t.mu.Unlock()
	}
	f()
}

func callLocksInsideClosure(t *target) {
	t.mu.Lock()
	t.locksInsideClosure() // +checklocksfail=must not hold
	t.mu.Unlock()
}

func (t *target) locksInsideDeferredClosure() {
	defer func() {
		t.mu.Lock()
		t.value++
		t.mu.Unlock()
	}()
}

func callLocksInsideDeferredClosure(t *target) {
	t.mu.Lock()
	t.locksInsideDeferredClosure() // +checklocksfail=must not hold
	t.mu.Unlock()
}

// A closure this method does not run is not this method's acquisition.

// locksOnGoroutine hands the lock to a new goroutine. A caller holding it is
// raced with, not deadlocked, so the exclusion is not derived.
func (t *target) locksOnGoroutine() {
	go func() {
		t.mu.Lock()
		t.value++
		t.mu.Unlock()
	}()
}

func callLocksOnGoroutine(t *target) {
	t.mu.Lock()
	t.locksOnGoroutine()
	t.mu.Unlock()
}

// returnsLockingClosure builds a closure and hands it back. Whoever runs it
// does so at a time this cannot see, so the exclusion is not derived here.
func (t *target) returnsLockingClosure() func() {
	return func() {
		t.mu.Lock()
		t.value++
		t.mu.Unlock()
	}
}

func callReturnsLockingClosure(t *target) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.returnsLockingClosure()
}
