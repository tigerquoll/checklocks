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
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// TestLockStateString pins the text of a lock state, which a map order would vary.
func TestLockStateString(t *testing.T) {
	keys := []string{
		"&({param:t}.mu)",
		"&({param:t}.inner.mu)",
		"&({global:Mu})",
		"&({*ssa.Alloc:(*T).m:t0}.mu)",
		"&({*ssa.Call:(*T).m:t4}.mu)",
		"&({*ssa.Phi:(*T).m:t9}.mu)",
		"&(&({param:t}.mus)[{*ssa.Const:(*T).m:k0}])",
		"&(&({param:t}.mus)[{*ssa.Const:(*T).m:k1}])",
		"*(&({param:t}.ptr.mu))",
		"&({freevar:fv}.mu)",
	}
	stateOf := func(order []string) string {
		ls := newLockState()
		for _, name := range order {
			// The mode is the name's, not the insertion order's.
			path := newLockPath(pathRootValue, nil, nil, name)
			ls.lockedMutexes[path.Key()] = lockInfo{exclusive: len(name)%2 == 0, path: path}
		}
		return ls.String()
	}
	reversed := slices.Clone(keys)
	slices.Reverse(reversed)
	forward, backward := stateOf(keys), stateOf(reversed)
	if forward != backward {
		t.Errorf("two insertion orders print differently:\n%s\n%s", forward, backward)
	}
	printed := strings.Split(forward, ",")
	if !slices.IsSorted(printed) {
		t.Errorf("the locks are not printed in order: %s", forward)
	}
	if len(printed) != len(keys) {
		t.Errorf("printed %d locks, want %d", len(printed), len(keys))
	}
}

// TestAddAliasAssumed pins that a merge keeps an entry acquired unless both were assumed.
func TestAddAliasAssumed(t *testing.T) {
	merge := func(left, right lockInfo) lockInfo {
		ls := newLockState()
		// Two zero-value values would resolve to one key, so each is named.
		l := makeResolvedValue(ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int]), nil)
		r := makeResolvedValue(ssa.NewConst(constant.MakeInt64(2), types.Typ[types.Int]), nil)
		leftPath := newLockPath(pathRootValue, l.value, nil, "left")
		rightPath := newLockPath(pathRootValue, r.value, nil, "right")
		ls.substitute(l.value, leftPath, nil)
		ls.substitute(r.value, rightPath, nil)
		left.path, right.path = leftPath, rightPath
		ls.lockedMutexes[leftPath.Key()] = left
		ls.lockedMutexes[rightPath.Key()] = right
		ls.addAlias(l, r)
		return ls.lockedMutexes[rightPath.Key()]
	}
	assumedExclusive := lockInfo{exclusive: true, assumed: true}
	acquiredShared := lockInfo{exclusive: false}
	sameMode := func(a, b lockInfo) bool { return a.exclusive == b.exclusive && a.assumed == b.assumed }
	if got := merge(assumedExclusive, acquiredShared); !sameMode(got, acquiredShared) {
		t.Errorf("an assumed left merged with an acquired right is %+v, want the acquired entry", got)
	}
	if got := merge(acquiredShared, assumedExclusive); !sameMode(got, acquiredShared) {
		t.Errorf("an acquired left merged with an assumed right is %+v, want the acquired entry", got)
	}
	if got := merge(assumedExclusive, assumedExclusive); !got.assumed {
		t.Error("two assumed entries merged to an acquired one")
	}
	if got := merge(lockInfo{exclusive: true}, acquiredShared); !got.exclusive || got.assumed {
		t.Errorf("two acquired entries merged to %+v, want exclusive acquired", got)
	}
}

// TestAddAliasMovesPath pins that a moved entry carries the path of its new key.
func TestAddAliasMovesPath(t *testing.T) {
	ls := newLockState()
	l := makeResolvedValue(ssa.NewConst(constant.MakeInt64(1), types.Typ[types.Int]), nil)
	r := makeResolvedValue(ssa.NewConst(constant.MakeInt64(2), types.Typ[types.Int]), nil)
	leftPath := newLockPath(pathRootValue, l.value, nil, "left")
	ls.substitute(l.value, leftPath, nil)
	rightPath := newLockPath(pathRootValue, r.value, nil, "right")
	ls.substitute(r.value, rightPath, nil)
	obj := types.NewVar(token.NoPos, nil, "mu", types.Typ[types.Int])
	ls.lockedMutexes[leftPath.Key()] = lockInfo{exclusive: true, object: obj, path: leftPath}
	ls.addAlias(l, r)
	info, ok := ls.lockedMutexes[rightPath.Key()]
	if !ok {
		t.Fatal("the entry was not moved to the right root")
	}
	if got := info.path.String(); got != "right" {
		t.Errorf("moved entry has path %q, want %q", got, "right")
	}
	if info.object != obj {
		t.Errorf("moved entry has object %v, want the left entry's", info.object)
	}
}

