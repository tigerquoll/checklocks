// Copyright 2020 The gVisor Authors.
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

type lfStruct struct {
	mu   sync.Mutex
	next *lfStruct
}

func lfNoop() {}

func lfGet(s *lfStruct) *lfStruct { return s }

// The literal's own defer does not run the caller's pending unlock.
func lfUnlockRelock(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mu.Unlock() // +lockgapfail=taken again
	func() { defer lfNoop() }()
	t.mu.Lock()
}

// A literal that releases the caller's lock leaves it released for the caller.
func lfReleases(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		t.mu.Unlock()
	}()
}

// Without a defer, the caller no longer holds what the literal released.
func lfReleased(t *oneGuardStruct) {
	t.mu.Lock()
	func() { t.mu.Unlock() }()
	t.guardedField = 1 // +checklocksfail
}

// The same, with the lock passed as an argument.
func lfArgReleases(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func(m *sync.Mutex) {
		defer lfNoop()
		m.Unlock()
	}(&t.mu)
}

// A lock the literal takes is its own to release.
func lfAcquires(t *oneGuardStruct) {
	func() {
		t.mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	t.guardedField = 1 // +checklocksfail
}

// Exits that hold different locks leave the caller's state as it was.
func lfDisagree(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	func() { // +checklocksfail=incompatible return states
		if c {
			t.mu.Unlock()
		}
	}()
	t.mu.Unlock()
}

// Exits that disagree under the caller's pending defer: the literal is walked with the defer again.
func lfRelMaybeDefer(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		if c {
			t.mu.Unlock()
		}
	}()
}

// A literal that frees a lock and takes the same one under another name is not folded.
func lfRetake(s *lfStruct) {
	s.mu.Lock()
	func() {
		s.mu.Unlock()
		lfGet(s).mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	s.mu.Unlock()
}

// A captured cursor moved on inside the literal.
func lfHandOver(s *lfStruct) {
	p := s
	p.mu.Lock()
	func() {
		p.mu.Unlock() // +lockgapfail=taken again
		p = p.next
		p.mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	p.mu.Unlock()
}

// A literal that never returns folds nothing.
func lfNoExit(t *oneGuardStruct) {
	t.mu.Lock()
	func() { panic("x") }()
	t.mu.Unlock()
}

// A deferred literal's release is the state the next deferred call runs in.
func lfDeferredLit(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	defer func() { t.mu.Unlock() }()
}

// A literal inside a literal that is walked again with the caller's defers is walked that way too.
func lfNest(t, u, w *oneGuardStruct, c bool) {
	t.mu.Lock()
	u.mu.Lock()
	defer t.mu.Unlock()
	defer u.mu.Unlock() // +checklocksfail=already unlocked

	func() { // +checklocksfail=incompatible return states
		defer lfNoop()
		u.mu.Unlock() // +lockgapfail=taken again
		func() { defer lfNoop() }()
		u.mu.Lock()
		if c {
			w.mu.Lock()
		}
	}() // +checklocksfail=return with unexpected locks held
}

var lfSink func()

// The closures of a literal walked again are analyzed again.
func lfRewalkClosure(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		if c {
			t.mu.Unlock()
		}
		lfSink = func() {
			t.guardedField = 1 // +checklocksfail
		}
	}()
}
