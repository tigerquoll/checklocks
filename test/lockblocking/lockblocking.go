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

// A lockblocking ignore does not reach the ordering walk the path comes from, so the hop stays.
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
	reachesThroughAnIgnoredCallee(c) // +lockblockingfail=which may block (calling ignoredWaiter at lockblocking/lockblocking.go:469 -> a channel receive at lockblocking/lockblocking.go:475)
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
