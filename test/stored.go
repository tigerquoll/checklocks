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

// Each probe stores a lock-relevant value on one arm only, so the walk must keep both arms.

type stT struct {
	mu sync.Mutex
}

type stIn struct {
	mu *sync.Mutex
}

// +checklocksalias:in.mu=mu
type stAliased struct {
	mu sync.Mutex
	in stIn

	// +checklocks:mu
	g int
}

type stGuarded struct {
	mu sync.Mutex

	// +checklocks:mu
	g int
}

var stCur, stOther *stT

func stCond() bool { return stOther == nil }

func stNop() {}

func stSink(any) {}

// storedGlobalThen: the literal stores the global its parameter was loaded from, on the then arm.
func storedGlobalThen() {
	stCur = stOther
	func(t *stT) { // +checklocksfail=incompatible return states
		t.mu.Lock()
		if stCond() {
			stCur = new(stT)
		}
		t.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}(stCur) // +checklocksfail=return with unexpected locks held
}

// storedGlobalElse: the same with the store on the else arm.
func storedGlobalElse() {
	stCur = stOther
	func(t *stT) { // +checklocksfail=incompatible return states
		t.mu.Lock()
		if stCond() {
			stNop()
		} else {
			stCur = new(stT)
		}
		t.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}(stCur) // +checklocksfail=return with unexpected locks held
}

// storedAliasBase: a slot reassigned on one arm, then the base of an alias-typed access.
func storedAliasBase(a, b *stAliased, c bool) {
	a.mu.Lock()
	p := b
	if c {
		p = a
	}
	stSink(&p)
	a.g = 1
	a.mu.Unlock()
	stSink(p.in)
}

// storedGuardedSlot: a slot of a type without alias annotations, reassigned on one arm, then a guarded access through it.
func storedGuardedSlot(a, b *stGuarded, c bool) {
	a.mu.Lock()
	p := b
	if c {
		p = a
	}
	stSink(&p)
	p.g = 1 // +checklocksfail=invalid field access, mu (&({param:b}.mu)) must be locked when accessing g
	a.mu.Unlock()
}

// storedReturn: a slot reassigned on one arm, then returned under a return guard.
//
// +checklocksacquire:r.mu
func storedReturn(a, b *stT, c bool) (r *stT) {
	a.mu.Lock()
	p := b
	if c {
		p = a
	}
	stSink(&p)
	return p // +checklocksfail=not held
}

// storedReceiver: a captured slot reassigned on one arm, then the receiver of a deferred unlock in a closure.
func storedReceiver(a, b *stT, c bool) {
	a.mu.Lock()
	p := &b.mu
	if c {
		p = &a.mu
	}
	touch := func() {
		defer p.Unlock() // +checklocksfail=already unlocked or locked differently
	}
	touch()
} // +checklocksfail=return with unexpected locks held
