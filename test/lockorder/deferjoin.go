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

package lockorder

import "sync"

// The functions here defer calls that cannot change a lock state, in shapes where two paths
// reach a block with different deferred calls pending. The site dump holds every such call.

type djPool struct{}

func (*djPool) put(b *[]byte) {}

func djNoop()  {}
func djNoop2() {}

type djGuarded struct {
	mu sync.Mutex
	x  int // +checklocks:mu
}

// djArms defers a different no-op in each arm of an if.
func djArms(p *djPool, c bool) {
	var b []byte
	if c {
		defer p.put(&b)
	} else {
		defer p.put(nil)
	}
	_ = b
}

// djUnderUnlock defers a no-op under a deferred unlock, in one arm.
func djUnderUnlock(s *djGuarded, c bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c {
		defer djNoop()
	} else {
		s.x = 1
	}
	return nil
}

// djLastArm defers a no-op in the default arm only, then runs more code.
func djLastArm(c int) int {
	n := 0
	switch {
	case c == 1:
		n = 1
	case c == 2:
		n = 2
	default:
		defer djNoop()
		n = 3
	}
	djNoop2()
	return n
}

// +checklocks:s.mu
func djTouch(s *djGuarded) { s.x++ }

// djLoopGap pushes a no-op below or above a relevant defer, by iteration.
func djLoopGap(s *djGuarded, a, b []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range a {
		if a[i] {
			defer djNoop()
		}
		if b[i] {
			defer djTouch(s)
		}
	}
}

// djLoopGap2 pushes the no-op or the relevant defer, by iteration.
func djLoopGap2(s *djGuarded, a []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range a {
		if a[i] {
			defer djNoop()
		} else {
			defer djTouch(s)
		}
	}
	djNoop2()
}

// --- a no-op defer that locks a lower class, run holding an entry lock ----------------

// +checklockslocktype
// +lockclass:DjGuard
type DjGuard struct{ mu sync.Mutex }

func (g *DjGuard) Lock() { g.mu.Lock() }

func (g *DjGuard) Unlock() { g.mu.Unlock() }

// +lockclass:DjPool
type DjPool struct{ mu sync.Mutex }

// +checklocksignore
func (p *DjPool) Lock() { p.mu.Lock() }

// +checklocksignore
func (p *DjPool) Unlock() { p.mu.Unlock() }

// +checklockslocktype
// +lockclass:DjGuardH
type DjGuardH struct{ mu sync.Mutex }

func (g *DjGuardH) Lock() { g.mu.Lock() }

func (g *DjGuardH) Unlock() { g.mu.Unlock() }

type djInfo struct {
	g DjGuard
	h DjGuardH
}

// djEntry holds its guard two fields deep, so only the engine classes an entry lock on it.
type djEntry struct{ info djInfo }

func djTakePool(p *DjPool) { p.Lock(); p.Unlock() }

func djOther() {}

// +checklocksrelease:e.info.g
func djReleaseGuard(e *djEntry) { e.info.g.Unlock() }

// The defer is on every path.
//
// +checklocks:e.info.g
func djAlways(e *djEntry, p *DjPool) {
	defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
}

// The defer is on one path only.
//
// +checklocks:e.info.g
func djOneArm(e *djEntry, p *DjPool, c bool) {
	if c {
		defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
	}
}

// +checklocks:e.info.g
func djThenArm(e *djEntry, p *DjPool, c bool) {
	if c {
		defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
	} else {
		djOther()
	}
}

// +checklocks:e.info.g
func djElseArm(e *djEntry, p *DjPool, c bool) {
	if c {
		djOther()
	} else {
		defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
	}
}

// The no-op and the release are pushed together on every pass: the release is on top at
// the return, so the no-op never runs holding the guard.
//
// +checklocksrelease:e.info.g
func djLoopRelease(e *djEntry, p *DjPool, c func() bool) {
	for {
		defer djTakePool(p)
		defer djReleaseGuard(e)
		if c() {
			return
		}
	}
}

// As djLoopRelease with the release pushed first: the no-op is on top and runs holding the guard.
//
// +checklocksrelease:e.info.g
func djLoopReleaseFirst(e *djEntry, p *DjPool, c func() bool) {
	for {
		defer djReleaseGuard(e)
		defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
		if c() {
			return
		}
	}
}

// Each is pushed on its own condition: a pass that pushes only the release, then one that
// pushes only the no-op, runs the no-op holding the guard.
//
// +checklocks:e.info.g
func djLoopEither(e *djEntry, p *DjPool, a, b, c func() bool) { // +checklocksfail=incompatible return states
	for {
		if a() {
			defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
		}
		if b() {
			defer djReleaseGuard(e)
		}
		if c() {
			return // +checklocksfail=not held exclusively
		}
	}
}

// +checklocksrelease:e.info.h
func djReleaseGuardH(e *djEntry) { e.info.h.Unlock() }

// +checklocksacquire:e.info.g
func djAcquireGuard(e *djEntry) { e.info.g.Lock() }

// +checklocksacquire:e.info.h
func djAcquireGuardH(e *djEntry) { e.info.h.Lock() }

// The cases below pin the locks held at their djTakePool site (TestSiteDump). Their lockorder
// reports are none: the engine reads a deferred call at a return block only.

