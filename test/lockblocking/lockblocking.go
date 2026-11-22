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

	"github.com/tigerquoll/checklocks/test/lockblocking/dep"
	"github.com/tigerquoll/checklocks/test/lockblocking/dep/inner"
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
	waitsIndirectly(c) // +lockblockingfail=which may block (calling waits at lockblocking/lockblocking.go:168 -> a channel receive at lockblocking/lockblocking.go:164)
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
	defer waits(c) // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:164)
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
	a.mu.Unlock() // +lockgapfail=caller's critical section is split
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
	a.waitsWithTheLockHeld(wg) // +lockblockingfail=which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:268)
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
	resolve("someone") // +lockblockingfail=which may block (resolve is declared blocking at lockblocking/lockblocking.go:282)
	a.Unlock()
}

// --- across a package boundary ---------------------------------------------------------

// A wait the analysis can NAME is a statement about the callee, so it holds wherever the
// callee is called from and crosses the package boundary with it.
func reachesADeclaredWaitAcrossAPackage(a *App) {
	a.Lock()
	dep.Lookup("someone") // +lockblockingfail=which may block (calling Resolve at dep/dep.go:40 -> Resolve is declared blocking at dep/dep.go:30)
	a.Unlock()
}

// The callee itself carries the declaration here, which is the shorter version of the same
// thing.
func callsADeclaredWaitAcrossAPackage(a *App) {
	a.Lock()
	dep.Resolve("someone") // +lockblockingfail=which may block (Resolve is declared blocking at dep/dep.go:30)
	a.Unlock()
}

func reachesADeclaredWaitWithNothingHeld() {
	dep.Lookup("someone")
}

// A select with a default does not wait, in another package as much as in this one.
func callsANonBlockingDependency(a *App, c chan int) {
	a.Lock()
	dep.Sleeper(c)
	a.Unlock()
}