// twinLocks holds the two parameters named t of keySource's A and B, in two modes.
func twinLocks(t *testing.T) (a, b resolvedValue) {
	t.Helper()
	pkg := buildTestPackage(t, "a", keySource)
	return makeResolvedValue(pkg.Func("A").Params[0], nil), makeResolvedValue(pkg.Func("B").Params[0], nil)
}

// TestLockStateStringTwins pins that locks printing alike are both listed, in one order.
func TestLockStateStringTwins(t *testing.T) {
	a, b := twinLocks(t)
	ls := newLockState()
	ls.lockField(a, true)
	ls.lockField(b, false)
	const want = "{param:t} exclusively,{param:t} non-exclusively"
	if got := ls.String(); got != want {
		t.Errorf("state prints %q, want %q", got, want)
	}
	if first, second := ls.String(), ls.String(); first != second {
		t.Errorf("two calls print %q and %q", first, second)
	}
}

// TestIdentitySummaryTwins pins that states printing alike have different summaries.
func TestIdentitySummaryTwins(t *testing.T) {
	a, b := twinLocks(t)
	one, two := newLockState(), newLockState()
	one.lockField(a, true)
	two.lockField(b, true)
	all := func(lockInfo) bool { return true }
	if one.String() != two.String() {
		t.Fatalf("states print %q and %q, want alike", one.String(), two.String())
	}
	if one.identitySummary(all) == two.identitySummary(all) {
		t.Errorf("states of different locks share the summary %q", one.identitySummary(all))
	}
}

