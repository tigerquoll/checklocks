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

// Package lockblockinggroup is the test corpus for grouped blocking reports.
package lockblockinggroup

import (
	"sync"
	"time"
)

// Grouped carries a classed lock, so holding it is what a diagnostic names.
//
// +lockclass:Grouped
type Grouped struct {
	mu sync.Mutex
}

// +checklocksignore
func (g *Grouped) Lock() { g.mu.Lock() }

// +checklocksignore
func (g *Grouped) Unlock() { g.mu.Unlock() }

func waits(c chan int) {
	<-c
}

// Two sites of one callee reach the one wait, so the group names the path.
func twoSitesOfOneCallee(g *Grouped, c chan int) {
	g.Lock()
	waits(c) // +lockblockingfail=calling waits, which may block (a channel receive at lockblockinggroup/lockblockinggroup.go:37) while holding Grouped
	g.Unlock()
	g.Lock()
	waits(c) // +lockblockingfail=calling waits, which may block (a channel receive at lockblockinggroup/lockblockinggroup.go:37) while holding Grouped
	g.Unlock()
}

type queue struct{}

func (queue) drain(c chan int) { <-c }

type clock struct{}

func (clock) drain(c chan int) { time.Sleep(time.Millisecond) }

// Bound method values drop the receiver from the name: one group, two paths, no path named.
func twoCalleesOfOneName(g *Grouped, c chan int) {
	fromQueue, fromClock := queue{}.drain, clock{}.drain
	g.Lock()
	fromQueue(c) // +lockblockingfail=calling drain$bound, which may block (a channel receive at lockblockinggroup/lockblockinggroup.go:52) while holding Grouped
	fromClock(c) // +lockblockingfail=calling drain$bound, which may block (calling Sleep at lockblockinggroup/lockblockinggroup.go:56) while holding Grouped
	g.Unlock()
}

type reader struct{}

func (reader) fetch(c chan int) { <-c }

type sleeper struct{}

func (sleeper) fetch(c chan int) { time.Sleep(time.Millisecond) }

// An ignored site of the same name does not change the path this site names.
func anIgnoredSiteDoesNotVote(g *Grouped, c chan int) {
	fromReader, fromSleeper := reader{}.fetch, sleeper{}.fetch
	g.Lock()
	fromReader(c)  // +lockblockingfail=calling fetch$bound, which may block (a channel receive at lockblockinggroup/lockblockinggroup.go:69) while holding Grouped
	fromSleeper(c) // +lockblockingignore
	g.Unlock()
}