// Not called under a lock: see the comment on dep.Drain.
func callsAnInferredWaitWithNothingHeld(c chan int) {
	dep.Drain(c)
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

// --- the path to the wait, named at the call site -----------------------------------------

// Callers above callees, three calls deep: one forward sweep cannot reach the wait.
func reachesADeepChainUnderLock(a *App, c chan int) {
	a.Lock()
	deepChainTop(c) // +lockblockingfail=which may block (calling deepChainMiddle at lockblocking/lockblocking.go:359 -> calling deepChainBottom at lockblocking/lockblocking.go:363 -> a channel receive at lockblocking/lockblocking.go:367)
	a.Unlock()
}

func deepChainTop(c chan int) {
	deepChainMiddle(c)
}

func deepChainMiddle(c chan int) {
	deepChainBottom(c)
}

func deepChainBottom(c chan int) {
	<-c
}

// The deeper path sorts first; the shorter one must win.
func twoDepths() {
	DeeperSleep()
	time.Sleep(time.Millisecond)
}

func DeeperSleep() {
	time.Sleep(time.Millisecond)
}

func reachesTwoDepthsUnderLock(a *App) {
	a.Lock()
	twoDepths() // +lockblockingfail=which may block (calling Sleep at lockblocking/lockblocking.go:373)
	a.Unlock()
}

// Two paths of the same length, the smaller of them through the later call.
func twoEqualPaths(c chan int) {
	zWait(c)
	aWait(c)
}

func aWait(c chan int) {
	<-c
}

func zWait(c chan int) {
	<-c
}

func reachesTwoEqualPathsUnderLock(a *App, c chan int) {
	a.Lock()
	twoEqualPaths(c) // +lockblockingfail=which may block (calling aWait at lockblocking/lockblocking.go:389 -> a channel receive at lockblocking/lockblocking.go:393)
	a.Unlock()
}

// Mutual recursion with an exit: the path ends at the exit's wait.
func pingWaits(c chan int, n int) {
	if n == 0 {
		<-c
		return
	}
	pongWaits(c, n-1)
}

func pongWaits(c chan int, n int) {
	pingWaits(c, n-1)
}

func reachesMutualRecursionUnderLock(a *App, c chan int) {
	a.Lock()
	pongWaits(c, 2) // +lockblockingfail=which may block (calling pingWaits at lockblocking/lockblocking.go:416 -> a channel receive at lockblocking/lockblocking.go:409)
	a.Unlock()
}

// A named wait and a channel receive at one depth; the receive sorts first, the named wait wins.
func namedAndAChannelOp(c chan int) {
	<-c
	time.Sleep(time.Millisecond)
}

func reachesNamedAndAChannelOpUnderLock(a *App, c chan int) {
	a.Lock()
	namedAndAChannelOp(c) // +lockblockingfail=which may block (calling Sleep at lockblocking/lockblocking.go:428)
	a.Unlock()
}

// One branch waits with the lock dropped, the other holding it; the path names the second.
//
// +checklocks:a.mu
func (a *App) waitsEitherWay(wg *sync.WaitGroup, c chan int, cond bool) {
	if cond {
		a.mu.Unlock() // +lockgapfail=caller's critical section is split
		wg.Wait()
		a.mu.Lock()
		return
	}
	<-c // +lockblockingfail=a channel receive
}

// +checklocks:a.mu
func (a *App) callsWaitsEitherWay(wg *sync.WaitGroup, c chan int, cond bool) {
	a.waitsEitherWay(wg, c, cond) // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:447)
}

// The wait behind the go call is not on the path, though that path is shorter.
func goCallIsNotThePath(c chan int) {
	go waits(c)
	waitsIndirectly(c)
}

func reachesPastAGoCallUnderLock(a *App, c chan int) {
	a.Lock()
	goCallIsNotThePath(c) // +lockblockingfail=which may block (calling waitsIndirectly at lockblocking/lockblocking.go:458 -> calling waits at lockblocking/lockblocking.go:168 -> a channel receive at lockblocking/lockblocking.go:164)
	a.Unlock()
}

// An ignored callee's wait does not travel to its callers.
func reachesThroughAnIgnoredCallee(c chan int) {
	ignoredWaiter(c)
	waitsIndirectly(c)
}

// +lockblockingignore
func ignoredWaiter(c chan int) {
	<-c
}

func reachesThroughAnIgnoredCalleeUnderLock(a *App, c chan int) {
	a.Lock()
	reachesThroughAnIgnoredCallee(c) // +lockblockingfail=which may block (calling waitsIndirectly at lockblocking/lockblocking.go:470 -> calling waits at lockblocking/lockblocking.go:168 -> a channel receive at lockblocking/lockblocking.go:164)
	a.Unlock()
}

// A bound method value: the path resolves the wrapper to its method.
func (a *App) methodWaits(c chan int) {
	<-c
}

func callsABoundMethod(a *App, c chan int) {
	g := a.methodWaits
	g(c)
}

func reachesABoundMethodUnderLock(a *App, c chan int) {
	a.Lock()
	callsABoundMethod(a, c) // +lockblockingfail=which may block (calling (*App).methodWaits at lockblocking/lockblocking.go:491 -> a channel receive at lockblocking/lockblocking.go:486)
	a.Unlock()
}

// The declaration is the path even though the body's own wait sorts first.
//
// +blocking
func zDeclaredAndWaits(wg *sync.WaitGroup) {
	wg.Wait()
}

func reachesADeclaredWaiterUnderLock(a *App, wg *sync.WaitGroup) {
	a.Lock()
	zDeclaredAndWaits(wg) // +lockblockingfail=which may block (zDeclaredAndWaits is declared blocking at lockblocking/lockblocking.go:503)
	a.Unlock()
}

// Side carries a second class, for a caller holding a lock the path does not release.
//
// +lockclass:Side
type Side struct {
	mu sync.Mutex
}

// +checklocksignore
func (s *Side) Lock() { s.mu.Lock() }

// +checklocksignore
func (s *Side) Unlock() { s.mu.Unlock() }

// Both declaration orders of one chain; every path releases a.mu by the call, so the named wait wins.
//
// +checklocks:a.mu
func (a *App) downF(wg *sync.WaitGroup, c chan int, cond bool) {
	a.mu.Unlock()        // +lockgapfail=caller's critical section is split
	a.downT(wg, c, cond) // +checklocksfail=must hold a.mu exclusively
	a.mu.Lock()
}

// +checklocks:a.mu
func (a *App) downT(wg *sync.WaitGroup, c chan int, cond bool) {
	if cond {
		a.mu.Unlock() // +lockgapfail=caller's critical section is split
		wg.Wait()
		a.mu.Lock()
		return
	}
	a.downU(c) // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:547)
}

func (a *App) downU(c chan int) {
	<-c
}

func reachesDownUnderLock(a *App, s *Side, wg *sync.WaitGroup, c chan int, cond bool) {
	s.Lock()
	a.downF(wg, c, cond) // +checklocksfail=must hold a.mu exclusively +lockblockingfail=which may block (calling (*App).downT at lockblocking/lockblocking.go:531 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:539)
	s.Unlock()
}

func (a *App) upU(c chan int) {
	<-c
}

// +checklocks:a.mu
func (a *App) upT(wg *sync.WaitGroup, c chan int, cond bool) {
	if cond {
		a.mu.Unlock() // +lockgapfail=caller's critical section is split
		wg.Wait()
		a.mu.Lock()
		return
	}
	a.upU(c) // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:557)
}

// +checklocks:a.mu
func (a *App) upF(wg *sync.WaitGroup, c chan int, cond bool) {
	a.mu.Unlock()      // +lockgapfail=caller's critical section is split
	a.upT(wg, c, cond) // +checklocksfail=must hold a.mu exclusively
	a.mu.Lock()
}

func reachesUpUnderLock(a *App, s *Side, wg *sync.WaitGroup, c chan int, cond bool) {
	s.Lock()
	a.upF(wg, c, cond) // +checklocksfail=must hold a.mu exclusively +lockblockingfail=which may block (calling (*App).upT at lockblocking/lockblocking.go:574 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:564)
	s.Unlock()
}

// A callee on another object releases its own lock, not this one's, so its path outranks the sleep.
//
// +checklocks:a.mu
func (a *App) sendsWithItsOwnReleased(c chan int) {
	a.mu.Unlock() // +lockgapfail=caller's critical section is split
	c <- 1
	a.mu.Lock()
}

// +checklocks:a.mu
func (a *App) sendsThenSleeps(other *App, c chan int) {
	other.sendsWithItsOwnReleased(c) // +checklocksfail=must hold a.mu exclusively
	a.mu.Unlock()                    // +lockgapfail=caller's critical section is split
	time.Sleep(time.Millisecond)
	a.mu.Lock()
}

// +checklocks:a.mu
func (a *App) callsSendsThenSleeps(other *App, c chan int) {
	a.sendsThenSleeps(other, c) // +lockorderfail=two locks of one class must not nest +lockblockingfail=which may block (calling (*App).sendsWithItsOwnReleased at lockblocking/lockblocking.go:595 -> a channel send at lockblocking/lockblocking.go:589)
}

// The sleep outranks the send beside it whether or not the dependency is in scope.
func reachesANamedWaitPastAChannelOp(a *App, c chan int) {
	a.Lock()
	dep.SleepsThenSends(c) // +lockblockingfail=which may block (calling Sleep at dep/dep.go:64)
	a.Unlock()
}

// Two waits one class apart: which of them a caller is shown depends on the class it holds.
type twoClasses struct {
	app  App
	side Side
}

// +checklocks:t.app.mu
// +checklocks:t.side.mu
func (t *twoClasses) waitsUnderTwoClasses(wg *sync.WaitGroup, cond bool) {
	if cond {
		t.app.mu.Unlock() // +lockgapfail=caller's critical section is split
		wg.Wait()         // +lockblockingfail=calling (*WaitGroup).Wait
		t.app.mu.Lock()
		return
	}
	t.side.mu.Unlock()           // +lockgapfail=caller's critical section is split
	time.Sleep(time.Millisecond) // +lockblockingfail=calling Sleep
	t.side.mu.Lock()
}

func (t *twoClasses) callsTwoClassesHoldingApp(wg *sync.WaitGroup, cond bool) {
	t.app.Lock()
	t.waitsUnderTwoClasses(wg, cond) // +checklocksfail=must hold t.app.mu|must hold t.side.mu +lockblockingfail=which may block (calling Sleep at lockblocking/lockblocking.go:629) while holding App
	t.app.Unlock()
}

func (t *twoClasses) callsTwoClassesHoldingSide(wg *sync.WaitGroup, cond bool) {
	t.side.Lock()
	t.waitsUnderTwoClasses(wg, cond) // +checklocksfail=must hold t.app.mu|must hold t.side.mu +lockblockingfail=which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:624) while holding Side
	t.side.Unlock()
}

// The wait is two packages away, so the dependency's own dependency names the last hop.
func reachesThroughTwoPackagesUnderLock(a *App, i *inner.Inner, c chan int) {
	a.Lock()
	dep.Mid(i, c) // +lockblockingfail=which may block (calling (*Inner).SendsThenSleeps at dep/dep.go:70 -> calling Sleep at inner/inner.go:36)
	a.Unlock()
}

// A caller holding both classes is shown the wait that still holds one of them.
func (t *twoClasses) callsTwoClassesHoldingBoth(wg *sync.WaitGroup, cond bool) {
	t.app.Lock()
	t.side.Lock()
	t.waitsUnderTwoClasses(wg, cond) // +checklocksfail=must hold t.app.mu|must hold t.side.mu +lockblockingfail=which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:624) while holding App, Side
	t.side.Unlock()
	t.app.Unlock()
}

// Called from a plain function, the callee still releases the lock held here, so the send is the path.
//
// +checklocks:a.mu
func (a *App) sleepsThenSends(c chan int) {
	a.mu.Unlock() // +lockgapfail=caller's critical section is split
	time.Sleep(time.Millisecond)
	a.mu.Lock()
	c <- 1 // +lockblockingfail=a channel send
}

func callsSleepsThenSends(a *App, c chan int) {
	a.Lock()
	a.sleepsThenSends(c) // +checklocksfail=must hold a.mu exclusively +lockblockingfail=which may block (a channel send at lockblocking/lockblocking.go:668) while holding App
	a.Unlock()
}

// Neither wait released a class this caller holds, so the named one is the path.
type sideWaiter struct {
	side Side
}

// +checklocks:w.side.mu
func (w *sideWaiter) sendsThenSleepsOnSide(c chan int) {
	c <- 1             // +lockblockingfail=a channel send
	w.side.mu.Unlock() // +lockgapfail=caller's critical section is split
	time.Sleep(time.Millisecond)
	w.side.mu.Lock()
}

func (w *sideWaiter) callsSideWaiterHoldingApp(a *App, c chan int) {
	a.Lock()
	w.sendsThenSleepsOnSide(c) // +checklocksfail=must hold w.side.mu exclusively +lockblockingfail=which may block (calling Sleep at lockblocking/lockblocking.go:686) while holding App
	a.Unlock()
}

// A method expression is a thunk, which stands for the method it was written as.
func (a *App) thunkWaits(c chan int) {
	<-c
}

func reachesAThunkUnderLock(a *App, c chan int) {
	f := (*App).thunkWaits
	a.Lock()
	f(a, c) // +lockblockingfail=calling (*App).thunkWaits, which may block (a channel receive at lockblocking/lockblocking.go:698)
	a.Unlock()
}

// elem is the type argument below, so that the instance's name is a qualified one.
type elem struct{}

func waitForElem[E any](c chan E) {
	<-c
}

func reachesAnInstanceUnderLock(a *App, c chan elem) {
	a.Lock()
	waitForElem(c) // +lockblockingfail=calling waitForElem, which may block (a channel receive at lockblocking/lockblocking.go:712)
	a.Unlock()
}

// Two waits one call away, which the witness tie-break decides between by their text.
type tied struct{}

func (tied) zz(c chan int) { <-c }

func tiedHelper(c chan int) { <-c }

func reachesTwoWaits(c chan int, cond bool) {
	g := tied{}.zz
	if cond {
		tiedHelper(c)
	} else {
		g(c)
	}
}

func reachesTwoWaitsUnderLock(a *App, c chan int, cond bool) {
	a.Lock()
	reachesTwoWaits(c, cond) // +lockblockingfail=which may block (calling (tied).zz at lockblocking/lockblocking.go:733 -> a channel receive at lockblocking/lockblocking.go:724)
	a.Unlock()
}

// A method of a generic type is reached through an instance, and the wait is looked up on its declaration.
type plainWaiter struct {
	c chan int
}

func (p *plainWaiter) waits() { <-p.c }

func (p *plainWaiter) callsWaitsUnderLock(a *App) {
	a.Lock()
	p.waits() // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:748)
	a.Unlock()
}

type genWaiter[T any] struct {
	c chan T
}

func (g *genWaiter[T]) waits() { <-g.c }

func (g *genWaiter[T]) callsWaitsUnderLock(a *App) {
	a.Lock()
	g.waits() // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:760)
	a.Unlock()
}

func callsInstanceWaitsUnderLock(a *App, g *genWaiter[int]) {
	a.Lock()
	g.waits() // +lockblockingfail=which may block (a channel receive at lockblocking/lockblocking.go:760)
	a.Unlock()
}

func callsCrossPackageGenericUnderLock(a *App, b *dep.Box[int]) {
	a.Lock()
	b.Sleeps() // +lockblockingfail=which may block (calling Sleep at dep/dep.go:77)
	a.Unlock()
}

// --- a downgrade is not a lock operation to this analysis ---------------------------------

var downgradeWait chan int

// +checklocksignore
func (a *App) DowngradeLock() { <-downgradeWait }

func callsDowngradeUnderLock(a *App) {
	a.Lock()
	a.DowngradeLock() // +lockblockingfail=calling (*App).DowngradeLock, which may block (a channel receive at lockblocking/lockblocking.go:785)
	a.Unlock()
}

// --- a wait a generic body makes through a type parameter ---------------------------------

type g1Waiter interface{ Wait() }

type g1B[W g1Waiter] struct{ w W }

func (b *g1B[W]) Wait() { b.w.Wait() }

type g1N struct{ w *sync.WaitGroup }

func (n *g1N) Wait() { n.w.Wait() }

func g1Twin(a *App, n *g1N) {
	a.Lock()
	n.Wait() // +lockblockingfail=calling (*g1N).Wait, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:803)
	a.Unlock()
}

func g1S4(a *App, b *g1B[*sync.WaitGroup]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

type g1Sleeps struct{}

func (*g1Sleeps) Wait() { time.Sleep(time.Millisecond) }

func g1S4b(a *App, b *g1B[*g1Sleeps]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*g1Sleeps).Wait at lockblocking/lockblocking.go:799 -> calling Sleep at lockblocking/lockblocking.go:819)
	a.Unlock()
}

