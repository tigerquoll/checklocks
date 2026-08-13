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

// Package synthprecond is the corpus for derived preconditions.
//
// A method that uses a guarded field of its receiver without taking the lock
// is not a mistake where it is written: it is a method whose caller must hold
// the lock. None of the methods below say so; it is derived from the guard on
// the field.
package synthprecond

import "sync"

type target struct {
	mu sync.RWMutex
	// +checklocks:mu
	value int

	other *target
}

// reads only reads the guarded field, so a read lock is enough.
func (t *target) reads() int { return t.value }

// writes needs the lock exclusively.
func (t *target) writes() { t.value = 1 }

// viaCallee reaches the field through another method of the same receiver.
func (t *target) viaCallee() int { return t.reads() }

// viaWritingCallee inherits the exclusive requirement.
func (t *target) viaWritingCallee() { t.writes() }

// takesItself acquires the lock, so it assumes nothing; the derived exclusion
// applies to it instead.
func (t *target) takesItself() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.value
}

// usesAnother touches a guarded field of a different object, which says
// nothing about this receiver's lock.
func (t *target) usesAnother() int {
	return t.other.value // +checklocksfail=must be locked
}

// A read requirement is met by either mode, and not by nothing.

func callReadsUnlocked(t *target) int {
	return t.reads() // +checklocksfail=must hold
}

func callReadsRLocked(t *target) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.reads()
}

func callReadsLocked(t *target) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.reads()
}

// A write requirement is met only by the exclusive lock.

func callWritesUnlocked(t *target) {
	t.writes() // +checklocksfail=must hold
}

func callWritesRLocked(t *target) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.writes() // +checklocksfail=must hold
}

func callWritesLocked(t *target) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.writes()
}

// The requirement travels through callees.

func callViaCalleeUnlocked(t *target) int {
	return t.viaCallee() // +checklocksfail=must hold
}

func callViaCalleeRLocked(t *target) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.viaCallee()
}

func callViaWritingCalleeRLocked(t *target) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	t.viaWritingCallee() // +checklocksfail=must hold
}

// A method that takes the lock itself is excluded, not required.

func callTakesItselfUnlocked(t *target) int {
	return t.takesItself()
}

func callTakesItselfLocked(t *target) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.takesItself() // +checklocksfail=must not hold
}

// A guard declared for the structure as a whole is the same guard, so it is
// derived from identically.

// +checklocksguardedby:mu
type declared struct {
	mu sync.RWMutex

	count int
	name  string
}

func (d *declared) readsDeclared() int { return d.count }

func (d *declared) writesDeclared() { d.count = 1 }

func callDeclaredUnlocked(d *declared) int {
	return d.readsDeclared() // +checklocksfail=must hold
}

func callDeclaredRLocked(d *declared) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.readsDeclared()
}

func callDeclaredWriteRLocked(d *declared) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	d.writesDeclared() // +checklocksfail=must hold
}
