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

// The functions here call function literals in shapes whose sites the engine records, or does
// not. The site dump holds every site that has a record.

type litGuarded struct {
	mu sync.Mutex
}

type litSeven struct {
	a, b, c, d, e, f, g sync.Mutex
}

func litSink() {}

func litKeep(f func()) {}

// litTwoStates calls one literal under mu and without it.
func litTwoStates(s *litGuarded, c bool) {
	f := func() { litSink() }
	if c {
		s.mu.Lock()
		f()
		s.mu.Unlock()
	} else {
		f()
	}
}

// litArms defers a different no-op in each arm of an if, inside a literal.
func litArms(p *djPool, c bool) {
	func() {
		var b []byte
		if c {
			defer p.put(&b)
		} else {
			defer p.put(nil)
		}
		_ = b
	}()
}

// litGap defers a no-op before and after a relevant defer, inside a literal.
func litGap(s *litGuarded) {
	func() {
		s.mu.Lock()
		defer djNoop()
		defer s.mu.Unlock()
		defer djNoop2()
	}()
}

// litEscapeFree calls a capture-free literal under mu, then starts it as a goroutine.
func litEscapeFree(s *litGuarded) {
	f := func(m *sync.Mutex) { litSink() }
	s.mu.Lock()
	f(&s.mu)
	s.mu.Unlock()
	go f(&s.mu)
}

// litEscapeClosure calls a closure that takes mu, then starts it as a goroutine.
func litEscapeClosure(s *litGuarded) {
	f := func() {
		s.mu.Lock()
		s.mu.Unlock()
	}
	f()
	go f()
}

// litNested calls a literal that calls a literal, under mu, then hands the outer one away.
func litNested(s *litGuarded) {
	outer := func() {
		inner := func() { println(s != nil) }
		inner()
	}
	s.mu.Lock()
	outer()
	s.mu.Unlock()
	litKeep(outer)
}

// litDeferred defers a literal, and calls another literal that defers a no-op, under mu.
func litDeferred(s *litGuarded) {
	defer func() { litSink() }()
	s.mu.Lock()
	func() { defer djNoop() }()
	s.mu.Unlock()
}

// litNestGiveUp calls a literal twice, and the second call takes it over the cycle cap.
//
// +checklocksignore
func litNestGiveUp(f *litSeven, c func() bool) {
	outer := func() {
		inner := func() { litSink() }
		for c() {
			if c() {
				f.a.Lock()
			}
			if c() {
				f.b.Lock()
			}
			if c() {
				f.c.Lock()
			}
			if c() {
				f.d.Lock()
			}
			if c() {
				f.e.Lock()
			}
			if c() {
				f.f.Lock()
			}
			if c() {
				f.g.Lock()
			}
		}
		inner()
	}
	f.a.Lock()
	f.b.Lock()
	outer()
	f.b.Unlock()
	f.a.Unlock()
	outer()
}

// litGaveUp calls a literal that is over the cycle cap, under a lock its caller holds.
//
// +checklocksignore
func litGaveUp(s *litGuarded, f *litSeven, c func() bool) {
	s.mu.Lock()
	litSink()
	func() {
		for c() {
			if c() {
				f.a.Lock()
			}
			if c() {
				f.b.Lock()
			}
			if c() {
				f.c.Lock()
			}
			if c() {
				f.d.Lock()
			}
			if c() {
				f.e.Lock()
			}
			if c() {
				f.f.Lock()
			}
			if c() {
				f.g.Lock()
			}
		}
	}()
	litSink()
	s.mu.Unlock()
}

// litGapTop is litGap without the literal.
func litGapTop(s *litGuarded) {
	s.mu.Lock()
	defer djNoop()
	defer s.mu.Unlock()
	defer djNoop2()
}

// litInherits defers a no-op in one arm, and calls a literal that calls another with the lock released.
//
// +checklocksignore
func litInherits(t *litGuarded, c, d bool) {
	t.mu.Lock()
	if c {
		defer djNoop()
	} else {
		litSink()
	}
	func(t *litGuarded) {
		func(t *litGuarded) { t.mu.Unlock() }(t)
		litSink()
		if d {
			t.mu.Lock()
		}
	}(t)
}

// litNoopSites calls a literal with a no-op defer once with a lock held and once under a relevant defer.
//
// +checklocksignore
func litNoopSites(t, u, w *litGuarded, c, d bool) {
	if c {
		t.mu.Lock()
	} else {
		u.mu.Lock()
		defer u.mu.Unlock()
	}
	func(w *litGuarded) {
		defer djNoop()
		if d {
			w.mu.Lock()
		}
	}(w)
	if c {
		t.mu.Unlock()
	}
}

// litRewalkPanic defers a literal that runs only at the panic, and calls a literal walked again.
//
// +checklocksignore
func litRewalkPanic(s, t *litGuarded, c bool) {
	defer func() { litSink() }()
	s.mu.Lock()
	func() {
		defer djNoop()
		if c {
			t.mu.Lock()
		}
	}()
	s.mu.Unlock()
	panic("x")
}
