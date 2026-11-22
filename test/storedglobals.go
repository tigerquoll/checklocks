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

package test

// Probes for the slots a function's walk keeps apart: a global is a slot only where a lock
// resolution loads it, and an alias-typed value passed to a literal is a root.

var sg0, sg1, sg2, sg3, sg4, sg5, sg6, sg7, sg8, sg9, sg10, sg11, sg12, sg13, sg14, sg15, sg16, sg17, sg18, sg19 int

func sgLockFree20(c []bool) {
	if c[0] {
		sg0 = 1
	}
	if c[1] {
		sg1 = 1
	}
	if c[2] {
		sg2 = 1
	}
	if c[3] {
		sg3 = 1
	}
	if c[4] {
		sg4 = 1
	}
	if c[5] {
		sg5 = 1
	}
	if c[6] {
		sg6 = 1
	}
	if c[7] {
		sg7 = 1
	}
	if c[8] {
		sg8 = 1
	}
	if c[9] {
		sg9 = 1
	}
	if c[10] {
		sg10 = 1
	}
	if c[11] {
		sg11 = 1
	}
	if c[12] {
		sg12 = 1
	}
	if c[13] {
		sg13 = 1
	}
	if c[14] {
		sg14 = 1
	}
	if c[15] {
		sg15 = 1
	}
	if c[16] {
		sg16 = 1
	}
	if c[17] {
		sg17 = 1
	}
	if c[18] {
		sg18 = 1
	}
	if c[19] {
		sg19 = 1
	}
}

var sh0, sh1, sh2, sh3, sh4 int

func sgLockFreeLoop(n int, c []bool) {
	for i := 0; i < n; i++ {
		if c[0] {
			sh0 = 1
		}
		if c[1] {
			sh1 = 1
		}
		if c[2] {
			sh2 = 1
		}
		if c[3] {
			sh3 = 1
		}
		if c[4] {
			sh4 = 1
		}
	}
}

var sgCur, sgAlt *stT

// sgGlobalToGlobal: a literal stores one global into another it loads.
func sgGlobalToGlobal() {
	sgCur = stOther
	func(t *stT) {
		t.mu.Lock()
		if stCond() {
			sgAlt = new(stT)
		}
		sgCur = sgAlt
		t.mu.Unlock() // +checklocksfail=already unlocked or locked differently|already unlocked or locked differently
	}(sgCur) // +checklocksfail=return with unexpected locks held
}

// sgParentStores: the parent stores a global the literal loads.
func sgParentStores(x *stT, c bool) {
	if c {
		sgAlt = x
	}
	sgCur = sgAlt
	func() {
		sgCur.mu.Lock()
		sgCur.mu.Unlock()
	}()
}

// sgAddressTakenLocal: an address-taken local stored into a global the literal loads.
func sgAddressTakenLocal(x *stT, c bool) {
	var al *stT
	stSink(&al)
	if c {
		al = x
	}
	sgCur = al
	func() {
		sgCur.mu.Lock()
		sgCur.mu.Unlock()
	}()
}

var sgAliasCur *stAliased

// sgAliasGlobal: a global alias-typed argument of a literal.
func sgAliasGlobal(a, b *stAliased, c bool) {
	sgAliasCur = b
	if c {
		sgAliasCur = a
	}
	func(x *stAliased) { _ = x }(sgAliasCur)
}

// sgAliasParam: a reassigned slot passed as an alias-typed literal argument.
func sgAliasParam(a, b *stAliased, c bool) {
	p := b
	if c {
		p = a
	}
	stSink(&p)
	func(x *stAliased) { _ = x }(p)
}

// sgAliasFreeVar: a reassigned slot captured by a literal.
func sgAliasFreeVar(a, b *stAliased, c bool) {
	p := b
	if c {
		p = a
	}
	func() { _ = p }()
}

// sgAliasClosureArg: a closure with an argument and a captured variable.
func sgAliasClosureArg(a, b *stAliased, c bool) {
	p := b
	if c {
		p = a
	}
	stSink(&p)
	q := 0
	func(x *stAliased) { _ = x; q++ }(p)
}

// sgGoAliasArg: an alias-typed argument of an annotated go literal.
func sgGoAliasArg(t *stT, a, b *stAliased, c bool) {
	p := b
	if c {
		p = a
	}
	stSink(&p)
	t.mu.Lock()
	// +checklocks:u.mu
	go func(u *stT, x *stAliased) {}(t, p) // +checklocksfail=goroutine started by go func literal does not hold u.mu exclusively
	t.mu.Unlock()
}
