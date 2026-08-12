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

// Package lockblocking is the test corpus for the lockblocking analyzer.
//
// The classes are declared because the check is "is a class held", not "is a lock held": a
// lock with no class declared takes no part, the same as in the ordering analysis. No order
// is declared between them, since this analysis does not consult one.
package lockblocking

import (
	"net/http"
	"os/user"
	"sync"
	"time"
)

// App carries a classed lock, so holding it is what a diagnostic names.
//
// +lockclass:App
type App struct {
	mu sync.Mutex
}

// +checklocksignore
func (a *App) Lock() { a.mu.Lock() }

// +checklocksignore
func (a *App) Unlock() { a.mu.Unlock() }

// Unclassed carries a lock that takes no part in the analysis.
type Unclassed struct {
	mu sync.Mutex
}

// +checklocksignore
func (u *Unclassed) Lock() { u.mu.Lock() }

// +checklocksignore
func (u *Unclassed) Unlock() { u.mu.Unlock() }

// --- channels: a send or a receive waits --------------------------------------------------

func receiveUnderLock(a *App, c chan int) {
	a.Lock()
	<-c // +lockblockingfail=a channel receive
	a.Unlock()
}

func sendUnderLock(a *App, c chan int) {
	a.Lock()
	c <- 1 // +lockblockingfail=a channel send
	a.Unlock()
}

func receiveWithNothingHeld(c chan int) {
	<-c
}

func receiveAfterUnlock(a *App, c chan int) {
	a.Lock()
	a.Unlock()
	<-c
}

// A lock with no class declared is not a lock as far as this analysis is concerned.
func receiveUnderUnclassedLock(u *Unclassed, c chan int) {
	u.Lock()
	<-c
	u.Unlock()
}

// --- select: the default case is the whole point -------------------------------------------

// A select with no default waits for one of its cases, so it is a wait like any other.
func blockingSelectUnderLock(a *App, c chan int, stop chan struct{}) {
	a.Lock()
	select { // +lockblockingfail=a select with no default case
	case <-c:
	case <-stop:
	}
	a.Unlock()
}

// A select WITH a default never waits: it takes the case that is ready or the default, and
// returns either way. This is the non blocking dispatch idiom, and reporting it would
// condemn the very shape that makes dispatching under a lock safe.
func nonBlockingSelectUnderLock(a *App, c chan int) {
	a.Lock()
	select {
	case c <- 1:
	default:
	}
	a.Unlock()
}

// The same shape one call away, which is how a dispatcher is actually reached: the summary
// of the callee must not carry a blocking bit for it.
func dispatch(c chan int, event int) bool {
	select {
	case c <- event:
		return true
	default:
		return false
	}
}

func dispatchUnderLock(a *App, c chan int) {
	a.Lock()
	dispatch(c, 1)
	a.Unlock()
}

// --- the rest of the built-in list ---------------------------------------------------------

func sleepUnderLock(a *App) {
	a.Lock()
	time.Sleep(time.Second) // +lockblockingfail=calling Sleep
	a.Unlock()
}

func waitGroupUnderLock(a *App, wg *sync.WaitGroup) {
	a.Lock()
	wg.Wait() // +lockblockingfail=calling (*WaitGroup).Wait
	a.Unlock()
}

func condWaitUnderLock(a *App, cond *sync.Cond) {
	a.Lock()
	cond.Wait() // +lockblockingfail=calling (*Cond).Wait
	a.Unlock()
}

func httpUnderLock(a *App, client *http.Client, req *http.Request) {
	a.Lock()
	//nolint:bodyclose
	_, _ = client.Do(req) // +lockblockingfail=calling (*Client).Do
	a.Unlock()
}

func userLookupUnderLock(a *App, name string) {
	a.Lock()
	_, _ = user.Lookup(name) // +lockblockingfail=calling Lookup
	a.Unlock()
}

// --- reached through a summary, not written at the site ------------------------------------

func waits(c chan int) {
	<-c
}

func waitsIndirectly(c chan int) {
	waits(c)
}

func reachesAWaitUnderLock(a *App, c chan int) {
	a.Lock()
	waitsIndirectly(c) // +lockblockingfail=which may block
	a.Unlock()
}