type g1Quiet struct{}

func (*g1Quiet) Wait() {}

func g1S4c(a *App, b *g1B[*g1Quiet]) {
	a.Lock()
	b.Wait()
	a.Unlock()
}

// A generic that calls another generic with its own type parameter resolves nothing.
type g1Fwd[W g1Waiter] struct{ w W }

func (f *g1Fwd[W]) Wait() { (&g1B[W]{f.w}).Wait() }

func g1S4d(a *App, f *g1Fwd[*sync.WaitGroup]) {
	a.Lock()
	f.Wait()
	a.Unlock()
}

func g1MethodValue(a *App, b *g1B[*sync.WaitGroup]) {
	f := b.Wait
	a.Lock()
	f() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

type g1Gap[W g1Waiter] struct {
	App
	w W
}

// +checklocks:g.mu
func (g *g1Gap[W]) Wait() {
	g.mu.Unlock() // +lockgapfail=caller's critical section is split
	defer g.mu.Lock()
	g.w.Wait()
}

func g1ReceiverIdiom(g *g1Gap[*sync.WaitGroup]) {
	g.mu.Lock()
	g.Wait()
	g.mu.Unlock()
}

type g1Pair[A, B g1Waiter] struct {
	a A
	b B
}

func (p *g1Pair[A, B]) Wait() {
	p.a.Wait()
	p.b.Wait()
}

func g1TwoParams(a *App, p *g1Pair[*sync.WaitGroup, *g1Sleeps]) {
	a.Lock()
	p.Wait() // +lockblockingfail=calling (*g1Pair[A, B]).Wait, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:879)
	a.Unlock()
}

type g1Local struct{}

func (*g1Local) Wait() { time.Sleep(time.Millisecond) }

func g1CrossPackage(a *App, w *dep.Waiter[*g1Local], s *dep.Waiter[*dep.Sleepy]) {
	a.Lock()
	w.Wait() // +lockblockingfail=calling (*Waiter[W]).Wait, which may block (calling (*g1Local).Wait at dep/dep.go:83 -> calling Sleep at lockblocking/lockblocking.go:891)
	s.Wait() // +lockblockingfail=calling (*Waiter[W]).Wait, which may block (calling (*Sleepy).Wait at dep/dep.go:83 -> calling Sleep at dep/dep.go:88)
	a.Unlock()
}

func g1Nested(a *App, o *g1B[*g1B[*sync.WaitGroup]]) {
	a.Lock()
	o.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*g1B[W]).Wait at lockblocking/lockblocking.go:799 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

func g1S7[W g1Waiter](a *App, b W) {
	a.Lock()
	b.Wait()
	a.Unlock()
}

func g1f[W g1Waiter](w W) { w.Wait() }

func g1GenericFunc(a *App, wg *sync.WaitGroup) {
	a.Lock()
	g1f(wg) // +lockblockingfail=calling g1f, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:912)
	a.Unlock()
}

func g1Caller(b *g1B[*sync.WaitGroup]) { b.Wait() }

func g1CallersCaller(a *App, b *g1B[*sync.WaitGroup]) {
	a.Lock()
	g1Caller(b) // +lockblockingfail=calling g1Caller, which may block (calling (*g1B[W]).Wait at lockblocking/lockblocking.go:920 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

type g1Ignored struct{}

// +lockblockingignore
func (*g1Ignored) Wait() { time.Sleep(time.Millisecond) }

func g1IgnoredArg(a *App, b *g1B[*g1Ignored]) {
	a.Lock()
	b.Wait()
	a.Unlock()
}

type g1Both[W g1Waiter] struct{ w W }

func (b *g1Both[W]) Wait() {
	time.Sleep(time.Millisecond)
	b.w.Wait()
}

func g1WaitsTwice(a *App, b *g1Both[*sync.WaitGroup]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1Both[W]).Wait, which may block (calling (*WaitGroup).Wait at lockblocking/lockblocking.go:943)
	a.Unlock()
}

func g1GapCaller(a *App, g *g1Gap[*sync.WaitGroup]) {
	a.Lock()
	g1f(g) // +lockblockingfail=calling g1f, which may block (calling (*g1Gap[W]).Wait at lockblocking/lockblocking.go:912 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:864)
	a.Unlock()
}

func g1NestedGap(a *App, o *g1B[*g1Gap[*sync.WaitGroup]]) {
	a.Lock()
	g1f(o) // +lockblockingfail=calling g1f, which may block (calling (*g1B[W]).Wait at lockblocking/lockblocking.go:912 -> calling (*g1Gap[W]).Wait at lockblocking/lockblocking.go:799 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:864)
	a.Unlock()
}

func g1NestedGapDirect(a *App, o *g1B[*g1Gap[*sync.WaitGroup]]) {
	a.Lock()
	o.Wait()
	a.Unlock()
}

func g1BothCaller(b *g1Both[*sync.WaitGroup]) { b.Wait() }

func g1BothCallersCaller(a *App, b *g1Both[*sync.WaitGroup]) {
	a.Lock()
	g1BothCaller(b) // +lockblockingfail=calling g1BothCaller, which may block (calling (*g1Both[W]).Wait at lockblocking/lockblocking.go:970 -> calling Sleep at lockblocking/lockblocking.go:942)
	a.Unlock()
}

func g1DepBothCaller(b *dep.Both[*sync.WaitGroup]) { b.Wait() }

func g1DepBothCallersCaller(a *App, b *dep.Both[*sync.WaitGroup]) {
	a.Lock()
	g1DepBothCaller(b) // +lockblockingfail=calling g1DepBothCaller, which may block (calling (*Both[W]).Wait at lockblocking/lockblocking.go:978 -> calling Sleep at dep/dep.go:94)
	a.Unlock()
}

// A pointer to an instance of the body's own type parameter lifts nothing: a known miss.
type g1Lift[W g1Waiter] struct{}

func (f *g1Lift[W]) Wait() { (&g1B[*g1Both[W]]{}).Wait() }

func g1LiftCaller(a *App, f *g1Lift[*sync.WaitGroup]) {
	a.Lock()
	f.Wait()
	a.Unlock()
}

// --- a type argument's own release on a receiver chain -----------------------------------

// +lockclass:G2T
type g2T struct {
	mu sync.Mutex
	wg sync.WaitGroup
}

// +checklocks:t.mu
func (t *g2T) Wait() {
	t.mu.Unlock() // +lockgapfail=caller's critical section is split
	t.wg.Wait()
	t.mu.Lock()
}

type g2N struct{ w *g2T }

func (n *g2N) Wait() { n.w.Wait() } // +checklocksfail=must hold t.mu exclusively

func g2Twin(n *g2N) {
	n.w.mu.Lock()
	n.Wait()
	n.w.mu.Unlock()
}

func g2Generic(b *g1B[*g2T]) {
	b.w.mu.Lock()
	b.Wait()
	b.w.mu.Unlock()
}

func g2OtherClass(a *App, b *g1B[*g2T]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*g2T).Wait at lockblocking/lockblocking.go:799 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:1008)
	a.Unlock()
}

