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

// Package blockguards is the corpus for a guard declared on a structure.
//
// What it expands to is a guard on each field, so most of what is checked here is that the
// expansion reaches exactly the fields it should: not the lock, not the ones that opted out,
// not the ones that named a lock of their own, and every one of the rest including those
// declared several to a line.
package blockguards

import "sync"

// --- the sugar ---------------------------------------------------------------------------

// queue is the shape this exists for: one lock, most fields under it, a few that are set at
// construction and read without it.
//
// +checklocks:mu
type queue struct {
	mu sync.Mutex

	name  string
	state int
	// Several names on one line declare several fields, and the expansion has to reach
	// all of them rather than the syntax they share.
	pending, running int

	// +checklocksunguarded
	id string // set at construction, read without the lock

	// +checklocksunguarded
	parent *queue // as above
}

func writeUnlocked(q *queue) {
	q.name = "no"    // +checklocksfail=invalid field access
	q.state = 1      // +checklocksfail=invalid field access
	q.pending = 2    // +checklocksfail=invalid field access
	q.running = 3    // +checklocksfail=invalid field access
	q.id = "allowed" // exempt
	q.parent = nil   // exempt
}

func writeLocked(q *queue) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.name = "yes"
	q.state = 1
	q.pending = 2
	q.running = 3
}

func readUnlocked(q *queue) string {
	_ = q.state // +checklocksfail=invalid field access
	return q.id
}

// --- the lock is not guarded by itself -----------------------------------------------------

// Taking the lock is the whole point, so the field the guard names cannot be under it. An
// expansion that covered the lock would make every critical section unenterable.
func lockingWorks(q *queue) {
	q.mu.Lock()
	q.mu.Unlock()
}

// --- an embedded lock, which is how a real structure writes it --------------------------------

// +checklocks:Mutex
type embedded struct {
	sync.Mutex

	count int
	// +checklocksunguarded
	label string
}

func embeddedUnlocked(e *embedded) {
	// An increment reads and writes, so it is two accesses and two reports.
	e.count++       // +checklocksfail=invalid field access|invalid field access
	e.label = "any" // exempt
}

func embeddedLocked(e *embedded) {
	e.Lock()
	defer e.Unlock()
	e.count++
}

// --- a field may name a lock of its own -------------------------------------------------------

// +checklocks:mu
type twoLocks struct {
	mu    sync.Mutex
	other sync.Mutex

	underMu int
	// +checklocks:other
	underOther int
}

// The field that named the other lock keeps it: what is written on the field is more
// specific than the rule for the structure.
func twoLocksUnderMu(t *twoLocks) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.underMu = 1
	t.underOther = 2 // +checklocksfail=invalid field access
}

func twoLocksUnderOther(t *twoLocks) {
	t.other.Lock()
	defer t.other.Unlock()
	t.underOther = 2
}

// --- the declared form -------------------------------------------------------------------------

// The rule stated as the structure's own, rather than as shorthand. The expansion is the
// same; what differs is that a field restating it is reported, so that the annotations this
// replaces do not survive it unnoticed.
//
// +checklocksguardedby:mu
type declared struct {
	mu sync.Mutex

	value int
	// +checklocks:mu
	restated int // +checklocksfail=redundant

	// +checklocksunguarded
	kind string
}

func declaredUnlocked(d *declared) {
	d.value = 1    // +checklocksfail=invalid field access
	d.restated = 2 // +checklocksfail=invalid field access
	d.kind = "any" // exempt
}

// --- an exemption needs something to exempt from -------------------------------------------------

// A structure with no guard of its own has nothing for a field to opt out of, and an
// exemption there is left over from something: the guard was removed, or it was never
// written. Either way the field is unprotected and the annotation says the opposite.
type noStructGuard struct {
	mu sync.Mutex

	// +checklocksunguarded
	stale int // +checklocksfail=nothing to exempt
}

func noStructGuardAccess(n *noStructGuard) {
	n.stale = 1
}
