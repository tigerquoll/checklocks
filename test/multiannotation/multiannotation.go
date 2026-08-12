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

// Package multiannotation is the corpus for a comment that carries more than one
// annotation.
//
// It is a corpus of its own because it is the one place two analyses are turned on at once:
// the line that has to carry two annotations has to be a violation in two analyses' terms,
// and each of the other corpora runs with the others silenced.
package multiannotation

import "sync"

// Cache carries a classed lock and a guarded field, so that one line can be both an
// unguarded access and a wait under a lock.
//
// +lockclass:Cache
type Cache struct {
	mu sync.Mutex

	// +checklocks:mu
	entries map[string]string

	refills chan string
}

// refillFrom is the case: one line, two findings, two annotations on it.
//
// The write goes to ANOTHER cache's guarded field without that cache's lock, which checklocks
// reports, and it waits on a channel with this cache's lock held, which lockblocking reports.
// Neither annotation can be moved to the function without silencing the rest of the body, and
// before a comment could carry two, one of them had to be.
func (c *Cache) refillFrom(other *Cache, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	other.entries[key] = <-other.refills // +checklocksignore +lockblockingignore
}

// The same line with the ignores removed, to prove there is something to silence. Both
// analyses report it, and the two EXPECTATIONS share the line as well: an expectation is an
// annotation like any other, and one per analysis is what a shared line is for.
func (c *Cache) refillFromReported(other *Cache, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	other.entries[key] = <-other.refills // +checklocksfail=invalid field access +lockblockingfail=a channel receive
}

// An annotation that takes a payload can share a line too, and keeps its own payload: the
// separator is a space before the plus, and a payload does not contain one.
//
// +checklocks:c.mu +lockblockingignore
func (c *Cache) waitsWithTheLockHeld() string {
	c.entries["waited"] = ""
	return <-c.refills
}

func callsTheAnnotatedWaiter(c *Cache) {
	c.mu.Lock()
	// The callee is declared to run with the lock held, and the wait it does is silenced
	// inside it, so nothing is reported here either.
	c.waitsWithTheLockHeld()
	c.mu.Unlock()
}
