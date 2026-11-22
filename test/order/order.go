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

// Package order is the corpus of the walk order test: each function is one whose report or decision
// log may move with the order the walk visits successors in.
package order

import (
	"os"
	"sync"
)

type T struct {
	mu sync.Mutex
	// +checklocks:mu
	g int
	p *T
}

func nop()       {}
func cond() bool { return len(os.Args) > 2 }

// P5a: unlock deferred on one branch only. Truth: returns locked when !c.
func memoDefersFirstGood(t *T, c bool) {
	t.mu.Lock()
	if c {
		defer t.mu.Unlock()
	}
	t.g = 1
	return
}

// P5b: same, deferring branch walked second.
func memoDefersFirstBad(t *T, c bool) {
	t.mu.Lock()
	if c {
		nop()
	} else {
		defer t.mu.Unlock()
	}
	t.g = 1
	return // +checklocksfail=return with unexpected locks held
}

func sink(pp **T) {}

// P4a: p is a on the branch walked first. Truth: p may be b.
func memoStoredFirstGood(a, b *T, c bool) {
	p := b
	if c {
		p = a
	}
	sink(&p)
	a.mu.Lock()
	p.g = 1
	a.mu.Unlock()
}

// P4b: branch sense swapped.
func memoStoredFirstBad(a, b *T, c bool) {
	p := a
	if c {
		p = b
	}
	sink(&p)
	a.mu.Lock()
	p.g = 1 // +checklocksfail=invalid field access
	a.mu.Unlock()
}

// b3/b4: retarget through a pointer to the local.
func retargetThen(a, b *T, c bool) {
	p := a
	pp := &p
	p.mu.Lock()
	if c {
		*pp = b
	} else {
		nop()
	}
	p.g = 1 // +checklocksfail=invalid field access
	a.mu.Unlock()
	_ = pp
}

func retargetElse(a, b *T, c bool) {
	p := a
	pp := &p
	p.mu.Lock()
	if c {
		nop()
	} else {
		*pp = b
	}
	p.g = 1
	a.mu.Unlock()
	_ = pp
}

// nonMono: the Fable review's non-monotone loop (must terminate).
func nonMono(a, b *T) {
	mu := &a.mu
	g := func() { _ = mu }
	for cond() {
		mu.Lock()
		mu = &b.mu
		a.mu.Unlock()
	}
	g()
}

// closeDefers: five conditional irrelevant defers (2^5 defer lists) before a lock loop.
func closeDefers(t *T, f1, f2, f3, f4, f5 *os.File) {
	if cond() {
		defer f1.Close()
	}
	if cond() {
		defer f2.Close()
	}
	if cond() {
		defer f3.Close()
	}
	if cond() {
		defer f4.Close()
	}
	if cond() {
		defer f5.Close()
	}
	t.mu.Lock()
	t.g = 1
	t.mu.Unlock()
}

// lisafsLike: a captured cursor advanced each iteration, locked through closures.
func lisafsLike(d *T, n int) {
	start := d
	cur := start
	lock := func() {
		cur.mu.Lock()
	} // +checklocksfail=return with unexpected locks held|return with unexpected locks held|return with unexpected locks held
	unlock := func() {
		cur.mu.Unlock() // +checklocksfail=already unlocked or locked differently
	}
	for i := 0; i < n; i++ {
		lock()
		child := cur.p // +checklocksforce
		cur.g = 1      // +checklocksforce
		unlock()
		cur = child
	}
}