func g2f[W g1Waiter](w W) { w.Wait() }

func g2fn(t *g2T) { t.Wait() } // +checklocksfail=must hold t.mu exclusively

func g2FuncGeneric(t *g2T) {
	t.mu.Lock()
	g2f(t) // +lockblockingfail=calling g2f, which may block (calling (*g2T).Wait at lockblocking/lockblocking.go:1034 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:1008)
	t.mu.Unlock()
}

func g2FuncTwin(t *g2T) {
	t.mu.Lock()
	g2fn(t) // +lockorderfail=two locks of one class must not nest +lockblockingfail=calling g2fn, which may block (calling (*g2T).Wait at lockblocking/lockblocking.go:1036 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:1008)
	t.mu.Unlock()
}

// +lockclass:G2M
type g2M[W g1Waiter] struct {
	mu sync.Mutex
	w  W // +checklocksfail=may require checklocks annotation for mu, used with lock held 100% of the time
}

// +checklocks:m.mu
func (m *g2M[W]) Wait() {
	m.mu.Unlock() // +lockgapfail=caller's critical section is split
	time.Sleep(time.Millisecond)
	m.mu.Lock()
	m.w.Wait()
}

type g2MN struct{ w *g2M[*sync.WaitGroup] }

func (n *g2MN) Wait() { n.w.Wait() } // +checklocksfail=must hold m.mu exclusively