func reachesAWaitWithNothingHeld(c chan int) {
	waitsIndirectly(c)
}

// --- the goroutine escape is the sanctioned fix ---------------------------------------------

func goEscapeIsSilent(a *App, c chan int) {
	a.Lock()
	go waits(c)
	a.Unlock()
}

func goClosureEscapeIsSilent(a *App, c chan int) {
	a.Lock()
	go func() {
		<-c
	}()
	a.Unlock()
}

// --- defer runs at the exit, with whatever is held then --------------------------------------

// The unlock is deferred first, so it runs LAST: the wait deferred below it runs with the
// lock still held.
func deferredWaitRunsLocked(a *App, c chan int) {
	defer a.Unlock()
	a.Lock()
	defer waits(c) // +lockblockingfail=which may block
}

// A wait deferred BEFORE the lock is taken runs after the deferred unlock, because defers run
// in reverse order. That is how a notification is made safe, and it must not be reported.
func deferredBeforeLockRunsUnlocked(a *App, c chan int) {
	defer waits(c)
	a.Lock()
	defer a.Unlock()
}

// The lock is held across a loop with a deferred unlock, so the wait is in a later block than
// the acquisition.
func waitInALoopUnderADeferredUnlock(a *App, cs []chan int) {
	a.Lock()
	defer a.Unlock()
	for _, c := range cs {
		<-c // +lockblockingfail=a channel receive
	}
}

func waitInABranchUnderADeferredUnlock(a *App, c chan int, cond bool) {
	a.Lock()
	defer a.Unlock()
	if cond {
		<-c // +lockblockingfail=a channel receive
	}
}

// A short circuit condition is evaluated in blocks the SSA builder numbers AFTER the branch
// they guard, so walking the blocks by index would start the guarded body with nothing held.
func shortCircuitConditionKeepsTheLock(a *App, cs []chan int, x, y bool) {
	a.Lock()
	defer a.Unlock()
	if (x || y) && len(cs) > 0 {
		for _, c := range cs {
			<-c // +lockblockingfail=a channel receive
		}
	} else {
		<-cs[0] // +lockblockingfail=a channel receive
	}
}

// --- the unlock-relock gap: the wait happens with the caller's lock released ------------------

// The shape of a registration that drops the lock before waiting for the answers. Nothing is
// held across the wait, and the summary records the release so the call sites see that too.
//
// +checklocks:a.mu
func (a *App) waitsWithTheLockReleased(wg *sync.WaitGroup) {
	a.mu.Unlock()
	defer a.mu.Lock()
	wg.Wait()
}

// +checklocks:a.mu
func (a *App) callsTheGap(wg *sync.WaitGroup) {
	a.waitsWithTheLockReleased(wg)
}

// The same callee without the gap, which is the polarity that must still be reported.
//
// +checklocks:a.mu
func (a *App) waitsWithTheLockHeld(wg *sync.WaitGroup) {
	wg.Wait() // +lockblockingfail=calling (*WaitGroup).Wait
}

// +checklocks:a.mu
func (a *App) callsTheWait(wg *sync.WaitGroup) {
	a.waitsWithTheLockHeld(wg) // +lockblockingfail=which may block
}

// --- annotations -------------------------------------------------------------------------

// resolve stands for a wait this analysis cannot see: the work happens behind an indirect
// call, so the declaration is what says it waits.
//
// +blocking
func resolve(name string) string {
	return name
}

func annotatedSinkUnderLock(a *App) {
	a.Lock()
	resolve("someone") // +lockblockingfail=which may block
	a.Unlock()
}

func lineIgnoreSuppresses(a *App, c chan int) {
	a.Lock()
	<-c // +lockblockingignore
	a.Unlock()
}

// +lockblockingignore
func functionIgnoreSuppresses(a *App, c chan int) {
	a.Lock()
	<-c
	a.Unlock()
}

// --- an annotated callback starts with the class held -----------------------------------------

// The caller took the lock and handed control over, so this runs with App held even though it
// does not take it itself.
//
// +checklocks:a.mu
func annotatedCallbackWaits(a *App, c chan int) {
	<-c // +lockblockingfail=a channel receive
}
