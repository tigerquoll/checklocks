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

func testClosureInvalid(tc *oneGuardStruct) {
	// This is expected to fail.
	callClosure(func() {
		tc.guardedField = 1 // +checklocksfail
	})
}

func testClosureUnsupported(tc *oneGuardStruct) {
	// Locked outside the closure, so may or may not be valid. This cannot
	// be handled and we should explicitly fail. This can't be handled
	// because of the call through callClosure, below, which means the
	// closure will actually be passed as a value somewhere.
	tc.mu.Lock()
	callClosure(func() {
		tc.guardedField = 1 // +checklocksfail
	})
	tc.mu.Unlock()
}

func testClosureValid(tc *oneGuardStruct) {
	// All locking happens within the closure. This should not present a
	// problem for analysis.
	callClosure(func() {
		tc.mu.Lock()
		tc.guardedField = 1
		tc.mu.Unlock()
	})
}

func testClosureInline(tc *oneGuardStruct) {
	// If the closure is being dispatching inline only, then we should be
	// able to analyze this call and give it a thumbs up.
	tc.mu.Lock()
	func() {
		tc.guardedField = 1
	}()
	tc.mu.Unlock()
}

func testClosureParamOther(t, u *oneGuardStruct, n int) {
	t.mu.Lock()
	func(t *oneGuardStruct) {
		t.guardedField = n // +checklocksfail={param:u}
	}(u)
	t.mu.Unlock()
}

func testClosureParamPairing(t, u *oneGuardStruct, n int) {
	t.mu.Lock()
	func(a, b *oneGuardStruct) {
		b.guardedField = n // +checklocksfail={param:u}
	}(t, u)
	t.mu.Unlock()
}

func testClosureParamSame(t *oneGuardStruct, n int) {
	t.mu.Lock()
	func(x *oneGuardStruct) {
		x.guardedField = n
	}(t)
	t.mu.Unlock()
}

func testClosureParamStore(a *oneGuardStruct, n int) {
	a.mu.Lock()
	func(p *oneGuardStruct) {
		_ = n
		*p = oneGuardStruct{}
		p.guardedField = 1
	}(a)
	a.mu.Unlock()
}

func testParamSelfStore(s **oneGuardStruct) {
	t := s
	*s = *t
	(*s).mu.Lock()
	(*s).guardedField = 1
	(*s).mu.Unlock()
}

func testAnonymousParamRenamed(t *oneGuardStruct) {
	t.mu.Lock()
	func(x *oneGuardStruct) {
		x.guardedField = 1
	}(t)
	t.mu.Unlock()
}

func testClosureParamDefer(t *oneGuardStruct, n int) {
	t.mu.Lock()
	defer func(x *oneGuardStruct) {
		_ = n
		x.mu.Unlock()
	}(t)
	// A deferred literal's unlock does not reach the caller.
} // +checklocksfail=return with unexpected locks held

// +checklocksignore
func testClosureIgnore(tc *oneGuardStruct) {
	// Inherit the checklocksignore.
	x := func() {
		tc.guardedField = 1
	}
	x()
}

func testAnonymousInvalid(tc *oneGuardStruct) {
	// Invalid, as per testClosureInvalid above.
	callAnonymous(func(tc *oneGuardStruct) {
		tc.guardedField = 1 // +checklocksfail
	}, tc)
}

func testAnonymousUnsupported(tc *oneGuardStruct) {
	// Not supportable, as per testClosureUnsupported above.
	tc.mu.Lock()
	callAnonymous(func(tc *oneGuardStruct) {
		tc.guardedField = 1 // +checklocksfail
	}, tc)
	tc.mu.Unlock()
}

func testAnonymousValid(tc *oneGuardStruct) {
	// Valid, as per testClosureValid above.
	callAnonymous(func(tc *oneGuardStruct) {
		tc.mu.Lock()
		tc.guardedField = 1
		tc.mu.Unlock()
	}, tc)
}

func testAnonymousInline(tc *oneGuardStruct) {
	// Unlike the closure case, we are able to dynamically infer the set of
	// preconditions for the function dispatch and assert that this is
	// a valid call.
	tc.mu.Lock()
	func(tc *oneGuardStruct) {
		tc.guardedField = 1
	}(tc)
	tc.mu.Unlock()
}

// +checklocksignore
func testAnonymousIgnore(tc *oneGuardStruct) {
	// Inherit the checklocksignore.
	x := func(tc *oneGuardStruct) {
		tc.guardedField = 1
	}
	x(tc)
}

//go:noinline
func callClosure(fn func()) {
	fn()
}

//go:noinline
func callAnonymous(fn func(*oneGuardStruct), tc *oneGuardStruct) {
	fn(tc)
}
