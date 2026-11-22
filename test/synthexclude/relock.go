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

// Callers of methods that take their own receiver's lock, including by dropping and retaking it.

package synthexclude

import "sync"

type relocker struct {
	mu sync.Mutex
	rw sync.RWMutex

	// +checklocks:mu
	n int

	// +checklocks:rw
	m int

	peer *relocker
}

func work() {}

func (r *relocker) locksMu() {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func callLocksMuHeld(r *relocker) {
	r.mu.Lock()
	r.locksMu() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A spawned method call's acquisition counts (a known false report for a mutex).

func (r *relocker) spawns() { go r.locksMu() }

func callSpawns(r *relocker) {
	r.mu.Lock()
	r.spawns() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A spawned acquisition alongside one the body takes itself.

func (r *relocker) spawnsThenLocks() {
	go r.locksMu()
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func callSpawnsThenLocks(r *relocker) {
	r.mu.Lock()
	r.spawnsThenLocks() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A deferred call runs in the method, under whatever its caller holds.

func (r *relocker) defersCall() { defer r.locksMu() }

func callDefersCall(r *relocker) {
	r.mu.Lock()
	r.defersCall() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A lock dropped and taken back is an acquisition like any other.

func (r *relocker) dropsAndRetakes() {
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	work()
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callDropsAndRetakes(r *relocker) {
	r.mu.Lock()
	r.dropsAndRetakes() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The receiver captured by a closure, which spills it to a local.

func (r *relocker) dropsAndRetakesWithClosure() {
	defer func() { _ = r.peer }()
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	work()
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callDropsAndRetakesWithClosure(r *relocker) {
	r.mu.Lock()
	r.dropsAndRetakesWithClosure() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The release and the retake may have a method of the same receiver between them.

func (r *relocker) dropsAroundCall() {
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	r.locksMu()
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callDropsAroundCall(r *relocker) {
	r.mu.Lock()
	r.dropsAroundCall() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The read lock is dropped and the write lock taken.

func (r *relocker) upgrades() {
	r.rw.RUnlock() // +checklocksfail=already unlocked +lockgapfail=stale
	r.rw.Lock()
} // +checklocksfail=return with unexpected locks held

func callUpgrades(r *relocker) {
	r.rw.RLock()
	r.upgrades() // +checklocksfail=must not hold
	r.rw.RUnlock()
}

// One loop iteration gives the lock up and takes it back.

func (r *relocker) relocksInLoop() {
	for i := 0; i < 3; i++ {
		r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
		work()
		r.mu.Lock()
	}
}

func callRelocksInLoop(r *relocker) {
	r.mu.Lock()
	r.relocksInLoop() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The body takes the read lock itself before it takes the write one.

func (r *relocker) readsThenWrites() {
	r.rw.RLock()
	_ = r.m
	r.rw.RUnlock() // +lockgapfail=stale
	r.rw.Lock()
	r.m++
	r.rw.Unlock()
}

func callReadsThenWrites(r *relocker) {
	r.rw.RLock()
	r.readsThenWrites() // +checklocksfail=must not hold
	r.rw.RUnlock()
}

// A deferred release runs at the return, so what follows it is under the lock.

func (r *relocker) deferredRelease() {
	defer r.mu.Unlock()
	r.mu.Lock()
}

func callDeferredRelease(r *relocker) {
	r.mu.Lock()
	r.deferredRelease() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A release on one path only leaves the acquisition reachable with the lock held.

func (r *relocker) condRelocks(c bool) {
	if c {
		r.mu.Unlock() // +checklocksfail=already unlocked
	}
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callCondRelocks(r *relocker) {
	r.mu.Lock()
	r.condRelocks(true) // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The second of two acquisitions after one release deadlocks on the first.

func (r *relocker) retakesTwice() {
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	r.mu.Lock()
	r.mu.Lock() // +checklocksfail=already locked
} // +checklocksfail=return with unexpected locks held

func callRetakesTwice(r *relocker) {
	r.mu.Lock()
	r.retakesTwice() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// The lock given up belongs to another object.

func (r *relocker) dropsPeerLock() {
	r.peer.mu.Unlock() // +checklocksfail=already unlocked
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callDropsPeerLock(r *relocker) {
	r.mu.Lock()
	r.dropsPeerLock() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A callee's declared entry lock still reaches the wrapper's callers (known false report).

// +checklocks:r.mu
func (r *relocker) deferredRetake() {
	defer r.mu.Lock()
	r.mu.Unlock() // +lockgapfail=does not take it back
	work()
}

func (r *relocker) wrapsDeferredRetake() {
	r.deferredRetake() // +checklocksfail=must hold
}

func callWrapsDeferredRetake(r *relocker) {
	r.mu.Lock()
	r.wrapsDeferredRetake() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A lock the callee declares held on return is one it acquires.

// +checklocksacquire:r.mu
func (r *relocker) acquiresMu() {
	r.mu.Lock()
}

func (r *relocker) wrapsAcquiresMu() {
	r.acquiresMu()
	r.mu.Unlock()
}

func callWrapsAcquiresMu(r *relocker) {
	r.mu.Lock()
	r.wrapsAcquiresMu() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A callee whose body is not checked still takes the lock.

// +checklocksignore
func (r *relocker) ignoredLocksMu() {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func (r *relocker) wrapsIgnoredLocksMu() { r.ignoredLocksMu() }

func callWrapsIgnoredLocksMu(r *relocker) {
	r.mu.Lock()
	r.wrapsIgnoredLocksMu() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A lock that becomes a write lock only through a callee reaches callers in that mode.

func (r *relocker) readsThenCallsWriter() {
	r.rw.RLock()
	_ = r.m
	r.rw.RUnlock()
	r.writesRW()
}

func (r *relocker) writesRW() {
	r.rw.Lock()
	r.m++
	r.rw.Unlock()
}

func callReadsThenCallsWriter(r *relocker) {
	r.rw.RLock()
	r.readsThenCallsWriter() // +checklocksfail=must not hold
	r.rw.RUnlock()
}

// Two releases, each followed by a retake.

func (r *relocker) relocksTwice() {
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	r.mu.Lock()
	r.mu.Unlock() // +lockgapfail=taken again
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callRelocksTwice(r *relocker) {
	r.mu.Lock()
	r.relocksTwice() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A closure between the release and the retake that the receiver does not reach.

func (r *relocker) dropsAroundUnrelatedClosure(x *int) {
	r.mu.Unlock() // +checklocksfail=already unlocked +lockgapfail=taken again
	func() { *x++ }()
	r.mu.Lock()
} // +checklocksfail=return with unexpected locks held

func callDropsAroundUnrelatedClosure(r *relocker, x *int) {
	r.mu.Lock()
	r.dropsAroundUnrelatedClosure(x) // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A "go" statement in a closure the body runs counts as one in the body does.

func (r *relocker) spawnsInClosure() {
	func() { go r.locksMu() }()
}

func callSpawnsInClosure(r *relocker) {
	r.mu.Lock()
	r.spawnsInClosure() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A read lock declared held on entry, with the body taking the write lock.

// +checklocksread:r.rw
func (r *relocker) readHeldUpgrades() {
	r.rw.RUnlock() // +lockgapfail=takes it again
	r.rw.Lock()
	r.rw.Unlock() // +lockgapfail=takes it again
	r.rw.RLock()
}

func (r *relocker) wrapsReadHeldUpgrades() {
	r.rw.RLock()
	r.readHeldUpgrades()
	r.rw.RUnlock()
}

func callWrapsReadHeldUpgrades(r *relocker) {
	r.rw.RLock()
	r.wrapsReadHeldUpgrades() // +checklocksfail=must not hold
	r.rw.RUnlock()
}

// A wrapper takes the lock through an acquire helper, which the derivation does not see.

// +checklocksacquire:x.mu
func lockRelocker(x *relocker) {
	x.mu.Lock()
}

// +checklocks:r.mu
func (r *relocker) heldRelocks() {
	r.mu.Unlock() // +lockgapfail=takes it again
	work()
	r.mu.Lock()
}

func (r *relocker) wrapsHeldRelocks() {
	lockRelocker(r)
	r.heldRelocks()
	r.mu.Unlock()
}

func callWrapsHeldRelocks(r *relocker) {
	r.mu.Lock()
	r.wrapsHeldRelocks() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A spawned write lock blocks the read lock the method takes itself.

func (r *relocker) goWriterReads() {
	go r.writesRW()
	r.rw.RLock()
	_ = r.m
	r.rw.RUnlock()
}

func callGoWriterReads(r *relocker) {
	r.rw.RLock()
	r.goWriterReads() // +checklocksfail=must not hold
	r.rw.RUnlock()
}

// The arguments of a "go" statement are evaluated by the method.

func (r *relocker) lockedN() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func consume(int) {}

func (r *relocker) spawnsWithLockedArg() { go consume(r.lockedN()) }

func callSpawnsWithLockedArg(r *relocker) {
	r.mu.Lock()
	r.spawnsWithLockedArg() // +checklocksfail=must not hold
	r.mu.Unlock()
}

// A write lock two calls down reaches the head of the chain in that mode.

func (r *relocker) chainHead() {
	r.rw.RLock()
	_ = r.m
	r.rw.RUnlock()
	r.chainMid()
}

func (r *relocker) chainMid() {
	r.rw.RLock()
	_ = r.m
	r.rw.RUnlock()
	r.chainTail()
}

func (r *relocker) chainTail() {
	r.rw.Lock()
	r.m++
	r.rw.Unlock()
}

func callChainHead(r *relocker) {
	r.rw.RLock()
	r.chainHead() // +checklocksfail=must not hold
	r.rw.RUnlock()
}
