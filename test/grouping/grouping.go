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

// Package grouping is the corpus for grouped reports.
//
// One callee that acquires a class out of order is one defect however many
// call sites reach it. Grouping emits it once, with the other sites attached,
// and leaves suppression alone: an ignore still applies to the site it is
// written on, and to nothing else.
//
// +lockorder:Queue < App
package grouping

import "sync"

// +lockclass:App
type App struct {
	mu sync.Mutex
}

// +checklocksignore
func (a *App) Lock() { a.mu.Lock() }

// +checklocksignore
func (a *App) Unlock() { a.mu.Unlock() }

// +lockclass:Queue
type Queue struct {
	mu sync.Mutex
}

// +checklocksignore
func (q *Queue) Lock() { q.mu.Lock() }

// +checklocksignore
func (q *Queue) Unlock() { q.mu.Unlock() }

// takesQueue is the one defect: it acquires Queue, and every caller that holds
// App reaches it. Queue is declared to be taken before App, so taking it while
// App is held is an inversion.
func takesQueue(q *Queue) {
	q.Lock()
	q.Unlock()
}

// Three sites reach it while holding App. Grouped, they are one report: the
// first carries it and the other two are attached to it.

func siteOne(a *App, q *Queue) {
	a.Lock()
	takesQueue(q) // +lockorderfail=declared order has Queue before App
	a.Unlock()
}

func siteTwo(a *App, q *Queue) {
	a.Lock()
	takesQueue(q) // +lockorderfail=declared order has Queue before App
	a.Unlock()
}

func siteThree(a *App, q *Queue) {
	a.Lock()
	takesQueue(q) // +lockorderfail=declared order has Queue before App
	a.Unlock()
}

// Partial suppression: an ignore removes its own site and leaves the rest of
// the group reporting. The two sites below are one group; only the second is
// reported.

func partialIgnored(a *App, q *Queue) {
	a.Lock()
	takesQueueTwo(q) // +lockorderignore
	a.Unlock()
}

func partialReported(a *App, q *Queue) {
	a.Lock()
	takesQueueTwo(q) // +lockorderfail=declared order has Queue before App
	a.Unlock()
}

func takesQueueTwo(q *Queue) {
	q.Lock()
	q.Unlock()
}

// Full suppression: every site of the group is ignored, so the group has no
// members and nothing is reported.

func fullyIgnoredOne(a *App, q *Queue) {
	a.Lock()
	takesQueueThree(q) // +lockorderignore
	a.Unlock()
}

func fullyIgnoredTwo(a *App, q *Queue) {
	a.Lock()
	takesQueueThree(q) // +lockorderignore
	a.Unlock()
}

func takesQueueThree(q *Queue) {
	q.Lock()
	q.Unlock()
}