// The no-op repeats below the release, so djTakePool runs holding the guard. The first defer
// acquires h, so only its diff in the unwind puts h in the no-op's held set.
//
// +checklocks:e.info.g
func djPinRepeat(e *djEntry, p *DjPool, c func() bool) { // +checklocksfail=incompatible return states
	for {
		defer djAcquireGuardH(e)
		if c() {
			return // +checklocksfail=not held exclusively|return with unexpected locks held
		}
		defer djReleaseGuard(e)
		defer djTakePool(p)
	}
}

// As djPinRepeat with a second release under the first.
//
// +checklocksrelease:e.info.g
// +checklocksrelease:e.info.h
func djPinTwoRelease(e *djEntry, p *DjPool, c func() bool) { // +checklocksfail=incompatible return states
	for {
		defer djReleaseGuardH(e)
		if c() {
			return // +checklocksfail=return with unexpected locks held
		}
		defer djReleaseGuard(e)
		defer djTakePool(p)
	}
}

// djTakePool skips the release of h and runs after an acquire of g, so it holds both.
//
// +checklocks:e.info.h
func djPinAcquire(e *djEntry, p *DjPool, c func() bool) { // +checklocksfail=incompatible return states
	for {
		defer djAcquireGuard(e)
		if c() {
			return // +checklocksfail=not held exclusively|return with unexpected locks held
		}
		defer djReleaseGuardH(e)
		defer djTakePool(p)
	}
}

// djTakePool runs after the release of the guard, which sits on top of it in the stack.
//
// +checklocksrelease:e.info.g
func djPinReleased(e *djEntry, p *DjPool, c func() bool) {
	for {
		defer djReleaseGuard(e)
		if c() {
			return
		}
		defer djOther()
		defer djTakePool(p)
	}
}

// The cases below pin a no-op defer to the releases and acquires pushed after it.

// At exit, per pass in reverse: release, then the no-op without the guard, then acquire.
//
// +checklocks:e.info.g
func djPinRelock(e *djEntry, p *DjPool, c func() bool) {
	for {
		defer djAcquireGuard(e)
		defer djTakePool(p)
		defer djReleaseGuard(e)
		if c() {
			return
		}
	}
}

// The guard is dropped before the loop; a later pass runs the no-op above the acquire pushed
// after the return check, holding the guard.
//
// +checklocksrelease:e.info.g
func djPinVis(e *djEntry, p *DjPool, c func() bool) {
	djReleaseGuard(e)
	for {
		defer djReleaseGuard(e) // +checklocksfail=attempt to release|must hold
		defer djTakePool(p)     // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
		if c() {
			return
		}
		defer djAcquireGuard(e)
	}
}

// The no-op is always under the later release of the guard, so it never holds it.
//
// +checklocksrelease:e.info.g
// +checklocksrelease:e.info.h
func djPinTwoRel(e *djEntry, p *DjPool, x, c func() bool) { // +checklocksfail=incompatible return states
	for {
		defer djTakePool(p)
		if x() {
			defer djReleaseGuardH(e)
		}
		defer djReleaseGuard(e)
		if c() {
			return // +checklocksfail=return with unexpected locks held
		}
	}
}

// A forced acquire turns the assumed guard into a held one, so the no-op runs holding it.
//
// +checklocks:e.info.g
func djPinAssumed(e *djEntry, p *DjPool, c func() bool) {
	djReleaseGuard(e)
	djNeedGuard(e) // +checklocksforce
	for {
		defer djTakePool(p) // +lockorderfail=acquiring DjPool (via djTakePool) while holding DjGuard
		if c() {
			return
		}
		defer djAcquireGuard(e) // +checklocksforce
	}
}

// +checklocks:e.info.g
func djNeedGuard(e *djEntry) {}

type djBox struct{ mu sync.Mutex }

// The literal run by a defer ends without running the pending acquire of its caller.
//
// +checklocksacquire:b.mu
func djPinLiteral(b *djBox) {
	defer b.mu.Lock()
	defer func() {
		defer djOther()
	}()
}

// djFailedUnlock unlocks twice by defer under a no-op; the second unlock fails and checklocks is silenced.
//
// +checklocksignore
func djFailedUnlock(s *djGuarded) {
	s.mu.Lock()
	defer djNoop()
	defer s.mu.Unlock()
	defer s.mu.Unlock()
}

// djCleanUnlock is the same with one unlock, which every site answers exactly.
func djCleanUnlock(s *djGuarded) {
	s.mu.Lock()
	defer djNoop()
	defer s.mu.Unlock()
}

// djLiteralUnlock defers an unlock in a literal, which replay does not walk, and the unlock itself; the literal's fails.
//
// +checklocksignore
func djLiteralUnlock(s *djGuarded) {
	s.mu.Lock()
	defer djNoop()
	defer func() { s.mu.Unlock() }()
	defer s.mu.Unlock()
}

// djForcedPrecondition forces the lock the deferred call needs, which inserts an assumed lock.
func djForcedPrecondition(s *djGuarded) {
	defer djNoop()
	defer djTouch(s) // +checklocksforce
}

// djLiteralReleases calls a literal that releases the lock under a pending unlock; the unlock finds it released.
func djLiteralReleases(s *djGuarded) {
	s.mu.Lock()
	defer s.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer djNoop()
		s.mu.Unlock()
	}()
}

// djLiteralArgReleases releases the lock in a literal that captures nothing and takes it as an argument.
func djLiteralArgReleases(s *djGuarded) {
	s.mu.Lock()
	defer s.mu.Unlock() // +checklocksfail=already unlocked
	func(m *sync.Mutex) {
		defer djNoop()
		m.Unlock()
	}(&s.mu)
}
