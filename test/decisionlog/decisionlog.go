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

// Package decisionlog is the corpus of the decision log test: each function is read for the
// decisions the checks make in it, so editing it moves the lines that test looks for.
package decisionlog

import "sync"

type guarded struct {
	mu sync.Mutex

	// +checklocks:mu
	field int
}

func noop() {}

func heldAccess(g *guarded) {
	g.mu.Lock()
	g.field = 1
	g.mu.Unlock()
}

func unheldAccess(g *guarded) {
	g.field = 1 // +checklocksfail
}

// +checklocks:g.mu
func needsLock(g *guarded) {
	defer noop()
	g.field = 2
}

func callNeedsLock(g *guarded) {
	g.mu.Lock()
	needsLock(g)
	g.mu.Unlock()
	needsLock(g) // +checklocksfail
}

func literalTwice(g *guarded) {
	g.mu.Lock()
	f := func() {
		g.field = 3
	}
	f()
	f()
	g.mu.Unlock()
}

var mu sync.Mutex

type named struct {
	mu sync.Mutex // +checklocksfail=may require checklocks annotation for mu, used with lock held 100% of the time

	// +checklocks:mu
	val int
}

// +checklocks:mu
var val named

func sameName() {
	mu.Lock()
	val.mu.Lock()
	val.val = 4
	val.mu.Unlock()
	mu.Unlock()
}

type fiveLocks struct {
	a, b, c, d, e sync.Mutex
}

func overflowWithLiteral(f *fiveLocks, c func() bool) { // +checklocksfail=checklocks gave up on this function
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
	}
}