func g2OriginGeneric(b *g1B[*g2M[*sync.WaitGroup]]) {
	b.w.mu.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block while holding G2M
	b.w.mu.Unlock()
}

func g2OriginTwin(n *g2MN) {
	n.w.mu.Lock()
	n.Wait() // +lockblockingfail=calling (*g2MN).Wait, which may block while holding G2M
	n.w.mu.Unlock()
}

type g2U struct{}

func (*g2U) Wait() { time.Sleep(time.Millisecond) }

func g2PairGeneric(p *g1Pair[*g2T, *g2U]) {
	p.a.mu.Lock()
	p.Wait() // +lockblockingfail=calling (*g1Pair[A, B]).Wait, which may block (calling (*g2U).Wait at lockblocking/lockblocking.go:880 -> calling Sleep at lockblocking/lockblocking.go:1082)
	p.a.mu.Unlock()
}

type g2PN struct {
	a *g2T
	b *g2U
}

func (p *g2PN) Wait() {
	p.a.Wait() // +checklocksfail=must hold t.mu exclusively
	p.b.Wait()
}

func g2PairTwin(p *g2PN) {
	p.a.mu.Lock()
	p.Wait() // +lockblockingfail=calling (*g2PN).Wait, which may block (calling (*g2U).Wait at lockblocking/lockblocking.go:1097 -> calling Sleep at lockblocking/lockblocking.go:1082)
	p.a.mu.Unlock()
}

