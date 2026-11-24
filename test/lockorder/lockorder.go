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
// +lockorder:Partition < SeamInner
// +lockorder:DjPool < DjGuard
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

// --- a callee that releases its caller's lock is not nesting ------------------------------

// The unlock-relock gap: the callee drops the lock its caller holds, takes another one, and
// takes the caller's lock back on the way out. Nothing is nested at runtime, and the summary
// records the release so the call site can see that.
//
// +checklocks:n.mu
func (n *Node) gapThenTakesTheApp(a *App) {
	n.mu.Unlock() // +lockgapfail=caller's critical section is split
	defer n.mu.Lock()
	a.Lock()
	a.Unlock()
}

// +checklocks:n.mu
func (n *Node) callsTheGap(a *App) {
	// Silent: the application lock is taken with the node lock released.
	n.gapThenTakesTheApp(a)
}

// The same callee without the gap, which is the polarity that must still be reported.
//
// +checklocks:n.mu
func (n *Node) takesTheApp(a *App) {
	a.Lock() // +lockorderfail=acquiring App
	a.Unlock()
}

// +checklocks:n.mu
func (n *Node) callsWithoutTheGap(a *App) {
	n.takesTheApp(a) // +lockorderfail=acquiring App
}

// Only what the callee released is subtracted. The tracker lock is still held across the
// application acquisition, and the node's gap says nothing about it.
//
// +checklocks:n.mu
func (n *Node) callsTheGapHoldingATracker(a *App, t *Tracker) {
	t.Lock()
	n.gapThenTakesTheApp(a) // +lockorderfail=acquiring App
	t.Unlock()
}

// The gap is taken on one path only, so the summary cannot promise the caller anything: a
// class counts as released only where every path that acquires released it first.
//
// +checklocks:n.mu
func (n *Node) gapOnOnePathOnly(a *App, cond bool) {
	if cond {
		n.mu.Unlock() // +lockgapfail=caller's critical section is split
		a.Lock()
		a.Unlock()
		n.mu.Lock()
		return
	}
	a.Lock() // +lockorderfail=acquiring App
	a.Unlock()
}

// +checklocks:n.mu
func (n *Node) callsTheOnePathGap(a *App, cond bool) {
	n.gapOnOnePathOnly(a, cond) // +lockorderfail=acquiring App
}

// The caller holds the lock across a loop, which puts the call in a later block than the
// acquisition: the release has to survive that the same way the lock itself does.
func loopingCallerOfTheGap(n *Node, apps []*App) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range apps {
		n.gapThenTakesTheApp(a)
	}
}

// A defer is not pending at a return above the line that registers it. The early return
// here leaves through a path that never dropped the lock and never takes it back, so what
// the summary can promise is what the gap path promises, and no more.
//
// +checklocks:n.mu
func (n *Node) gapAfterAnEarlyReturn(a *App, done bool) {
	if done {
		return
	}
	n.mu.Unlock() // +lockgapfail=caller's critical section is split
	defer n.mu.Lock()
	a.Lock()
	a.Unlock()
}

// +checklocks:n.mu
func (n *Node) callsTheGapWithAnEarlyReturn(a *App, done bool) {
	// Silent: neither path takes the application lock with the node lock held. Treating
	// the deferred relock as pending at the early return as well would put the node class
	// back there, and report both this call and every caller of it.
	n.gapAfterAnEarlyReturn(a, done)
}

// A gap in a callee called on ANOTHER object says nothing about this receiver's lock, so it
// does not carry into this summary: a caller of this holding a node lock is still reported.
//
// +checklocks:n.mu
func (n *Node) callsTheGapOnAnotherNode(other *Node, a *App) {
	other.gapThenTakesTheApp(a) // +checklocksfail=must hold n.mu
}

