// Copyright 2020 The gVisor Authors.
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

package test

import "sync"

type lfStruct struct {
	mu   sync.Mutex
	next *lfStruct
}

func lfNoop() {}

func lfGet(s *lfStruct) *lfStruct { return s }

// The literal's own defer does not run the caller's pending unlock.
func lfUnlockRelock(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.mu.Unlock() // +lockgapfail=taken again
	func() { defer lfNoop() }()
	t.mu.Lock()
}

// A literal that releases the caller's lock leaves it released for the caller.
func lfReleases(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		t.mu.Unlock()
	}()
}

// Without a defer, a lock the literal released is not held by the caller after it.
func lfReleased(t *oneGuardStruct) {
	t.mu.Lock()
	func() { t.mu.Unlock() }()
	t.guardedField = 1 // +checklocksfail
}

// The same, with the lock passed as an argument.
func lfArgReleases(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func(m *sync.Mutex) {
		defer lfNoop()
		m.Unlock()
	}(&t.mu)
}

// A lock the literal takes is its own to release.
func lfAcquires(t *oneGuardStruct) {
	func() {
		t.mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	t.guardedField = 1 // +checklocksfail
}

// Exits that hold different locks leave the caller's state as it was.
func lfDisagree(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	func() { // +checklocksfail=incompatible return states
		if c {
			t.mu.Unlock()
		}
	}()
	t.mu.Unlock()
}

// Exits that disagree under the caller's pending defer: the literal is walked with the defer again.
func lfRelMaybeDefer(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		if c {
			t.mu.Unlock()
		}
	}()
}

