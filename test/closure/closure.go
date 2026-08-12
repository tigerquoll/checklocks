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

// Package closure is the corpus for annotations on function literals.
//
// A literal that is passed somewhere this analysis cannot follow is analyzed
// on its own, with nothing held. An annotation says what the caller holds when
// it runs, which is the only way a callback invoked by a library can be
// checked at all.
package closure

import "sync"

type target struct {
	mu sync.Mutex
	// +checklocks:mu
	value int
}

// callbacks is the shape a state machine library takes: a table of literals,
// each invoked by the library with the lock already held.
type callbacks map[string]func(t *target)

func register(cb callbacks) {}

func run(f func(t *target)) {}

// A literal in a composite literal, annotated above the key and value.
func table() {
	register(callbacks{
		// +checklocks:t.mu
		"enter": func(t *target) {
			t.value = 1
		},
		// +checklocks:t.mu
		"leave": func(t *target) {
			t.value = 2
		},
		// Without an annotation the literal is analyzed holding
		// nothing, so the guarded write is reported.
		"unannotated": func(t *target) {
			t.value = 3 // +checklocksfail=must be locked
		},
		// The annotation is a claim about the caller, so a literal
		// that takes the lock itself is a double acquisition.
		// +checklocks:t.mu
		"relocks": func(t *target) {
			t.mu.Lock() // +checklocksfail=already locked
			t.value = 4
			t.mu.Unlock()
		},
	})
}

// A literal assigned to a variable.
func assigned(t *target) {
	// +checklocks:t.mu
	f := func(t *target) {
		t.value = 1
	}
	run(f)
}

func assignedUnannotated(t *target) {
	f := func(t *target) {
		t.value = 1 // +checklocksfail=must be locked
	}
	run(f)
}

// A literal in a declaration.
func declared(t *target) {
	// +checklocks:t.mu
	var f = func(t *target) {
		t.value = 1
	}
	run(f)
}

// A literal passed directly as an argument, annotated above the literal.
func argument() {
	run(
		// +checklocks:t.mu
		func(t *target) {
			t.value = 1
		})
}

type rwTarget struct {
	mu sync.RWMutex
	// +checklocks:mu
	value int
}

func runRW(f func(t *rwTarget)) {}

// The read variant admits a read lock, which covers a read and not a write.
func readOnly() {
	// +checklocksread:t.mu
	runRW(func(t *rwTarget) {
		_ = t.value
	})
	// +checklocksread:t.mu
	runRW(func(t *rwTarget) {
		t.value = 1 // +checklocksfail=must be locked
	})
}

// An ignore on a literal drops its diagnostics wherever it is analyzed.
func ignored() {
	register(callbacks{
		// +checklocksignore
		"ignored": func(t *target) {
			t.value = 1
		},
	})
}

// The same literal, invoked inline rather than handed away: the ignore still
// applies, and an unannotated literal is analyzed with the caller's real lock
// state, which is why this one is clean without any annotation.
func inline(t *target) {
	t.mu.Lock()
	func() {
		t.value = 1
	}()
	t.mu.Unlock()
}

func inlineIgnored(t *target) {
	// +checklocksignore
	func() {
		t.value = 1
	}()
}

// A comment above a statement holding more than one literal does not say
// which it means, so it binds to neither and both are analyzed plain.
func ambiguous() {
	// +checklocks:t.mu
	a, b := func(t *target) { t.value = 1 }, func(t *target) { t.value = 2 } // +checklocksfail=must be locked|must be locked
	run(a)
	run(b)
}
