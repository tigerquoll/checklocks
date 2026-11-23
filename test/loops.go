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

import "sync"

type loopT struct {
	mu sync.Mutex
	// +checklocks:mu
	x int
}

type loopSet struct {
	mu sync.Mutex
}

// sevenLocks is a type no other case uses, so its mutexes are fresh paths.
type sevenLocks struct {
	a, b, c, d, e, f, g sync.Mutex
}

func loopWork() {}

func loopNever(t *loopT, n int) { // +checklocksfail=incompatible return states
	for i := 0; i < n; i++ {
		t.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

func loopNeverReturn(t *loopT, n int) int { // +checklocksfail=incompatible return states
	for i := 0; i < n; i++ {
		t.mu.Lock() // +checklocksfail=already locked
	}
	return n // +checklocksfail=return with unexpected locks held
}

func loopUnlock(t *loopT, n int) { // +checklocksfail=incompatible return states
	t.mu.Lock()
	for i := 0; i < n; i++ {
		t.x++         // +checklocksfail=invalid field access
		t.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}
} // +checklocksfail=return with unexpected locks held

func loopLockAllUnlockAll(ts []*loopT) {
	for _, t := range ts {
		t.mu.Lock()
	}
	for _, t := range ts {
		t.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}
}

func loopLockAllIndex(ts []loopT) {
	for i := range ts {
		ts[i].mu.Lock()
	}
	for i := range ts {
		ts[i].mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}
}

func loopLockAllPtrUnlockDefer(ts []*loopT) {
	for _, t := range ts {
		t.mu.Lock()
	}
	defer func() {
		for _, t := range ts {
			t.mu.Unlock() // +checklocksfail=already unlocked or locked differently
		}
	}()
}

func loopLockDeferEach(ts []*loopT) {
	for _, t := range ts {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
}

func loopLockDeferForever(get func() *loopT) {
	for {
		t := get()
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.x > 0 {
			break
		}
	}
}

func loopPerIteration(t *loopT, n int) {
	for i := 0; i < n; i++ {
		t.mu.Lock()
		t.x++
		t.mu.Unlock()
	}
}

func loopNested(t *loopT, n int) {
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			t.mu.Lock()
			t.x++
			t.mu.Unlock()
		}
	}
}

// +checklocks:t.mu
func loopRelock(t *loopT, n int) {
	for i := 0; i < n; i++ {
		t.mu.Unlock() // +lockgapfail=takes it again
		loopWork()
		t.mu.Lock()
	}
}

func loopEarlyReturn(t *loopT, n int) int {
	for i := 0; i < n; i++ {
		t.mu.Lock()
		if t.x == i {
			t.mu.Unlock()
			return i
		}
		t.mu.Unlock()
	}
	return -1
}

// The kill of it.mu does not silence the release of s.mu, which the loop did not drop.
// +checklocks:s.mu
func (s *loopSet) loopNarrowed(items []*loopT) { // +checklocksfail=incompatible return states
	for _, it := range items {
		s.mu.Unlock() // +checklocksfail=already unlocked or locked differently +lockgapfail=does not take it back
		it.mu.Lock()
	}
} // +checklocksfail=not held exclusively

func loopUnlockBefore(get func() *loopT, c func() bool) { // +checklocksfail=incompatible return states
	u := get()
	u.mu.Lock()
	for c() {
		t := get()
		t.mu.Lock()
		u.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}
} // +checklocksfail=return with unexpected locks held

// The deferred receiver is resolved at exit, not at the defer, so only the earlier rule keeps this quiet.
func loopDeferStored(ts []*loopT, get func() *loopT) {
	var cur *loopT
	g := func() { _ = cur }
	for _, t := range ts {
		cur = t
		cur.mu.Lock()
		defer cur.mu.Unlock()
	}
	cur = get()
	g()
}

func loopDeferStoredInside(ts []*loopT, get func() *loopT) {
	var cur *loopT
	g := func() { _ = cur }
	for _, t := range ts {
		cur = t
		cur.mu.Lock()
		defer cur.mu.Unlock()
		cur = get()
	}
	g()
}

// The literal unlocks the value cur has at exit, which nothing locked.
func loopDeferLiteralStored(ts []*loopT, get func() *loopT) {
	var cur *loopT
	for _, t := range ts {
		cur = t
		cur.mu.Lock()
		defer func() {
			cur.mu.Unlock() // +checklocksfail=already unlocked or locked differently
		}()
	}
	cur = get()
}

// Neither label's block dominates the other, so the jump back is not a back edge.
func loopIrreducible(t *loopT, c func() bool) { // +checklocksfail=incompatible return states
	if c() {
		goto second
	}
first:
	t.mu.Lock() // +checklocksfail=already locked
second:
	if c() {
		goto first
	}
} // +checklocksfail=return with unexpected locks held

func loopGet() *loopT { return nil }

// A for loop inside a goto cycle: leaving it keeps p.mu.
func loopInIrr(c func() bool) {
	if c() {
		goto second
	}
first:
	loopWork()
second:
	p := loopGet()
	p.mu.Lock()
	for c() {
		loopWork()
	}
	p.x = 1
	p.mu.Unlock()
	if c() {
		goto first
	}
}

// The lock is on a value the cycle redefines; coming round does not lock it twice.
func loopIrrHeld(c func() bool) { // +checklocksfail=incompatible return states
	if c() {
		goto second
	}
first:
	{
		q := loopGet()
		q.mu.Lock()
	}
second:
	if c() {
		goto first
	}
} // +checklocksfail=return with unexpected locks held

// The break leaves the loop holding p.mu, and the lock on the next trip round is on another value.
func loopIrrForBreak(c func() bool) { // +checklocksfail=incompatible return states
	if c() {
		goto second
	}
first:
	loopWork()
second:
	for c() {
		p := loopGet()
		p.mu.Lock()
		if c() {
			break
		}
		p.mu.Unlock()
	}
	if c() {
		goto first
	}
} // +checklocksfail=return with unexpected locks held

func loopIrrForGotoOut(c func() bool) { // +checklocksfail=incompatible return states
	if c() {
		goto second
	}
first:
	loopWork()
second:
	for c() {
		p := loopGet()
		p.mu.Lock()
		if c() {
			goto first
		}
		p.mu.Unlock()
	}
} // +checklocksfail=return with unexpected locks held

func loopIrrForBreakParam(c func() bool, t *loopT) { // +checklocksfail=incompatible return states
	if c() {
		goto second
	}
first:
	loopWork()
second:
	for c() {
		t.mu.Lock() // +checklocksfail=already locked
		if c() {
			break
		}
		t.mu.Unlock()
	}
	if c() {
		goto first
	}
} // +checklocksfail=return with unexpected locks held

func loopOverflowWithLiteral(f *sevenLocks, c func() bool) { // +checklocksfail=checklocks gave up on this function
	func() {
		if c() {
			f.a.Lock()
		}
	}()
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
}

func loopOverflowInLiteral(f *sevenLocks, o *loopT, c func() bool) {
	if c() {
		o.x = 1 // +checklocksfail=invalid field access
	}
	func() { // +checklocksfail=checklocks gave up on this function
		for c() {
			if c() {
				f.a.Lock()
			}
			if c() {
				f.b.Lock()
			}
			if c() {
				f.c.Lock()
			}
			if c() {
				f.d.Lock()
			}
			if c() {
				f.e.Lock()
			}
			if c() {
				f.f.Lock()
			}
			if c() {
				f.g.Lock()
			}
		}
	}()
	if c() {
		o.x = 2 // +checklocksfail=invalid field access
	}
}

func loopOverflowDefer(f *sevenLocks, c func() bool) { // +checklocksfail=checklocks gave up on this function
	defer loopWork()
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
}
