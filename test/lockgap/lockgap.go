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

// Package lockgap is the corpus for a lock released and taken again in one function.
//
// Most of what is here is the cases that must NOT be reported. The shape this looks for is
// two lock calls in one body, and so is every correct critical section; what separates them
// is which one can happen without the other, so the silences are where the analysis is.
package lockgap

import "sync"

type cache struct {
	mu    sync.RWMutex
	items map[string]int
	total int
}

type other struct {
	mu sync.Mutex
	n  int
}

// --- the shape: released, then taken again ------------------------------------------------

// Two critical sections written as one function. Nothing that was true under the first is
// true under the second, and the code in between reads as though it were.
func releaseAndRetake(c *cache) int {
	c.mu.Lock()
	n := c.total
	c.mu.Unlock() // +lockgapfail=taken again

	n += 1

	c.mu.Lock()
	c.total = n
	c.mu.Unlock()
	return n
}

// The read lock upgrade: what was decided under the read lock is decided again, or should
// be, because it may have changed before the write lock was taken.
func upgrade(c *cache, key string) {
	c.mu.RLock()
	_, present := c.items[key]
	c.mu.RUnlock() // +lockgapfail=stale

	if !present {
		c.mu.Lock()
		c.items[key] = 1
		c.mu.Unlock()
	}
}

// The window spans blocks, which is where it usually is: the release and the second
// acquisition are nowhere near each other.
func releaseAcrossBlocks(c *cache, cond bool) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock() // +lockgapfail=taken again

	if cond {
		c.total = 0
	}
	for i := 0; i < 3; i++ {
		_ = i
	}

	c.mu.Lock()
	c.total++
	c.mu.Unlock()
}

// --- the shape: a lock the caller holds, dropped by the callee ------------------------------

// The gap that is invisible from outside: the caller took the lock, called this, and this
// gave it up in the middle. The deferred re-acquisition is what makes it look balanced.
//
// +checklocks:c.mu
func dropsTheCallersLock(c *cache) {
	c.mu.Unlock() // +lockgapfail=caller's critical section is split
	defer c.mu.Lock()
	c.total = 0
}

// The same without taking it back at all: the caller's section simply ends here.
//
// +checklocks:c.mu
func endsTheCallersSection(c *cache) {
	c.mu.Unlock() // +lockgapfail=does not take it back
	c.total = 0
}

// A function that says it hands the lock back released has declared the split, and its call
// sites are checked against the declaration. There is nothing hidden to report.
//
// +checklocks:c.mu
// +checklocksrelease:c.mu
func declaresTheHandover(c *cache) {
	c.total = 0
	c.mu.Unlock()
}

// --- the silences ----------------------------------------------------------------------------

// Every correct critical section is a lock and an unlock in one function, and none of them
// is a gap.
func ordinaryCriticalSection(c *cache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
}

func ordinaryCriticalSectionExplicit(c *cache) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock()
}

// A lock taken and released each time round is a whole critical section per iteration. The
// release does not dominate the next acquisition: the loop is entered from outside it too.
func loopPerIteration(c *cache, keys []string) {
	for _, key := range keys {
		c.mu.Lock()
		c.items[key]++
		c.mu.Unlock()
	}
}

// The same with the loop written the other way round, and with work between the sections.
func loopWithWorkBetween(c *cache, n int) {
	for i := 0; i < n; i++ {
		c.mu.Lock()
		c.total += i
		c.mu.Unlock()

		expensive(i)
	}
}

func expensive(int) {}

// The same, with a branch inside the loop so that the release and the next iteration's
// acquisition are in different blocks: the second is reached from the first only by going
// round again. Plain reachability reports this; dominance is what does not.
func loopAcrossBlocks(c *cache, keys []string, cond bool) {
	for _, key := range keys {
		c.mu.Lock()
		if cond {
			c.items[key]++
		} else {
			c.items[key]--
		}
		c.mu.Unlock()
	}
}

// Two locks are two locks. Releasing one and taking the other is nesting, not a gap.
func differentLocks(c *cache, o *other) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock()

	o.mu.Lock()
	o.n++
	o.mu.Unlock()
}

// Two objects of one type are two locks as well: the object is part of what a lock is.
func differentObjects(a, b *cache) {
	a.mu.Lock()
	a.total++
	a.mu.Unlock()

	b.mu.Lock()
	b.total++
	b.mu.Unlock()
}

// A release on one path and an acquisition on another: neither can be said to follow the
// other, and the analysis says nothing rather than guessing.
func releaseOnOneBranch(c *cache, cond bool) {
	if cond {
		c.mu.Lock()
		c.total++
		c.mu.Unlock()
	} else {
		c.mu.Lock()
		c.total--
		c.mu.Unlock()
	}
}

// A goroutine has a lock state of its own, and what it does with the lock is its own
// critical section rather than a window in this one.
func goroutineTakesItAgain(c *cache) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock()

	go func() {
		c.mu.Lock()
		c.total++
		c.mu.Unlock()
	}()
}

// --- suppression --------------------------------------------------------------------------------

func lineIgnoreSuppresses(c *cache) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock() // +lockgapignore

	c.mu.Lock()
	c.total++
	c.mu.Unlock()
}

// +lockgapignore
func functionIgnoreSuppresses(c *cache) {
	c.mu.Lock()
	c.total++
	c.mu.Unlock()

	c.mu.Lock()
	c.total++
	c.mu.Unlock()
}
