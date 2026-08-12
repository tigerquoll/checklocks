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

// Package lockorder is the test corpus for the lockorder analyzer.
//
// The taxonomy below mirrors the shape of a real scheduler: a context above a partition,
// a partition above an application, and an application above the queue, node and tracker
// objects it touches. Queue is hierarchical because a queue walks its own tree.
//
// +lockorder:Context < Partition
// +lockorder:Partition < App
// +lockorder:App < Queue
// +lockorder:App < Node
// +lockorder:App < Manager
// +lockorder:Manager < Tracker
// +lockhierarchical:Queue
package lockorder

import "sync"

// +lockclass:Context
type Context struct {
	mu sync.Mutex
}

// +checklocksignore
func (c *Context) Lock() { c.mu.Lock() }

// +checklocksignore
func (c *Context) Unlock() { c.mu.Unlock() }

// +lockclass:Partition
type Partition struct {
	mu sync.Mutex
}

// +checklocksignore
func (p *Partition) Lock() { p.mu.Lock() }

// +checklocksignore
func (p *Partition) Unlock() { p.mu.Unlock() }

// +lockclass:App
type App struct {
	mu    sync.RWMutex
	queue *Queue
}

// +checklocksignore
func (a *App) Lock() { a.mu.Lock() }

// +checklocksignore
func (a *App) Unlock() { a.mu.Unlock() }

// +checklocksignore
func (a *App) RLock() { a.mu.RLock() }

// +checklocksignore
func (a *App) RUnlock() { a.mu.RUnlock() }

// +lockclass:Queue
type Queue struct {
	mu     sync.Mutex
	parent *Queue
}

// +checklocksignore
func (q *Queue) Lock() { q.mu.Lock() }

// +checklocksignore
func (q *Queue) Unlock() { q.mu.Unlock() }

// +lockclass:Node
type Node struct {
	mu sync.Mutex
}

// +checklocksignore
func (n *Node) Lock() { n.mu.Lock() }

// +checklocksignore
func (n *Node) Unlock() { n.mu.Unlock() }

// +lockclass:Manager
type Manager struct {
	mu sync.Mutex
}

// +checklocksignore
func (m *Manager) Lock() { m.mu.Lock() }

// +checklocksignore
func (m *Manager) Unlock() { m.mu.Unlock() }

// +lockclass:Tracker
type Tracker struct {
	mu sync.Mutex
}

// +checklocksignore
func (t *Tracker) Lock() { t.mu.Lock() }

// +checklocksignore
func (t *Tracker) Unlock() { t.mu.Unlock() }

// Unclassed carries a lock that takes no part in the order.
type Unclassed struct {
	mu sync.Mutex
}

// +checklocksignore
func (u *Unclassed) Lock() { u.mu.Lock() }

// +checklocksignore
func (u *Unclassed) Unlock() { u.mu.Unlock() }

// --- the declared order is allowed downward -------------------------------------------

func downwardIsAllowed(c *Context, p *Partition, a *App, n *Node) {
	c.Lock()
	p.Lock()
	a.Lock()
	n.Lock()
	n.Unlock()
	a.Unlock()
	p.Unlock()
	c.Unlock()
}

func downwardTransitively(c *Context, t *Tracker) {
	c.Lock()
	t.Lock()
	t.Unlock()
	c.Unlock()
}

// --- acquiring upward is a violation ---------------------------------------------------

func upwardIsAViolation(p *Partition, a *App) {
	a.Lock()
	p.Lock() // +lockorderfail=acquiring Partition
	p.Unlock()
	a.Unlock()
}

func upwardTransitively(c *Context, t *Tracker) {
	t.Lock()
	c.Lock() // +lockorderfail=acquiring Context
	c.Unlock()
	t.Unlock()
}

// --- pairs the order does not relate are not checked -----------------------------------

func unrelatedPairIsSilent(n *Node, q *Queue) {
	n.Lock()
	q.Lock()
	q.Unlock()
	n.Unlock()
}

func unrelatedPairEitherWay(n *Node, q *Queue) {
	q.Lock()
	n.Lock()
	n.Unlock()
	q.Unlock()
}

func unclassedIsSilent(u *Unclassed, a *App) {
	u.Lock()
	a.Lock()
	a.Unlock()
	u.Unlock()
}

// --- same class ------------------------------------------------------------------------

func sameClassIsAViolation(a, b *App) {
	a.Lock()
	b.Lock() // +lockorderfail=two locks of one class
	b.Unlock()
	a.Unlock()
}

// A hierarchical class nests with itself by design: a queue walks its own tree.
func hierarchicalNestingIsAllowed(q *Queue) {
	q.Lock()
	if q.parent != nil {
		q.parent.Lock()
		q.parent.Unlock()
	}
	q.Unlock()
}

// --- releases put the class back --------------------------------------------------------

func releaseClearsTheClass(p *Partition, a *App) {
	a.Lock()
	a.Unlock()
	// The application lock is no longer held, so this is not an inversion.
	p.Lock()
	p.Unlock()
}

// --- the violation is found through a callee, using its summary --------------------------

func takesTheQueue(q *Queue) {
	q.Lock()
	q.Unlock()
}

func takesThePartition(p *Partition) {
	p.Lock()
	p.Unlock()
}

func viaCalleeIsAllowed(a *App, q *Queue) {
	a.Lock()
	takesTheQueue(q)
	a.Unlock()
}

func viaCalleeIsAViolation(a *App, p *Partition) {
	a.Lock()
	takesThePartition(p) // +lockorderfail=acquiring Partition
	a.Unlock()
}

// The summary travels through a chain of calls, not just one level.
func indirectlyTakesThePartition(p *Partition) {
	takesThePartition(p)
}

func viaChainIsAViolation(a *App, p *Partition) {
	a.Lock()
	indirectlyTakesThePartition(p) // +lockorderfail=acquiring Partition
	a.Unlock()
}

// --- go breaks the nesting, which is the sanctioned fix ----------------------------------

func goEscapeIsSilent(a *App, p *Partition) {
	a.Lock()
	go takesThePartition(p)
	a.Unlock()
}

func goClosureEscapeIsSilent(a *App, p *Partition) {
	a.Lock()
	go func() {
		p.Lock()
		p.Unlock()
	}()
	a.Unlock()
}

// --- defer runs at exit, with whatever is held then ---------------------------------------

func deferIsCheckedAtExit(a *App, p *Partition) {
	a.Lock()
	defer takesThePartition(p) // +lockorderfail=acquiring Partition
	defer a.Unlock()
}

// --- suppression -------------------------------------------------------------------------

func lineIgnoreSuppresses(a *App, p *Partition) {
	a.Lock()
	p.Lock() // +lockorderignore
	p.Unlock()
	a.Unlock()
}

// +lockorderignore
func functionIgnoreSuppresses(a *App, p *Partition) {
	a.Lock()
	p.Lock()
	p.Unlock()
	a.Unlock()
}

// --- an annotated callback starts with the class held --------------------------------------

// The caller took the application lock and handed control over, so this runs with App held
// even though it does not take it itself.
//
// +checklocks:a.mu
func annotatedCallbackHoldsApp(a *App, p *Partition) {
	p.Lock() // +lockorderfail=acquiring Partition
	p.Unlock()
}

// +checklocksread:a.mu
func annotatedReadCallbackHoldsApp(a *App, q *Queue) {
	// Downward from App, so this is fine.
	q.Lock()
	q.Unlock()
}
