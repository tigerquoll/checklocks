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

package checklocks

import (
	"slices"
	"testing"

	"golang.org/x/tools/go/ssa"
)

type effect int

const (
	releaseOwn effect = iota // a lock no other defer touches
	releaseA
	releaseB
	acquireA
	acquireB
)

// TestDeferWordOracle compares each no-op's held set, over every push word and prefix, with a true unbounded stack.
func TestDeferWordOracle(t *testing.T) {
	const noops, rels, exactLen, anyLen = 2, 3, 8, 6
	syms := make([]*ssa.Defer, noops+rels)
	for i := range syms {
		syms[i] = &ssa.Defer{}
	}
	relevant := func(d *ssa.Defer) bool { return slices.Index(syms, d) >= noops }
	own := func(sym int) pathKey { return pathKey(rune('a' + sym)) }

	// apply runs effect e on held and reports whether the call was valid: not a release of an unheld lock or an acquire of a held one.
	apply := func(held map[pathKey]lockInfo, e effect, sym int) bool {
		key := own(sym)
		switch e {
		case releaseA, acquireA:
			key = "A"
		case releaseB, acquireB:
			key = "B"
		}
		_, was := held[key]
		if e == acquireA || e == acquireB {
			held[key] = lockInfo{}
			return !was
		}
		delete(held, key)
		return was
	}
	entry := func(e []effect) map[pathKey]lockInfo {
		held := map[pathKey]lockInfo{"A": {}}
		if e[0] == releaseOwn {
			held = map[pathKey]lockInfo{}
			for s := noops; s < noops+rels; s++ {
				held[own(s)] = lockInfo{}
			}
		}
		return held
	}
	union := func(into map[int]map[pathKey]bool, sym int, held map[pathKey]lockInfo) {
		if into[sym] == nil {
			into[sym] = map[pathKey]bool{}
		}
		for k := range held {
			into[sym][k] = true
		}
	}

	var word []int
	var words, unwinds, failedWords, failedLost, failedExtra int
	check := func(ls *lockState, e []effect) {
		real := map[int]map[pathKey]bool{}
		held := entry(e)
		for p := len(word) - 1; p >= 0; p-- {
			if s := word[p]; s >= noops {
				apply(held, e[s-noops], s)
			} else {
				union(real, s, held)
			}
		}
		got := map[int]map[pathKey]bool{}
		failed := false
		c := ls.fork()
		c.lockedMutexes = entry(e)
		unwindDefers(c, func(d *ssa.Defer, at *lockState) {
			if s := slices.Index(syms, d); s >= noops {
				at.modify()
				failed = !apply(at.lockedMutexes, e[s-noops], s) || failed
			} else {
				union(got, s, at.lockedMutexes)
			}
		})
		unwinds++
		for _, s := range word {
			if s < noops && (got[s] == nil || !slices.ContainsFunc(ls.noops, func(x deferEntry) bool { return x.d == syms[s] })) {
				t.Fatalf("word %v effects %v: no-op %d has no entry or never ran", word, e, s)
			}
		}
		var lost, extra pathKey
		for s := range real {
			for k := range real[s] {
				if !got[s][k] {
					lost = k
				}
			}
			for k := range got[s] {
				if !real[s][k] {
					extra = k
				}
			}
		}
		switch {
		case failed:
			failedWords++
			if lost != "" {
				failedLost++
			}
			if extra != "" {
				failedExtra++
			}
		case lost != "" || extra != "":
			t.Fatalf("word %v effects %v: lost %q extra %q: got %v, real %v", word, e, lost, extra, got, real)
		}
	}
	effects := make([]effect, rels)
	var assign func(ls *lockState, r int)
	assign = func(ls *lockState, r int) {
		if r == rels {
			check(ls, effects)
			return
		}
		for e := releaseA; e <= acquireB; e++ {
			effects[r] = e
			assign(ls, r+1)
		}
	}
	var walk func(ls *lockState)
	walk = func(ls *lockState) {
		words++
		check(ls, []effect{releaseOwn, releaseOwn, releaseOwn})
		if len(word) <= anyLen {
			assign(ls, 0)
		}
		if len(word) == exactLen {
			return
		}
		for s := range syms {
			next := ls.fork()
			next.pushDefer(syms[s], relevant(syms[s]))
			word = append(word, s)
			walk(next)
			word = word[:len(word)-1]
		}
	}
	walk(newLockState())
	t.Logf("%d words, %d unwinds; %d unwinds with a failed lock call (lost on %d, extra on %d); the rest are exact", words, unwinds, failedWords, failedLost, failedExtra)
}
