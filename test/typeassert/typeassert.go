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

// Package typeassert is the corpus for guards naming a value recovered by a
// type assertion.
//
// The shape is a callback table read by a state machine library: the subject
// arrives as an interface inside an event, and the callback recovers it. The
// lock is held by the library's caller, which nothing here can see.
package typeassert

import "sync"

// Event is the shape a state machine library passes to a callback.
type Event struct {
	Args []any
	Src  string
}

// Callbacks is the table.
type Callbacks map[string]func(event *Event)

func register(cb Callbacks) {}

type Application struct {
	mu sync.RWMutex
	// +checklocks:mu
	state string
	// +checklocks:mu
	count int

	id string
}

// +checklocks:a.mu
func (a *Application) onStateChangeLocked() {
	a.count++
}

// +checklocksexclude:a.mu
func (a *Application) unrelatedLocked() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.count++
}

// Other is a second type asserted from the same element, to check that an
// assertion to a type the annotation does not name stays unbound.
type Other struct {
	mu sync.Mutex
	// +checklocks:mu
	value int
}

func table() {
	register(Callbacks{
		// The annotated callback: the guard names the assertion, so the
		// body runs with the lock held.
		// +checklocks:event.Args[0].(*Application).mu
		"enter": func(event *Event) {
			app := event.Args[0].(*Application)
			app.state = "entered"
			app.onStateChangeLocked()
		},
		// Without the annotation the body holds nothing.
		"unannotated": func(event *Event) {
			app := event.Args[0].(*Application)
			app.state = "entered"     // +checklocksfail=must be locked
			app.onStateChangeLocked() // +checklocksfail=must hold
		},
		// The guard is a claim about the caller, so taking the lock as
		// well is a second acquisition.
		// +checklocks:event.Args[0].(*Application).mu
		"relocks": func(event *Event) {
			app := event.Args[0].(*Application)
			app.mu.Lock() // +checklocksfail=already locked
			app.state = "entered"
			app.mu.Unlock()
		},
		// A method declared not to be called with the lock held is
		// reported when the guard says it is.
		// +checklocks:event.Args[0].(*Application).mu
		"calls-self-locking": func(event *Event) {
			app := event.Args[0].(*Application)
			app.unrelatedLocked() // +checklocksfail=must not hold
		},
		// The assertion may be written inline, and more than once: each
		// assertion of the named type on the named path is bound.
		// +checklocks:event.Args[0].(*Application).mu
		"inline-and-repeated": func(event *Event) {
			event.Args[0].(*Application).onStateChangeLocked()
			event.Args[0].(*Application).state = "again"
		},
		// An assertion to another type is a different subject, and is
		// not covered by this guard. A guard that matches no assertion
		// records nothing rather than reporting: the uncovered access
		// below is what tells the reader.
		// +checklocks:event.Args[0].(*Application).mu
		"other-type": func(event *Event) {
			other := event.Args[0].(*Other)
			other.value = 1 // +checklocksfail=must be locked
		},
		// A different element of the same parameter is a different
		// path, and is not covered either.
		// +checklocks:event.Args[0].(*Application).mu
		"other-index": func(event *Event) {
			app := event.Args[1].(*Application)
			app.state = "entered" // +checklocksfail=must be locked
		},
	})
}

// The assertion may be reached through more than one block.
func multiBlock() {
	register(Callbacks{
		// +checklocks:event.Args[0].(*Application).mu
		"branch": func(event *Event) {
			if event.Src == "a" {
				app := event.Args[0].(*Application)
				app.state = "a"
				return
			}
			app := event.Args[0].(*Application)
			app.count = 1
		},
	})
}

// The comma-ok form is bound as well. Note that the guard is recorded for the
// asserted value throughout the body, including the branch the assertion
// failed on, where the value is nil and any use of it panics; see the README.
func commaOk() {
	register(Callbacks{
		// +checklocks:event.Args[0].(*Application).mu
		"comma-ok": func(event *Event) {
			app, ok := event.Args[0].(*Application)
			if !ok {
				return
			}
			app.state = "entered"
			app.onStateChangeLocked()
		},
	})
}

// A declared function may use the form too, though the shape it exists for is
// a literal.

// +checklocks:args[0].(*Application).mu
func declared(args []any) {
	app := args[0].(*Application)
	app.state = "entered"
}

func callDeclared(args []any) {
	// The guard describes what the body may assume, not a precondition a
	// caller is checked against: the caller of a callback is the library.
	declared(args)
}