// TestLostClause pins the clause: the lost paths, held ones left out, the loops by line, the cap.
func TestLostClause(t *testing.T) {
	pc := &passContext{}
	nf := newNestFixture(t, pc)
	pc.pass = &analysis.Pass{Fset: nf.fn.Prog.Fset}
	in := "; lost track in an earlier pass of "
	outer, inner := nf.outer.String(), nf.inner.String()
	file := nf.fn.Prog.Fset.Position(nf.fn.Pos()).Filename
	for _, tc := range []struct {
		name string
		ls   *lockState
		want string
	}{
		{"known", newLockState(), ""},
		{"one key", markedIn(newLockState(), nf.outer, nf.rOuter), in + "the loop containing line 29: " + outer},
		{"held again", markedIn(held(false, nf.outer), nf.outer, nf.rOuter), ""},
		{"one held again", markedIn(markedIn(held(false, nf.outer), nf.outer, nf.rOuter), nf.inner, nf.rInner), in + "the loop containing line 32: " + inner},
		{"two loops", markedIn(markedIn(newLockState(), nf.inner, nf.rInner), nf.outer, nf.rOuter), in + "the loops containing lines 29, 32: " + min(outer, inner) + ", " + max(outer, inner)},
		{"two keys, one loop", markedIn(markedIn(newLockState(), nf.inner, nf.rInner), nf.outer, nf.rInner), in + "the loop containing line 32: " + min(outer, inner) + ", " + max(outer, inner)},
		{"top", toppedIn(newLockState(), nf.rSib), "; lost track of other locks in an earlier pass of the loop containing line 37"},
		{"top held", toppedIn(held(false, nf.outer), nf.rOuter, nf.rInner, nf.rSib), "; lost track of other locks in an earlier pass of the loops containing lines 29, 32, 37"},
		{"no region", markedIn(newLockState(), nf.outer), in + "a loop: " + outer},
	} {
		if got := pc.lostClause(tc.ls, nf.fn.Pos()); got != tc.want {
			t.Errorf("%s: clause %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := pc.loopsPhrase(regions(nf.rInner), file+"x"); !strings.HasPrefix(got, "the loop containing ") || strings.Contains(got, "line") || !strings.HasSuffix(got, ":32") {
		t.Errorf("a loop in another file reads %q, want its file and line", got)
	}
}

// TestLostClauseCap pins that more than maxClauseLoops loops are counted, not listed.
func TestLostClauseCap(t *testing.T) {
	src := "package p\n\nfunc get(i int) *int { return nil }\n\nfunc f(n int) {\n"
	for k := range 5 {
		src += fmt.Sprintf("\tfor i := 0; i < n; i++ {\n\t\t_ = get(%d)\n\t}\n", k)
	}
	src += "}\n"
	pkg := buildTestPackage(t, "cap", src)
	pc := &passContext{pass: &analysis.Pass{Fset: pkg.Prog.Fset}}
	var rs regionSet
	for _, r := range pc.cfgOf(pkg.Func("f")).region {
		rs = rs.with(r)
	}
	if len(rs) != 5 {
		t.Fatalf("%d loops, want 5", len(rs))
	}
	want := "the loops containing lines 6, 9, 12, and 2 more"
	if got := pc.loopsPhrase(rs, pkg.Prog.Fset.Position(pkg.Func("f").Pos()).Filename); got != want {
		t.Errorf("loops phrase %q, want %q", got, want)
	}
}

// TestLostClauseLineTie pins that the loop's line does not depend on map order when a //line
// directive gives a body line and the header the same number.
func TestLostClauseLineTie(t *testing.T) {
	src := "package p\n\nfunc get() int { return 0 }\nfunc f(n int) {\n\tfor i := 0; i < n; i++ {\n//line other.go:5\n\t\t_ = get()\n\t}\n}\n"
	pkg := buildTestPackage(t, "tie", src)
	pc := &passContext{pass: &analysis.Pass{Fset: pkg.Prog.Fset}}
	var rs regionSet
	for _, r := range pc.cfgOf(pkg.Func("f")).region {
		rs = rs.with(r)
	}
	file := pkg.Prog.Fset.Position(pkg.Func("f").Pos()).Filename
	for range 30 {
		if got := pc.loopsPhrase(rs, file); got != "the loop containing line 5" {
			t.Fatalf("loop phrase %q, want the header's file and line", got)
		}
	}
}

// TestFlushReportsLineTie pins that two positions the line directives map to one place do not split
// a report seen in a known and a lost state.
func TestFlushReportsLineTie(t *testing.T) {
	fset := token.NewFileSet()
	f := fset.AddFile("x.go", -1, 60)
	f.SetLines([]int{0, 20, 40})
	f.AddLineColumnInfo(20, "m.go", 5, 1)
	f.AddLineColumnInfo(40, "m.go", 5, 1)
	var got []string
	pass := &analysis.Pass{Fset: fset, Report: func(d analysis.Diagnostic) { got = append(got, d.Message) }}
	pc := &passContext{pass: pass, collect: true, expectations: newExpectations(pass, checkLocksAnnotations, true)}
	a, b := f.Pos(20), f.Pos(40)
	pc.pending = []pendingReport{{a, "x (locks: none)", ""}, {b, "x (locks: none)", ""}, {a, "x (locks: none)", "; lost"}}
	pc.flushReports()
	if want := []string{"x (locks: none)", "x (locks: none)"}; !slices.Equal(got, want) {
		t.Errorf("reports %q, want %q", got, want)
	}
}

// TestWithClause pins that the clause goes inside the closing parenthesis.
func TestWithClause(t *testing.T) {
	if got := withClause("x (locks: a)", "; y"); got != "x (locks: a; y)" {
		t.Errorf("clause reads %q", got)
	}
	if got := withClause("x (locks: a)", ""); got != "x (locks: a)" {
		t.Errorf("no clause reads %q", got)
	}
}

// TestFoldExit pins which locks a literal's exit leaves the caller holding.
func TestFoldExit(t *testing.T) {
	entry := func(name string, exclusive, assumed bool) (pathKey, lockInfo) {
		p := newLockPath(pathRootValue, nil, nil, name)
		return p.Key(), lockInfo{exclusive: exclusive, assumed: assumed, path: p}
	}
	pre, exit := newLockState(), newLockState()
	for _, e := range []struct {
		to                *lockState
		name              string
		exclusive, assume bool
	}{
		{pre, "kept", false, false}, {exit, "kept", true, false},
		{pre, "released", true, false},
		{pre, "assumed", true, true},
		{exit, "taken", true, false},
	} {
		k, info := entry(e.name, e.exclusive, e.assume)
		e.to.lockedMutexes[k] = info
	}
	pre.foldExit(exit, nil)
	if len(pre.lockedMutexes) != 2 {
		t.Fatalf("the fold holds %d locks, want 2: %s", len(pre.lockedMutexes), pre)
	}
	if k, _ := entry("kept", true, false); !pre.lockedMutexes[k].exclusive {
		t.Errorf("a lock the exit holds keeps the caller's mode: %s", pre)
	}
	if k, _ := entry("assumed", true, true); !pre.lockedMutexes[k].assumed {
		t.Errorf("an assumed lock is not kept: %s", pre)
	}
}

// TestRetaken pins that a lock released and one of the same object taken under another key is a retake.
func TestRetaken(t *testing.T) {
	obj := types.NewVar(token.NoPos, nil, "mu", types.Typ[types.Int])
	put := func(ls *lockState, name string, o types.Object) {
		p := newLockPath(pathRootValue, nil, nil, name)
		ls.lockedMutexes[p.Key()] = lockInfo{exclusive: true, object: o, path: p}
	}
	pre, exit := newLockState(), newLockState()
	put(pre, "a", obj)
	put(exit, "b", obj)
	if !retaken(pre, exit) {
		t.Error("a lock released and another of the same object taken is not a retake")
	}
	put(exit, "a", obj)
	if retaken(pre, exit) {
		t.Error("a lock still held is a retake")
	}
	exit = newLockState()
	put(exit, "b", types.NewVar(token.NoPos, nil, "other", types.Typ[types.Int]))
	if retaken(pre, exit) {
		t.Error("a lock of another object is a retake")
	}
}
