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