func g2DeepGeneric(b *g1B[*g2T]) {
	b.w.mu.Lock()
	g1f(b) // +lockblockingfail=calling g1f, which may block (calling (*g1B[W]).Wait at lockblocking/lockblocking.go:912 -> calling (*g2T).Wait at lockblocking/lockblocking.go:799 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:1008)
	b.w.mu.Unlock()
}

func g2fnN(n *g2N) { n.Wait() }

func g2DeepTwin(n *g2N) {
	n.w.mu.Lock()
	g2fnN(n) // +lockorderfail=two locks of one class must not nest +lockblockingfail=calling g2fnN, which may block (calling (*g2N).Wait at lockblocking/lockblocking.go:1112 -> calling (*g2T).Wait at lockblocking/lockblocking.go:1014 -> calling (*WaitGroup).Wait at lockblocking/lockblocking.go:1008)
	n.w.mu.Unlock()
}

func g2NestGeneric(b *g1B[*g1B[*g2T]]) {
	b.w.w.mu.Lock()
	b.Wait()
	b.w.w.mu.Unlock()
}

type g2N2 struct{ w *g2N }

func (n *g2N2) Wait() { n.w.Wait() }

func g2NestTwin(n *g2N2) {
	n.w.w.mu.Lock()
	n.Wait()
	n.w.w.mu.Unlock()
}

