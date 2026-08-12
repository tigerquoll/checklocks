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

// Package locktype is the corpus for declared lock primitives.
//
// The wrappers here carry no ignore of any kind. That is the point: an ignore
// on a forwarder suppresses the call site checks for every caller of the lock,
// which is the checking a wrapper used to cost.
package locktype

import "sync"

// Guard wraps a mutex behind a named field, so nothing is promoted and the
// forwarders are the only way in. Its name does not match the mutex pattern,
// so the declaration is the only thing that makes it a lock.
//
// +checklockslocktype
type Guard struct {
	mu sync.Mutex
}

func (g *Guard) Lock()   { g.mu.Lock() }
func (g *Guard) Unlock() { g.mu.Unlock() }

// RWGuard offers a read lock, so it is treated as an RWMutex: a read-only
// access is satisfied by holding it non-exclusively.
//
// +checklockslocktype
type RWGuard struct {
	mu sync.RWMutex
}

func (g *RWGuard) Lock()    { g.mu.Lock() }
func (g *RWGuard) Unlock()  { g.mu.Unlock() }
func (g *RWGuard) RLock()   { g.mu.RLock() }
func (g *RWGuard) RUnlock() { g.mu.RUnlock() }

type guarded struct {
	g Guard
	// +checklocks:g
	value int
}

// The call site checks are back: these are the class the forwarder ignores
// used to suppress.

func doubleLock(x *guarded) {
	x.g.Lock()
	x.g.Lock() // +checklocksfail=already locked
	x.value = 1
	x.g.Unlock()
}

func unlockNotHeld(x *guarded) {
	x.g.Unlock() // +checklocksfail=already unlocked
}

func unlockTwice(x *guarded) {
	x.g.Lock()
	x.value = 1
	x.g.Unlock()
	x.g.Unlock() // +checklocksfail=already unlocked
}

func correct(x *guarded) {
	x.g.Lock()
	x.value = 1
	x.g.Unlock()
}

func unguarded(x *guarded) {
	x.value = 1 // +checklocksfail=must be locked
}

// The read lock behaves as it does for an RWMutex.

type rwGuarded struct {
	g RWGuard
	// +checklocks:g
	value int
}

func readUnderRead(x *rwGuarded) int {
	x.g.RLock()
	defer x.g.RUnlock()
	return x.value
}

func writeUnderRead(x *rwGuarded) {
	x.g.RLock()
	x.value = 1 // +checklocksfail=must be locked
	x.g.RUnlock()
}

func doubleRLock(x *rwGuarded) int {
	x.g.RLock()
	x.g.RLock() // +checklocksfail=already locked
	v := x.value
	x.g.RUnlock()
	return v
}

// A function annotated with the wrapper as its guard, which is the
// convention a Locked helper follows.

// +checklocks:x.g
func writeLocked(x *guarded) {
	x.value = 1
}

func callWriteLocked(x *guarded) {
	x.g.Lock()
	writeLocked(x)
	x.g.Unlock()
}

func callWriteLockedUnlocked(x *guarded) {
	writeLocked(x) // +checklocksfail=must hold
}

// Promoted methods, the other idiom. Embedding leaves the wrapper's own
// methods promoted onto the embedder, and a lock taken through the promoted
// method is the same lock.

type embedder struct {
	Guard
	// +checklocks:Guard
	value int
}

func embeddedCorrect(e *embedder) {
	e.Lock()
	e.value = 1
	e.Unlock()
}

func embeddedDoubleLock(e *embedder) {
	e.Lock()
	e.Lock() // +checklocksfail=already locked
	e.Unlock()
}

func embeddedUnguarded(e *embedder) {
	e.value = 1 // +checklocksfail=must be locked
}

// An undeclared wrapper is not a lock. Its forwarder is an ordinary function
// that takes a lock and does not release it, which is what the declaration
// exists to avoid having to silence.

type plain struct {
	mu sync.Mutex
}

// +checklocksignore
func (p *plain) Lock() { p.mu.Lock() }

// +checklocksignore
func (p *plain) Unlock() { p.mu.Unlock() }