// A literal that frees a lock and takes the same one under another name is not folded.
func lfRetake(s *lfStruct) {
	s.mu.Lock()
	func() {
		s.mu.Unlock()
		lfGet(s).mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	s.mu.Unlock()
}

// A captured cursor moved on inside the literal.
func lfHandOver(s *lfStruct) {
	p := s
	p.mu.Lock()
	func() {
		p.mu.Unlock() // +lockgapfail=taken again
		p = p.next
		p.mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	p.mu.Unlock()
}

// A literal that never returns folds nothing.
func lfNoExit(t *oneGuardStruct) {
	t.mu.Lock()
	func() { panic("x") }()
	t.mu.Unlock()
}

// A deferred literal's release is the state the next deferred call runs in.
func lfDeferredLit(t *oneGuardStruct) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	defer func() { t.mu.Unlock() }()
}

// A literal inside a literal that is walked again with the caller's defers is walked that way too.
func lfNest(t, u, w *oneGuardStruct, c bool) {
	t.mu.Lock()
	u.mu.Lock()
	defer t.mu.Unlock()
	defer u.mu.Unlock() // +checklocksfail=already unlocked

	func() { // +checklocksfail=incompatible return states
		defer lfNoop()
		u.mu.Unlock() // +lockgapfail=taken again
		func() { defer lfNoop() }()
		u.mu.Lock()
		if c {
			w.mu.Lock()
		}
	}() // +checklocksfail=return with unexpected locks held
}

var lfSink func()

// The closures of a literal walked again are analyzed again.
func lfRewalkClosure(t *oneGuardStruct, c bool) {
	t.mu.Lock()
	defer t.mu.Unlock() // +checklocksfail=already unlocked
	func() {
		defer lfNoop()
		if c {
			t.mu.Unlock()
		}
		lfSink = func() {
			t.guardedField = 1 // +checklocksfail
		}
	}()
}

// A literal inside a literal folds the same way whichever arm of the caller arrived first.
func lfInherits(t *oneGuardStruct, c, d bool) {
	t.mu.Lock()
	if c {
		defer lfNoop()
	} else {
		lfNoop()
	}
	func(t *oneGuardStruct) { // +checklocksfail=incompatible return states
		func(t *oneGuardStruct) { t.mu.Unlock() }(t)
		t.guardedField = 1 // +checklocksfail
		if d {
			t.mu.Lock()
		}
	}(t)
} // +checklocksfail=return with unexpected locks held

var lfKeep func(*oneGuardStruct)

// A closure that escapes from a literal walked again is not walked the old way.
func lfEscapeRewalk(u, w *oneGuardStruct, d bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	func(w *oneGuardStruct) { // +checklocksfail=incompatible return states
		lfKeep = func(t *oneGuardStruct) {
			lfSinkT(w)
			t.mu.Lock()
			func(t *oneGuardStruct) { t.mu.Unlock() }(t)
			t.mu.Lock()
			t.mu.Unlock()
		}
		if d {
			w.mu.Lock()
		}
	}(w) // +checklocksfail=return with unexpected locks held
}

// The same, with the caller's lock and defer taken in one arm.
func lfEscapeRewalkArm(u, w *oneGuardStruct, c, d bool) {
	if c {
		u.mu.Lock()
		defer u.mu.Unlock()
	}
	func(w *oneGuardStruct) { // +checklocksfail=incompatible return states|incompatible return states
		lfKeep = func(t *oneGuardStruct) {
			lfSinkT(w)
			t.mu.Lock()
			func(t *oneGuardStruct) { t.mu.Unlock() }(t)
			t.mu.Lock()
			t.mu.Unlock()
		}
		if d {
			w.mu.Lock()
		}
	}(w) // +checklocksfail=return with unexpected locks held|return with unexpected locks held
}

func lfSinkT(*oneGuardStruct) {}

// A literal that points a captured variable at another lock does not fold.
func lfRepoint(x *oneGuardStruct, y *lfStruct) {
	p := &x.mu
	p.Lock()
	func() {
		p.Unlock() // +lockgapfail=taken again
		p = &y.mu
		p.Lock()
	}() // +checklocksfail=return with unexpected locks held
	p.Unlock()
}

// The same, pointing it at the same lock.
func lfRepointSame(x *oneGuardStruct) {
	p := &x.mu
	p.Lock()
	func() {
		p.Unlock() // +lockgapfail=taken again
		p = &x.mu
		p.Lock()
	}() // +checklocksfail=return with unexpected locks held
	p.Unlock()
}

var lfGlobalMu *sync.Mutex

// The same, through a global.
func lfRepointGlobal(x *oneGuardStruct, y *lfStruct) {
	lfGlobalMu = &x.mu
	lfGlobalMu.Lock()
	func() {
		lfGlobalMu.Unlock() // +lockgapfail=taken again
		lfGlobalMu = &y.mu
		lfGlobalMu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	lfGlobalMu.Unlock()
}

// The same, from a literal inside the literal.
func lfRepointNested(x *oneGuardStruct, y *lfStruct) {
	p := &x.mu
	p.Lock()
	func() {
		p.Unlock()
		func() {
			p = &y.mu
			p.Lock()
		}() // +checklocksfail=return with unexpected locks held
	}()
	p.Unlock()
}

func lfCompute() int { return 1 }

// A literal that writes a captured result does not keep the caller from folding.
func lfResult(t *oneGuardStruct) {
	var r int
	t.mu.Lock()
	func() {
		defer t.mu.Unlock()
		r = lfCompute()
	}()
	t.mu.Lock()
	t.guardedField = r
	t.mu.Unlock()
}

type lfA struct {
	mu sync.Mutex
	pm *sync.Mutex
}

type lfB struct{ mu sync.Mutex }

type lfC struct{ mu sync.Mutex }

type lfH struct{ p *sync.Mutex }

var lfG1, lfG4, lfG7, lfGF, lfGS *sync.Mutex

// +checklocksacquire:lfG1
func lfLockG1() { lfG1.Lock() }

// +checklocksrelease:lfG1
func lfUnlockG1() { lfG1.Unlock() }

// +checklocksacquire:lfGF
func lfLockGF() { lfGF.Lock() }

// +checklocksrelease:lfGF
func lfUnlockGF() { lfGF.Unlock() }

// A lock handed over between two names with no store is not folded.
func lfAliasNoStore(y *lfB) {
	lfLockG1()
	func() {
		lfUnlockG1()
		y.mu.Lock()
	}() // +checklocksfail=return with unexpected locks held
	lfUnlockG1()
}

// A lock handed over to another object is not folded.
func lfHandOverDiff(x *lfA, y *lfB) {
	x.mu.Lock()
	func() {
		y.mu.Lock()
		x.mu.Unlock()
	}() // +checklocksfail=return with unexpected locks held
	y.mu.Unlock() // +checklocksfail=already unlocked
} // +checklocksfail=return with unexpected locks held

// A store to a captured variable the caller reads refuses the fold.
func lfSwapCaptured(x *lfA, y *lfB) {
	p := &y.mu
	x.mu.Lock()
	y.mu.Lock()
	func() {
		y.mu.Unlock()
		p = &x.mu
	}()
	p.Unlock()
} // +checklocksfail=return with unexpected locks held

// A store to a global, from a nested literal, refuses the fold.
func lfSwapSibling(x *lfA, y *lfB) {
	lfG7 = &y.mu
	x.mu.Lock()
	y.mu.Lock()
	func(x *lfA, y *lfB) {
		func(x *lfA, y *lfB) {
			y.mu.Unlock()
			lfG7 = &x.mu
		}(x, y)
		func() {
			lfG7.Unlock()
		}()
	}(x, y)
} // +checklocksfail=return with unexpected locks held

// A store to a field refuses the fold.
func lfSwapField(h *lfH, x *lfA, y *lfB) {
	h.p = &y.mu
	x.mu.Lock()
	h.p.Lock()
	func() {
		h.p.Unlock()
		h.p = &x.mu
	}()
	h.p.Unlock()
} // +checklocksfail=return with unexpected locks held

// A store through a captured pointer refuses the fold.
func lfSwapCapPtr(x *lfA, y *lfB) {
	p := &y.mu
	pp := &p
	x.mu.Lock()
	y.mu.Lock()
	func() {
		y.mu.Unlock()
		*pp = &x.mu
	}()
	p.Unlock()
} // +checklocksfail=return with unexpected locks held

// A store through a pointer argument refuses the fold.
func lfSwapPtr(x *lfA, y *lfB) {
	p := &y.mu
	x.mu.Lock()
	y.mu.Lock()
	func(pp **sync.Mutex, x *lfA, y *lfB) {
		y.mu.Unlock()
		*pp = &x.mu
	}(&p, x, y)
	p.Unlock()
} // +checklocksfail=return with unexpected locks held

// A store of a value that holds no pointer does not refuse the fold.
func lfCountStore(c *lfC, n *int) {
	c.mu.Lock()
	defer func() {
		*n++
		c.mu.Unlock()
	}()
}

// A store to a composite local of the literal does not refuse the fold.
func lfLocalComposite(x *lfA, y *lfB) {
	x.mu.Lock()
	func() {
		h := lfH{p: &y.mu}
		_ = h
		x.mu.Unlock()
	}()
	x.mu.Lock()
	x.mu.Unlock()
}

// A store to a free variable the caller does not read does not refuse the fold.
func lfErrStore(x *lfA, f func() error) (err error) {
	x.mu.Lock()
	func() {
		defer x.mu.Unlock()
		err = f()
	}()
	x.mu.Lock()
	x.mu.Unlock()
	return err
}

// A global re-pointed under a lock fact is not folded.
func lfGlobalFacts(x *lfA, y *lfB) {
	lfLockGF()
	func() {
		lfGF.Unlock() // +lockgapfail=taken again
		lfGF = &y.mu
		lfGF.Lock()
	}() // +checklocksfail=return with unexpected locks held
	lfUnlockGF()
}

// A global re-pointed in one literal and read in a sibling is not folded.
func lfGlobalSibling(x *lfA, y *lfB) {
	lfGS = &x.mu
	lfGS.Lock()
	func() {
		func() {
			lfGS.Unlock() // +lockgapfail=taken again
			lfGS = &y.mu
			lfGS.Lock()
		}() // +checklocksfail=return with unexpected locks held
		func() {
			lfGS.Unlock()
		}()
	}()
} // +checklocksfail=return with unexpected locks held

// The same, with the sibling releasing under defer.
func lfGlobalSiblingDefer(x *lfA, y *lfB) {
	lfG4 = &x.mu
	lfG4.Lock()
	func() {
		func() {
			lfG4.Unlock() // +lockgapfail=taken again
			lfG4 = &y.mu
			lfG4.Lock()
		}() // +checklocksfail=return with unexpected locks held
		func() {
			defer lfG4.Unlock()
		}()
	}()
} // +checklocksfail=return with unexpected locks held

// A pointer stored through a field is not folded.
func lfFieldPtr(x *lfA, y *lfB) {
	x.pm.Lock()
	func(x *lfA) {
		q := &x.pm
		(*q).Unlock() // +lockgapfail=taken again
		*q = &y.mu
		(*q).Lock()
	}(x) // +checklocksfail=return with unexpected locks held
	x.pm.Unlock()
}

func lfSetPtr(pp **sync.Mutex, m *sync.Mutex) { *pp = m }

// Known gap: a store made by a callee of the literal is not seen.
func lfSwapViaCallee(x *lfA, y *lfB) {
	p := &y.mu
	x.mu.Lock()
	y.mu.Lock()
	func() {
		y.mu.Unlock()
		lfSetPtr(&p, &x.mu)
	}()
	p.Unlock() // +checklocksfail=already unlocked
} // +checklocksfail=return with unexpected locks held

// Known gap: a lock freed in one literal and taken again in another gives two rows.
func lfRelPair(x *lfA) {
	x.mu.Lock()
	func() { x.mu.Unlock() }()
	func() { x.mu.Lock() }() // +checklocksfail=return with unexpected locks held
	x.mu.Unlock()            // +checklocksfail=already unlocked
}

// Known gap: the same through named closures.
func lfRelPairNamed(x *lfA) {
	x.mu.Lock()
	unlock := func() { x.mu.Unlock() }
	relock := func() { x.mu.Lock() } // +checklocksfail=return with unexpected locks held
	unlock()
	relock()
	x.mu.Unlock() // +checklocksfail=already unlocked
}

type lfRelocker interface{ relock() }

// Known gap: a lock freed in a literal and taken again by a call the engine cannot see into.
func lfRelIface(c *lfC, r lfRelocker) {
	c.mu.Lock()
	func() { c.mu.Unlock() }()
	r.relock()
	c.mu.Unlock() // +checklocksfail=already unlocked
}

// Known gap: the same, through a function value.
func lfRelFunc(c *lfC, f func()) {
	c.mu.Lock()
	func() { c.mu.Unlock() }()
	f()
	c.mu.Unlock() // +checklocksfail=already unlocked
}

// The same without the literal.
func lfRelIfacePlain(c *lfC, r lfRelocker) {
	c.mu.Lock()
	c.mu.Unlock()
	r.relock()
	c.mu.Unlock() // +checklocksfail=already unlocked
}