// --- each ignore annotation suppresses only its own analyzer's part of a summary ----------

// +lockorderignore
func g3F1(o *App) {
	time.Sleep(time.Millisecond)
	o.Lock()
	o.Unlock()
}

func g3W1(o *App) { g3F1(o) }

func g3W1b(o *App) { g3W1(o) }

func g3T1(a, o *App) {
	a.Lock()
	g3F1(o) // +lockblockingfail=calling g3F1, which may block (calling Sleep at lockblocking/lockblocking.go:1140)
	a.Unlock()
}

func g3T1w(a, o *App) {
	a.Lock()
	g3W1(o) // +lockblockingfail=calling g3W1, which may block (calling g3F1 at lockblocking/lockblocking.go:1145 -> calling Sleep at lockblocking/lockblocking.go:1140)
	a.Unlock()
}

func g3T1w2(a, o *App) {
	a.Lock()
	g3W1b(o) // +lockblockingfail=calling g3W1b, which may block (calling g3W1 at lockblocking/lockblocking.go:1147 -> calling g3F1 at lockblocking/lockblocking.go:1145 -> calling Sleep at lockblocking/lockblocking.go:1140)
	a.Unlock()
}

// +lockblockingignore
func g3F2() { time.Sleep(time.Millisecond) }

func g3W2() { g3F2() }