// +checklocks:n.mu
func (n *Node) callsThroughAnotherNode(other *Node, a *App) {
	// Two reports: the application lock, and the other node's lock taken back while this
	// one is held, which is a genuine same class nesting between two objects.
	n.callsTheGapOnAnotherNode(other, a) // +lockorderfail=acquiring App|two locks of one class
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

// The unlock is deferred first, so it runs LAST: the deferred call below it runs with the
// application lock still held, and is a violation.
func deferIsCheckedAtExit(a *App, p *Partition) {
	defer a.Unlock()
	a.Lock()
	defer takesThePartition(p) // +lockorderfail=acquiring Partition
}

// A notification deferred BEFORE the lock is taken runs after the deferred unlock, because
// defers run in reverse order. That is the idiom for notifying listeners safely, and it must
// not be reported.
func deferBeforeLockRunsUnlocked(a *App, p *Partition) {
	defer takesThePartition(p)
	a.Lock()
	defer a.Unlock()
}

// The lock is held with a deferred unlock and the violation is in a loop, so it is in a
// later block than the acquisition. A defer evaluated where it was written rather than at
// the return would drop the class here and silently disarm the check for the whole
// "Lock then defer Unlock" idiom, which is most of the locking in real code.
func deferredUnlockHoldsAcrossBlocks(a *App, parts []*Partition) {
	a.Lock()
	defer a.Unlock()
	for _, p := range parts {
		takesThePartition(p) // +lockorderfail=acquiring Partition
	}
}

// A short circuit condition is evaluated in blocks the SSA builder numbers AFTER the branch
// they guard. Walking the blocks by index reaches the guarded body before anything has
// flowed into it, starts it with nothing held, and loses the lock for the rest of the
// function. Both arms are here because index order gets the shape right often enough to
// look correct: it takes a condition of this shape to come out in the wrong order.
func shortCircuitConditionKeepsTheLock(a *App, parts []*Partition, x, y bool) {
	a.Lock()
	defer a.Unlock()
	if (x || y) && len(parts) > 0 {
		for _, p := range parts {
			takesThePartition(p) // +lockorderfail=acquiring Partition
		}
	} else {
		takesThePartition(parts[0]) // +lockorderfail=acquiring Partition
	}
}

// The same shape with a branch rather than a loop.
func deferredUnlockHoldsAcrossBranch(a *App, p *Partition, cond bool) {
	a.Lock()
	defer a.Unlock()
	if cond {
		takesThePartition(p) // +lockorderfail=acquiring Partition
	}
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

// A method that calls itself on another value is recursion, whether or not its type is generic.
//
// +lockclass:PlainNode
type plainNode struct {
	mu sync.Mutex
}

// +checklocksignore
func (n *plainNode) Lock() { n.mu.Lock() }

// +checklocksignore
func (n *plainNode) Unlock() { n.mu.Unlock() }

func (n *plainNode) visit(o *plainNode) {
	n.Lock()
	o.visit(nil)
	n.Unlock()
}

// +lockclass:GenNode
type genNode[T any] struct {
	mu sync.Mutex
}

// +checklocksignore
func (n *genNode[T]) Lock() { n.mu.Lock() }

// +checklocksignore
func (n *genNode[T]) Unlock() { n.mu.Unlock() }

func (n *genNode[T]) visit(o *genNode[int]) {
	n.Lock()
	o.visit(nil)
	n.Unlock()
}

// --- locks the engine knows and this walk does not ------------------------------------

// +checklocksacquire:a.mu
func seamLockApp(a *App) { a.mu.Lock() }

// +checklocksrelease:a.mu
func seamUnlockApp(a *App) { a.mu.Unlock() }

// +checklocksacquire:p.mu
func seamLockPartition(p *Partition) { p.mu.Lock() }

// +checklocksrelease:p.mu
func seamUnlockPartition(p *Partition) { p.mu.Unlock() }

func seamAcquireThenInvert(a *App, p *Partition) {
	seamLockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	a.mu.Unlock()
}

// A callee's release is not applied, so the re-acquire reads as nested.
func seamReleaseThenReacquire(a *App) {
	a.mu.Lock()
	seamUnlockApp(a)
	a.mu.Lock() // +lockorderfail=acquiring App (via (*RWMutex).Lock) while holding App
	a.mu.Unlock()
}

func seamTakesPartition(p *Partition) {
	seamLockPartition(p)
	seamUnlockPartition(p)
}

func seamInversionThroughSummary(a *App, p *Partition) {
	seamLockApp(a)
	seamTakesPartition(p) // +lockorderfail=acquiring Partition (via seamTakesPartition) while holding App
	a.mu.Unlock()
}

// The callee is declared below its caller, so only the settled summary reaches the caller.
func seamInversionThroughRelease(a *App, p *Partition) {
	a.mu.Lock()
	a.seamRelockThenTake(p) // +lockorderfail=acquiring App (via (*App).seamRelockThenTake)|acquiring Partition (via (*App).seamRelockThenTake) while holding App
	a.mu.Unlock()
}

// The callee's release is its own, so the caller's App stays held across what it goes on to take.
func (a *App) seamRelockThenTake(p *Partition) {
	seamLockApp(a)
	a.mu.Unlock()
	p.Lock()
	p.Unlock()
}

// +checklockslocktype
// +lockclass:SeamGuard
type SeamGuard struct {
	mu sync.Mutex
}

func (g *SeamGuard) Lock() { g.mu.Lock() }

func (g *SeamGuard) Unlock() { g.mu.Unlock() }

type seamGuarded struct {
	g SeamGuard
}

// +checklocks:x.g
func seamParameterGuard(x, y *seamGuarded) {
	y.g.Lock() // +lockorderfail=acquiring SeamGuard (via (*SeamGuard).Lock) while holding SeamGuard
	y.g.Unlock()
}

var seamGlobalGuard SeamGuard

// +checklocks:seamGlobalGuard
func seamGlobalGuarded(y *seamGuarded) {
	y.g.Lock() // +lockorderfail=acquiring SeamGuard (via (*SeamGuard).Lock) while holding SeamGuard
	y.g.Unlock()
}

// +checklocks:a.mu
func seamNeedsApp(a *App) {}

// +checklocksignore
func seamAssumedOnly(a *App, p *Partition) {
	seamNeedsApp(a)
	p.Lock()
	p.Unlock()
}

// +checklocksignore
func seamAssumedOnOnePath(a *App, p *Partition, cond bool) {
	if cond {
		seamNeedsApp(a)
	} else {
		a.mu.Lock()
	}
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
}

// --- the engine only adds locks this walk cannot see; it never lowers this walk's own count -----

// +lockclass:SeamW
type seamW struct {
	mu sync.Mutex
}

// +checklocksacquire:w.mu
func (w *seamW) Lock() { w.mu.Lock() }

// +checklocksrelease:w.mu
func (w *seamW) Unlock() { w.mu.Unlock() }

// The wrapper's annotation is not counted a second time, so the standard unlock leaves nothing held.
func seamConsumedWrapperStandardUnlock(w *seamW) {
	w.Lock()
	w.mu.Unlock()
	w.Lock()
	w.Unlock()
}

// +checklocksrelease:a.mu
// +checklocksacquire:b.mu
func seamHandoff(a, b *App) {
	a.mu.Unlock()
	b.mu.Lock()
}

// One call that releases and takes back the same lock changes nothing.
func seamHandoffSameLock(a *App, p *Partition) {
	a.mu.Lock()
	seamHandoff(a, a) // +lockorderfail=acquiring App (via seamHandoff) while holding App
	a.mu.Unlock()
	p.Lock()
	p.Unlock()
}

// The entry lock is dropped through a wrapper the engine does not follow, so it stays entered with.
//
// +checklocks:a.mu
func (a *App) seamWrapperDropsEntry(p *Partition) {
	a.Unlock()
	p.Lock()
	p.Unlock()
	a.Lock()
}

var seamGApp App

// +checklocksrelease:seamGApp.mu
func seamReleaseG() { seamGApp.mu.Unlock() }

// +checklocksacquire:seamGApp.mu
func seamAcquireG() { seamGApp.mu.Lock() }

// Each release takes back the acquire before it: nothing is left held.
//
// +checklocks:seamGApp.mu
func seamHelperPairs(p *Partition) {
	seamReleaseG()
	seamAcquireG()
	seamReleaseG()
	p.Lock()
	p.Unlock()
	seamAcquireG()
}

// +checklocksignore
func seamAssumedThenAcquired(a *App, p *Partition) {
	seamNeedsApp(a)
	seamLockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	a.mu.Unlock()
}

// +checklocksrelease:b.mu
func seamUnlockB(b *App) { b.mu.Unlock() }

// +checklocks:a.mu
func (a *App) seamDropBoth(b *App, p *Partition) {
	b.mu.Lock() // +lockorderfail=acquiring App (via (*RWMutex).Lock) while holding App
	seamUnlockB(b)
	a.mu.Unlock() // +lockgapfail=the caller's critical section is split
	p.Lock()      // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	a.mu.Lock() // +lockorderfail=acquiring App (via (*RWMutex).Lock) while holding App
}

// c stays held across the callee, whatever the callee releases.
func seamTwoHolds(a, c, b *App, p *Partition) {
	c.mu.Lock()
	a.mu.Lock()          // +lockorderfail=acquiring App (via (*RWMutex).Lock) while holding App
	a.seamDropBoth(b, p) // +lockorderfail=acquiring App (via (*App).seamDropBoth)|acquiring Partition (via (*App).seamDropBoth) while holding App
	a.mu.Unlock()
	c.mu.Unlock()
}

// The gain is undone by the release, but never below what this walk holds itself.
//
// +checklocks:seamGApp.mu
func seamUndoFloor(a, b *App, p *Partition) {
	seamLockApp(a)       // +lockorderfail=acquiring App (via seamLockApp) while holding App
	seamGApp.mu.Unlock() // +lockgapfail=the mu lock is released here and taken again
	b.mu.Lock()
	seamUnlockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	b.mu.Unlock()
	seamGApp.mu.Lock()
}

// The own lock and the callee's gain meet at a join, in either order; the release must not take the own count.
//
// +checklocksignore
func seamJoinOwnFirst(a, b *App, p *Partition, c bool) {
	if c {
		b.mu.Lock()
	} else {
		seamLockApp(a)
	}
	seamUnlockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
}

// +checklocksignore
func seamJoinGainFirst(a, b *App, p *Partition, c bool) {
	if c {
		seamLockApp(a)
	} else {
		b.mu.Lock()
	}
	seamUnlockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
}

// A real unlock, then a gain of the same lock.
func seamUnlockThenGain(a *App, p *Partition) {
	seamLockApp(a)
	a.mu.Unlock()
	seamLockApp(a)
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	a.mu.Unlock()
}

// +lockclass:SeamInner
type seamInner struct {
	mu *sync.Mutex
}

// +checklocksalias:inner.mu=mu
type seamOuter struct {
	mu    sync.Mutex
	inner seamInner
}

func seamAliasBody(o *seamOuter, p *Partition) {
	o.inner.mu.Lock()
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding SeamInner
	p.Unlock()
	o.inner.mu.Unlock()
}

// +checklocks:o.inner.mu
func seamAliasEntry(o *seamOuter, p *Partition) {
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding SeamInner
	p.Unlock()
}

// A recursive read lock with one read unlock still holds the lock.
//
// +checklocksignore
func seamRecursiveRead(a *App, p *Partition) {
	a.mu.RLock()
	a.mu.RLock() // +lockorderfail=acquiring App (via (*RWMutex).RLock) while holding App
	a.mu.RUnlock()
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
	a.mu.RUnlock()
}

type seamQInfo struct {
	g SeamGuard
}

type seamE struct {
	info seamQInfo
}

// +checklocksrelease:e.info.g
func seamReleaseInfoG(e *seamE) { e.info.g.Unlock() }

// +checklocksacquire:e.info.g
func seamAcquireInfoG(e *seamE) { e.info.g.Lock() }

// The entry lock is two fields deep, so only the engine classes it.
//
// +checklocks:e.info.g
func (e *seamE) seamEntryReleasedOwnHeld(f *seamE) {
	f.info.g.Lock() // +lockorderfail=acquiring SeamGuard (via (*SeamGuard).Lock) while holding SeamGuard
	seamReleaseInfoG(e)
	f.info.g.Unlock()
	seamAcquireInfoG(e)
}

type seamTwoGuards struct {
	a SeamGuard
	b SeamGuard // +checklocksfail=may require checklocks annotation for a, used with lock held 100% of the time
}

// +checklocks:s.a
func (s *seamTwoGuards) seamOwnSameClass() {
	s.b.Lock() // +lockorderfail=acquiring SeamGuard (via (*SeamGuard).Lock) while holding SeamGuard
	s.b.Unlock()
}
