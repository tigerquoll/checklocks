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

// Package polarity pins how each report treats a lock a loop's back edge dropped, which the state
// cannot say is held or not held. Each loop takes a second lock g, so that the state with the
// dropped lock reads as a report of its own, apart from the state reached by the first pass.
package polarity

import "sync"

// +checklockslocktype
type dgRWMutex struct{ sync.RWMutex }

func (m *dgRWMutex) DowngradeLock() {}
func (m *dgRWMutex) Lock()          { m.RWMutex.Lock() }
func (m *dgRWMutex) Unlock()        { m.RWMutex.Unlock() }

type T struct {
	mu sync.Mutex
	// +checklocks:mu
	v  int
	rw dgRWMutex
}

func get() *T { return nil }

// +checklocks:x.mu
func needs(x *T) {}

// +checklocksrelease:x.mu
func rel(x *T) { x.mu.Unlock() }

// A release of the dropped lock is silent in all three kinds: one report, from the pass before the drop.

func releaseAnnotated(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x := get()
		rel(x) // +checklocksfail=attempt to release|must hold|must hold
		x.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

func releaseUnlock(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x := get()
		x.mu.Unlock() // +checklocksfail=already unlocked +lockgapignore
		x.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

func releaseDowngrade(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x := get()
		x.rw.DowngradeLock() // +checklocksfail=already unlocked or not exclusive
		x.rw.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

// A guarded access and a must-hold call read the dropped lock as not held: both report in the state that has it dropped.

func accessUnknown(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x := get()
		x.v = 1 // +checklocksfail=invalid field access|invalid field access
		x.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

func callUnknown(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x := get()
		needs(x) // +checklocksfail=must hold|must hold
		x.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

// Past the bound the marker is top, and the same holds for a value of the loop.

func accessTop(c func() bool, g *T) { // +checklocksfail=incompatible return states
	for c() {
		x, y := get(), get()
		x.v = 1 // +checklocksfail=invalid field access|invalid field access
		x.mu.Lock()
		y.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=return with unexpected locks held

// A lock the function was to leave held, dropped by a loop whose result is the lock's owner.
//
// +checklocksacquire:r.mu
func returnTop(c func() bool, g *T) (r *T) { // +checklocksfail=incompatible return states
	for c() {
		r = get()
		r.mu.Lock()
		y := get()
		y.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
	return r // +checklocksfail=not held exclusively|not held exclusively
}

// A deferred release is silent for any value once something was dropped; its twin is not.

func deferredUnrelated(ts []*T, g *T) { // +checklocksfail=incompatible return states
	for _, t := range ts {
		t.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
	x := get()
	defer x.mu.Unlock() // +checklocksfail=already unlocked
} // +checklocksfail=return with unexpected locks held

func plainUnrelated(ts []*T, g *T) { // +checklocksfail=incompatible return states
	for _, t := range ts {
		t.mu.Lock()
		g.mu.Lock() // +checklocksfail=already locked
	}
	x := get()
	x.mu.Unlock() // +checklocksfail=already unlocked|already unlocked
} // +checklocksfail=return with unexpected locks held

// A function over the cap prints the give-up row alone: the bug in it is not reported.

type sevenLocks struct {
	a, b, c, d, e, f, g sync.Mutex
}

func overCap(f *sevenLocks, t *T, c func() bool) { // +checklocksfail=checklocks gave up on this function
	for c() {
		t.v = 1
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