func g3W2b() { g3W2() }

func g3T2(a *App) {
	a.Lock()
	g3F2()
	g3W2()
	g3W2b()
	a.Unlock()
}

func g3W3() {
	g3F2()
	time.Sleep(time.Millisecond)
}

func g3T3(a *App) {
	a.Lock()
	g3W3() // +lockblockingfail=calling g3W3, which may block (calling Sleep at lockblocking/lockblocking.go:1184)
	a.Unlock()
}

// +blocking
// +lockblockingignore
func g3F4() { time.Sleep(time.Millisecond) }

func g3W4() { g3F4() }

func g3T4(a *App) {
	a.Lock()
	g3F4() // +lockblockingfail=calling g3F4, which may block
	a.Unlock()
}

func g3T4w(a *App) {
	a.Lock()
	g3W4() // +lockblockingfail=calling g3W4, which may block (calling g3F4 at lockblocking/lockblocking.go:1197 -> g3F4 is declared blocking at lockblocking/lockblocking.go:1195)
	a.Unlock()
}

type g3X struct{}

// +lockorderignore
func (*g3X) Wait() { time.Sleep(time.Millisecond) }

func g3T5(a *App, b *g1B[*g3X]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*g3X).Wait at lockblocking/lockblocking.go:799 -> calling Sleep at lockblocking/lockblocking.go:1214)
	a.Unlock()
}

func g3W5(b *g1B[*g3X]) { b.Wait() }

func g3T5w(a *App, b *g1B[*g3X]) {
	a.Lock()
	g3W5(b) // +lockblockingfail=calling g3W5, which may block (calling (*g1B[W]).Wait at lockblocking/lockblocking.go:1222 -> calling (*g3X).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

func g3W6() { dep.OrderIgnoredSleeps() }

func g3T6(a *App) {
	a.Lock()
	g3W6() // +lockblockingfail=calling g3W6, which may block (calling OrderIgnoredSleeps at lockblocking/lockblocking.go:1230 -> calling Sleep at dep/dep.go:99)
	a.Unlock()
}

type g3D struct{}

// +blocking
// +lockblockingignore
func (*g3D) Wait() { time.Sleep(time.Millisecond) }

func g3T7(a *App, b *g1B[*g3D]) {
	a.Lock()
	b.Wait() // +lockblockingfail=calling (*g1B[W]).Wait, which may block (calling (*g3D).Wait at lockblocking/lockblocking.go:799)
	a.Unlock()
}

// +lockblockingignore
func g3gf[W interface{ Wait() }](w W) { w.Wait() }

func g3W8(w *sync.WaitGroup) { g3gf(w) }

func g3T8(a *App, w *sync.WaitGroup) {
	a.Lock()
	g3gf(w)
	g3W8(w)
	a.Unlock()
}

type g3ibox[W g1Waiter] struct{ w W }

// +lockblockingignore
func (b *g3ibox[W]) Wait() { b.w.Wait() }

func g3T8b(a *App, b *g1B[*g3ibox[*sync.WaitGroup]]) {
	a.Lock()
	b.Wait()
	a.Unlock()
}

func g3W9() { dep.BlockingIgnoredSleeps() }

func g3T9(a *App) {
	a.Lock()
	g3W9()
	a.Unlock()
}
