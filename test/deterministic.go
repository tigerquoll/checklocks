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

package test

import "sync"

type detStruct struct {
	mu sync.RWMutex

	// +checklocks:mu
	x int
}

type detIndexed struct {
	mus [2]sync.Mutex
	m   map[string]*sync.Mutex
}

// A register belongs to its function; otherwise the closure's unlock would hide the double unlock.
func testDetClosureRegisters() {
	var outer detStruct
	outer.mu.Lock()
	func() {
		var inner detStruct
		inner.mu.Lock()
		inner.mu.Unlock()
	}()
	outer.mu.Unlock()
	outer.mu.Unlock() // +checklocksfail
}

// One lifted constant is one index, so the lock and the unlock are of one lock.
func testDetLiftedIndex(t *detIndexed) {
	i := 0
	t.mus[i].Lock()
	t.mus[i].Unlock()
}

// The same lifted constant twice is the same lock twice.
func testDetLiftedIndexDouble(t *detIndexed) {
	i := 1
	t.mus[i].Lock()
	t.mus[i].Lock() // +checklocksfail
}

// A map key is a constant like any other.
func testDetLiftedKey(t *detIndexed) {
	k := "a"
	t.m[k].Lock()
	t.m[k].Unlock()
}

// Each call passes its own literal, so the two calls lock two different indexes.
func testDetTwoCallsOfOneClosure(t *detIndexed) {
	lk := func(t *detIndexed, i int) { t.mus[i].Lock() }
	lk(t, 0)
	lk(t, 1)
	t.mus[0].Unlock() // +checklocksfail
	t.mus[1].Unlock() // +checklocksfail
}

// A closure's index is the caller's constant, so it is the caller's lock.
func testDetClosureSharesAnIndex(t *detIndexed) {
	i := 0
	lk := func() { t.mus[i].Lock() } // +checklocksfail
	t.mus[i].Lock()
	lk()
}

// Two sites of one literal index are two constants, as they are today.
func testDetLiteralIndex(t *detIndexed) {
	t.mus[0].Lock()
	t.mus[0].Unlock() // +checklocksfail
}

// A map whose entry is replaced between the lock and the unlock.
func testDetReplacedEntry(t *detIndexed, other *sync.Mutex) {
	t.m["a"].Lock()
	t.m["a"] = other
	t.m["a"].Unlock() // +checklocksfail
}

// A nil root is a constant too, and names a lock as it does today.
func testDetNilRoot() {
	(*detStruct)(nil).mu.Lock()
	(*detStruct)(nil).mu.Unlock() // +checklocksfail
}

// The mode is no part of a lock's key, so taking one shared and then exclusively is reported.
func testDetUpgradeInPlace(t *detStruct) {
	t.mu.RLock()
	t.mu.Lock() // +checklocksfail
	t.mu.RUnlock()
}

type detAliasInner struct {
	mu *sync.RWMutex
}

// +checklocksalias:inner.mu=mu
type detAlias struct {
	mu    sync.RWMutex
	inner detAliasInner

	// +checklocks:mu
	x int
}

// Two entry guards name one lock and the exclusive one sorts last, so first-wins fails the write.
//
// +checklocksread:a.inner.mu
// +checklocks:a.mu
func detNeedsBothAliased(a *detAlias) { // +checklocksfail=acquired multiple times or differently (locks: &({param:a}.mu) exclusively)
	a.x = 1
}

// The same two guards at a call this analysis is told to assume.
func testDetForcedAliased(a *detAlias) {
	detNeedsBothAliased(a) // +checklocksforce
	a.x = 2
	a.mu.Unlock()
}

// A lock held non-exclusively before a forced call is not upgraded by it.
func testDetForcedKeepsMode(a *detAlias) {
	a.mu.RLock()
	detNeedsBothAliased(a) // +checklocksforce
	a.x = 3                // +checklocksfail
	a.mu.RUnlock()
}

// The requirement itself still holds: shared is not enough for the exclusive guard.
func testDetCallNeedsExclusive(a *detAlias) {
	a.mu.RLock()
	detNeedsBothAliased(a) // +checklocksfail=must hold a.mu exclusively
	a.mu.RUnlock()
}

// Two acquisitions naming one lock, the exclusive one sorting last.
//
// +checklocksacquireread:a.inner.mu
// +checklocksacquire:a.mu
// +checklocksignore
func detAcquiresBothAliased(a *detAlias) {
	a.mu.Lock()
}

func testDetAcquiredAliased(a *detAlias) {
	detAcquiresBothAliased(a)
	a.x = 4
	a.mu.Unlock()
}

// Two acquisitions naming one lock at a return this analysis is told to assume.
//
// +checklocksacquireread:a.inner.mu
// +checklocksacquire:a.mu
func detExitForcedAliased(a *detAlias) {
	return // +checklocksforce +checklocksfail=return with unexpected locks held (locks: &({param:a}.mu) exclusively)
}

// +checklocksacquire:a.mu
// +checklocksacquire:b.mu
func detAcquiresTwo(a, b *detStruct) {
	a.mu.Lock()
	b.mu.Lock()
}

// One object passed twice is one lock, and taking it twice is a deadlock.
func testDetSameArgumentTwice(t *detStruct) {
	detAcquiresTwo(t, t) // +checklocksfail=already held
	t.mu.Unlock()
}
