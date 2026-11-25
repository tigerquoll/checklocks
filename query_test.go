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
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
)

// The truth tables of the two queries. Every other kind of report is left out of the corpus cells:
//
//	guarded access via an atomic-only field, atomic write   read isHeld, so unknown reads as not held
//	illegal use of an atomic-only field                     no lock text, so the states merge
//	incompatible return states, return with unexpected      compare the held locks a return reached;
//	locks                                                   the marker is not in that text
//	already locked, attempt to acquire                      an acquire of an unknown lock inserts it,
//	                                                        so it never reports
//	must not hold                                           read isHeld: unknown is not held, as none is
//	goroutine started by go ... does not hold               report without reading the held locks
//	loop-entry and entry-state reports                      the entry state precedes any back edge
//
// A kind that reads isHeld sees unknown as not held: query is not its reader yet.

func TestQueryElementHeld(t *testing.T) {
	fx := newLoopFixture(t)
	pc := &passContext{}
	shared := held(false, fx.before)
	shared.lockedMutexes[fx.before.Key()] = lockInfo{path: fx.before}
	for _, tc := range []struct {
		name string
		ls   *lockState
		want lockAnswer
	}{
		{"exclusive", held(false, fx.before), lockAnswer{kind: ansHeld, exclusive: true}},
		{"shared", shared, lockAnswer{kind: ansHeld}},
		{"assumed", held(true, fx.before), lockAnswer{kind: ansHeld, exclusive: true, assumed: true}},
		{"other lock held", held(false, fx.inside), lockAnswer{kind: ansNotHeld}},
		{"nothing held", held(false), lockAnswer{kind: ansNotHeld}},
	} {
		if got := pc.query(tc.ls, fx.before); got != tc.want {
			t.Errorf("%s: query = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestQueryElementMarker(t *testing.T) {
	fx := newLoopFixture(t)
	pc := &passContext{}
	for _, tc := range []struct {
		name string
		ls   *lockState
		want answerKind
	}{
		{"marker names it", marked(held(false), fx.inside), ansUnknown},
		{"marker names another", marked(held(false), fx.before), ansNotHeld},
		{"held beats the marker", marked(held(false, fx.inside), fx.inside), ansHeld},
		{"another held, marker names it", marked(held(false, fx.before), fx.inside), ansUnknown},
	} {
		if got := pc.query(tc.ls, fx.inside).kind; got != tc.want {
			t.Errorf("%s: query = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestQueryElementTop(t *testing.T) {
	fx := newLoopFixture(t)
	pc := &passContext{}
	nf := newNestFixture(t, pc)
	for _, tc := range []struct {
		name string
		ls   *lockState
		path lockPath
		want answerKind
	}{
		{"value of a dropped region", toppedIn(held(false), nf.rInner), nf.inner, ansUnknown},
		{"index defined in a dropped region", toppedIn(held(false), nf.rInner), index(fx.param, nf.inner), ansUnknown},
		{"value of another region", toppedIn(held(false), nf.rInner), nf.sibling, ansNotHeld},
		{"value of the enclosing region", toppedIn(held(false), nf.rInner), nf.outer, ansNotHeld},
		{"parameter root", toppedIn(held(false), nf.rInner), fx.param, ansNotHeld},
		{"top over no region", topped(held(false)), nf.inner, ansNotHeld},
		{"held beats top", toppedIn(held(false, nf.inner), nf.rInner), nf.inner, ansHeld},
	} {
		if got := pc.query(tc.ls, tc.path).kind; got != tc.want {
			t.Errorf("%s: query = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestQueryElementTransfer checks a release of an unknown lock leaves it unknown, and an acquire makes it held.
func TestQueryElementTransfer(t *testing.T) {
	fx := newLoopFixture(t)
	pc := &passContext{}
	rv := makeResolvedValue(fx.inside.root.value.(ssa.Value), nil)
	ls := newLockState()
	p, _ := rv.valueAndObject(ls)
	ls.marker = dropped(p)
	if _, ok := ls.unlockField(rv, true); ok {
		t.Fatal("released a lock that was not held")
	}
	if got := pc.query(ls, p).kind; got != ansUnknown {
		t.Errorf("after a release of an unknown lock: %d, want unknown", got)
	}
	if _, ok := ls.lockField(rv, true); !ok {
		t.Fatal("did not acquire an unknown lock")
	}
	if got := pc.query(ls, p); got.kind != ansHeld || !got.exclusive {
		t.Errorf("after an acquire of an unknown lock: %+v, want held exclusively", got)
	}
}

// viewFixture has a function with a record site, a literal with none, and the locks of its tests.
type viewFixture struct {
	pkg           *ssa.Package
	site, literal ssa.Instruction
	a, b          lockPath
}

func newViewFixture(t *testing.T) viewFixture {
	t.Helper()
	pkg := buildTestPackage(t, "view", "package view\n\nfunc f() {}\n\nfunc g() { func() {}() }\n")
	return viewFixture{
		pkg:     pkg,
		site:    pkg.Func("f").Blocks[0].Instrs[0],
		literal: pkg.Func("g").AnonFuncs[0].Blocks[0].Instrs[0],
		a:       newLockPath(pathRootValue, nil, nil, "a"),
		b:       newLockPath(pathRootValue, nil, nil, "b"),
	}
}

// lockSet makes a state holding p, exclusive or shared, assumed or acquired.
func lockSet(p lockPath, exclusive, assumed bool) *lockState {
	ls := newLockState()
	ls.lockedMutexes[p.Key()] = lockInfo{path: p, exclusive: exclusive, assumed: assumed}
	return ls
}

func TestViewQueryAnswers(t *testing.T) {
	fx := newViewFixture(t)
	ka, kb := fx.a.Key(), fx.b.Key()
	for _, tc := range []struct {
		name   string
		states []*lockState
		key    pathKey
		want   viewAnswer
	}{
		{"every state holds it", []*lockState{lockSet(fx.a, true, false), lockSet(fx.a, true, false)}, ka, viewAnswer{kind: viewAll, exclusive: true}},
		{"one state holds it", []*lockState{lockSet(fx.a, true, false), newLockState()}, ka, viewAnswer{kind: viewSome, exclusive: true}},
		{"a later state holds it", []*lockState{newLockState(), lockSet(fx.a, true, false)}, ka, viewAnswer{kind: viewSome, exclusive: true}},
		{"no state holds it", []*lockState{lockSet(fx.a, true, false), newLockState()}, kb, viewAnswer{kind: viewNone}},
		{"an empty state is a record", []*lockState{newLockState()}, ka, viewAnswer{kind: viewNone}},
		{"assumed only if every state assumes", []*lockState{lockSet(fx.a, true, true), lockSet(fx.a, true, false)}, ka, viewAnswer{kind: viewAll, exclusive: true}},
		{"assumed in every state", []*lockState{lockSet(fx.a, true, true), lockSet(fx.a, true, true)}, ka, viewAnswer{kind: viewAll, exclusive: true, assumed: true}},
		{"exclusive on some paths only", []*lockState{lockSet(fx.a, true, false), lockSet(fx.a, false, false)}, ka, viewAnswer{kind: viewAll}},
		{"shared first", []*lockState{lockSet(fx.a, false, false), lockSet(fx.a, true, false)}, ka, viewAnswer{kind: viewAll}},
		{"exclusive on the paths that hold it", []*lockState{lockSet(fx.a, true, false), newLockState()}, ka, viewAnswer{kind: viewSome, exclusive: true}},
		{"marker names it", []*lockState{marked(newLockState(), fx.a)}, ka, viewAnswer{}},
		{"marker names another", []*lockState{marked(newLockState(), fx.b)}, ka, viewAnswer{kind: viewNone}},
		{"one state's marker names it", []*lockState{newLockState(), marked(newLockState(), fx.a)}, ka, viewAnswer{}},
		{"held in one, named in another", []*lockState{lockSet(fx.a, true, false), marked(newLockState(), fx.a)}, ka, viewAnswer{kind: viewSome, exclusive: true}},
		{"a top state", []*lockState{newLockState(), topped(newLockState())}, ka, viewAnswer{}},
		{"held where a state is top", []*lockState{lockSet(fx.a, true, false), topped(lockSet(fx.a, true, false))}, ka, viewAnswer{kind: viewAll, exclusive: true}},
	} {
		var e engineView
		for _, ls := range tc.states {
			e.add(fx.site, ls, nil)
		}
		if got := e.query(fx.site, tc.key); got != tc.want {
			t.Errorf("%s: query = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestViewQueryIdempotent checks adding one state again changes nothing, whatever the number of adds.
func TestViewQueryIdempotent(t *testing.T) {
	fx := newViewFixture(t)
	var e engineView
	for range 3 {
		e.add(fx.site, lockSet(fx.a, true, false), nil)
	}
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewAll {
		t.Errorf("one state added three times: %+v, want all", got)
	}
	for range 3 {
		e.add(fx.site, newLockState(), nil)
	}
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewSome {
		t.Errorf("then a state without it three times: %+v, want some", got)
	}
	for range 3 {
		e.add(fx.site, lockSet(fx.a, true, false), nil)
	}
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewSome {
		t.Errorf("then the first state again: %+v, want some", got)
	}
}

func TestViewQueryNoAnswer(t *testing.T) {
	fx := newViewFixture(t)
	f := fx.pkg.Func("f")
	var e engineView
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewUnknown {
		t.Errorf("no record: %+v, want unknown", got)
	}
	if got := e.query(fx.literal, fx.a.Key()); got.kind != viewUnknown {
		t.Errorf("a literal has no record: %+v, want unknown", got)
	}
	var none *engineView
	if got := none.query(fx.site, fx.a.Key()); got.kind != viewUnknown {
		t.Errorf("nil view: %+v, want unknown", got)
	}
	e.add(fx.site, lockSet(fx.a, true, false), nil)
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewAll {
		t.Fatalf("recorded: %+v, want all", got)
	}
	e.giveUp(f, "test")
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewUnknown {
		t.Errorf("function given up on: %+v, want unknown", got)
	}
}

func TestViewQueryInexact(t *testing.T) {
	fx := newViewFixture(t)
	var e engineView
	if !e.markInexact(fx.site) {
		t.Errorf("first mark: not new")
	}
	e.add(fx.site, lockSet(fx.a, true, false), nil)
	if got := e.query(fx.site, fx.a.Key()); got.kind != viewUnknown {
		t.Errorf("a mark before any record: %+v, want unknown", got)
	}
	if e.markInexact(fx.site) {
		t.Errorf("second mark: new")
	}
	for _, k := range []pathKey{fx.a.Key(), fx.b.Key()} {
		if got := e.query(fx.site, k); got.kind != viewUnknown {
			t.Errorf("inexact site, key %s: %+v, want unknown", k, got)
		}
	}
}

func litSource() string {
	var loop strings.Builder
	for _, f := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		loop.WriteString("\t\t\tif c() {\n\t\t\t\tl." + f + ".Lock()\n\t\t\t}\n")
	}
	return strings.ReplaceAll(`package p

type Mutex struct{}

func (*Mutex) Lock()   {}
func (*Mutex) Unlock() {}

type Locks struct {
	a, b, c, d, e, f, g Mutex
}

func sink()         {}
func other()        {}
func keep(f func()) {}

func twoStates(m *Mutex, c bool) {
	f := func() { sink() }
	if c {
		m.Lock()
		f()
		m.Unlock()
	} else {
		f()
	}
}

func bothHeld(m *Mutex) {
	f := func() { sink() }
	m.Lock()
	f()
	f()
	m.Unlock()
}

func escapeFree(m *Mutex) {
	f := func(*Mutex) { sink() }
	m.Lock()
	f(m)
	m.Unlock()
	go f(m)
}

func escapeClosure(m *Mutex) {
	f := func() { sink(); println(m != nil) }
	m.Lock()
	f()
	m.Unlock()
	go f()
}

func nested(m *Mutex) {
	outer := func() {
		inner := func() { sink(); println(m != nil) }
		inner()
	}
	m.Lock()
	outer()
	m.Unlock()
	keep(outer)
}

func phantom(m *Mutex) {
	defer func() { sink() }()
	m.Lock()
	func() { defer other() }()
	m.Unlock()
}

func phantomPanic(m *Mutex) {
	defer func() { sink() }()
	m.Lock()
	func() { defer other() }()
	panic("x")
}

func nestGiveUp(l *Locks, c func() bool) {
	outer := func() {
		inner := func() { sink() }
		for c() {
@LOOP@		}
		inner()
	}
	l.a.Lock()
	l.b.Lock()
	outer()
	l.b.Unlock()
	l.a.Unlock()
	outer()
}

func gaveUp(m *Mutex, l *Locks, c func() bool) {
	m.Lock()
	func() {
		for c() {
@LOOP@		}
		sink()
	}()
	sink()
	m.Unlock()
}
`, "@LOOP@", loop.String())
}

// litAnswers walks fn of src and returns it, the view, and a renderer of the answers at a function's sink calls.
func litAnswers(t *testing.T, src, fn string) (top *ssa.Function, e *engineView, answers func(*ssa.Function) string) {
	t.Helper()
	pkg := buildTestPackage(t, "lit", src)
	pass := &analysis.Pass{
		Fset:             pkg.Prog.Fset,
		Report:           func(analysis.Diagnostic) {},
		ImportObjectFact: func(types.Object, analysis.Fact) bool { return false },
	}
	pc := &passContext{
		expectations: newExpectations(pass, checkLocksAnnotations, true),
		pass:         pass,
		collect:      true,
		functions:    make(map[*ssa.Function]struct{}),
		engine:       &engineView{},
	}
	top = pkg.Func(fn)
	pc.checkFunction(callSite{kind: siteEntry}, nil, top, &lockFunctionFacts{}, nil, false)
	names := map[viewKind]string{viewUnknown: "unknown", viewAll: "all", viewSome: "some", viewNone: "none"}
	answers = func(f *ssa.Function) string {
		var out []string
		for _, b := range f.Blocks {
			for _, inst := range b.Instrs {
				c, ok := inst.(*ssa.Call)
				if !ok || c.Call.Value.Name() != "sink" {
					continue
				}
				locks, ok := pc.engine.heldAt(inst)
				if !ok {
					out = append(out, "-")
					continue
				}
				var kinds []string
				for _, l := range locks {
					kinds = append(kinds, names[pc.engine.query(inst, l.path.Key()).kind])
				}
				out = append(out, "["+strings.Join(kinds, ",")+"]")
			}
		}
		return strings.Join(out, " ")
	}
	return top, pc.engine, answers
}

// TestViewLiteralSites checks a literal only called directly is answered over its walks, any other has no record.
func TestViewLiteralSites(t *testing.T) {
	src := litSource()
	for _, tc := range []struct {
		fn, inFn, inLit, why string
	}{
		{"twoStates", "", "[some]", ""},
		{"bothHeld", "", "[all]", ""},
		{"escapeFree", "", "-", "not called directly"},
		{"escapeClosure", "", "-", "not called directly"},
		{"phantom", "", "-", "not called directly"},
		{"phantomPanic", "", "-", "not called directly"},
		{"gaveUp", "[all]", "-", "gave up: more than 64 lock states reach one block in a loop"},
	} {
		top, e, answers := litAnswers(t, src, tc.fn)
		lit := top.AnonFuncs[0]
		if inFn, inLit := answers(top), answers(lit); inFn != tc.inFn || inLit != tc.inLit {
			t.Errorf("%s: sink answers in the function %q and in its literal %q, want %q and %q", tc.fn, inFn, inLit, tc.inFn, tc.inLit)
		}
		if got := e.whyNone(lit); tc.inLit == "-" && got != tc.why {
			t.Errorf("%s: whyNone = %q, want %q", tc.fn, got, tc.why)
		}
		if got := (&engineView{}).whyNone(lit); tc.fn == "twoStates" && got != "not reached" {
			t.Errorf("a literal no walk reached: whyNone = %q, want not reached", got)
		}
	}
	top, e, answers := litAnswers(t, src, "nested")
	inner := top.AnonFuncs[0].AnonFuncs[0]
	if got, why := answers(inner), e.whyNone(inner); got != "-" || why != "not called directly" {
		t.Errorf("nested: inner literal answers %q, whyNone %q, want no record because the literal around it escapes", got, why)
	}
	top, e, answers = litAnswers(t, src, "nestGiveUp")
	outer := top.AnonFuncs[0]
	inner = outer.AnonFuncs[0]
	if got, why := answers(inner), e.whyNone(inner); got != "-" || !strings.HasPrefix(why, "gave up: ") {
		t.Errorf("nestGiveUp: inner literal answers %q, whyNone %q, want no record because the literal around it gave up", got, why)
	}
}

// unwindFixture is the replay of one function's defers, each recorded as a site of an engine.
type unwindFixture struct {
	pc    *passContext
	fn    *ssa.Function
	sites []*ssa.Defer
	wc    *walkContext
}

// newUnwindFixture builds src; release is annotated as releasing its first argument, so a deferred call of it
// fails when the lock is not held.
func newUnwindFixture(t *testing.T, src, fn string) unwindFixture {
	t.Helper()
	pkg := buildTestPackage(t, "unwind", src)
	pc := deferPC(pkg.Prog.Fset)
	inner := pc.pass.ImportObjectFact
	pc.pass.ImportObjectFact = func(o types.Object, fact analysis.Fact) bool {
		if f, ok := fact.(*lockFunctionFacts); ok && o.Name() == "release" {
			f.HeldOnEntry = map[string]functionGuardInfo{"m": {Resolver: &parameterGuard{Index: 0}, Exclusive: true}}
			return true
		}
		return inner(o, fact)
	}
	pc.pass.Report = func(analysis.Diagnostic) {}
	f := pkg.Func(fn)
	wc := &walkContext{fn: f, lff: &lockFunctionFacts{}}
	pc.engine = &engineView{}
	pc.emit = emitReplay
	pc.records = []*blockRecord{{wc: wc, parent: newLockState()}}
	pc.current = pc.records[0]
	return unwindFixture{pc: pc, fn: f, sites: deferSites(f), wc: wc}
}

// run replays the unwind of the stack push builds, from a state holding the first parameter's lock if the
// function has one, and returns that lock's key.
func (u unwindFixture) run(push func(ls *lockState, sites []*ssa.Defer)) pathKey {
	ls := newLockState()
	var key pathKey
	if len(u.fn.Params) > 0 {
		p, _ := ls.lockField(makeResolvedValue(u.fn.Params[0], nil), true)
		key = p.Key()
	}
	push(ls, u.sites)
	u.pc.runDefers(u.wc, ls)
	return key
}

// pushInOrder pushes the first site as a no-op and the rest as relevant defers.
func pushInOrder(ls *lockState, sites []*ssa.Defer) {
	for i, d := range sites {
		ls.pushDefer(d, i > 0)
	}
}

// answers renders the answer kinds at every site, in source order.
func (u unwindFixture) answers(k pathKey) string {
	names := map[viewKind]string{viewUnknown: "unknown", viewAll: "all", viewSome: "some", viewNone: "none"}
	out := make([]string, len(u.sites))
	for i, d := range u.sites {
		out[i] = names[u.pc.engine.query(d, k).kind]
	}
	return strings.Join(out, ",")
}

func unwindSource() string {
	var b strings.Builder
	b.WriteString(`package p

type Mutex struct{}

func release(m *Mutex) {}
func noop()            {}
func other()           {}

func once(m *Mutex) {
	defer noop()
	defer release(m)
}

func twice(m *Mutex) {
	defer noop()
	defer release(m)
	defer release(m)
}

func literal(m *Mutex) {
	defer noop()
	defer func() { release(m) }()
}

func loop(m *Mutex, n int) {
	defer noop()
	for i := 0; i < n; i++ {
		defer release(m)
	}
}

func many() {
	defer noop()
`)
	for range maxEntries + 2 {
		b.WriteString("\tdefer other()\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// TestViewQueryUnwind checks which unwinds make their sites unknown: a failed relevant call, a relevant literal,
// a relevant defer in a cycle, and a may entry; and that an exact unwind makes none. A silenced failure is pinned by TestSiteDump.
func TestViewQueryUnwind(t *testing.T) {
	src := unwindSource()
	u := newUnwindFixture(t, src, "once")
	key := u.run(pushInOrder)
	if got, want := u.answers(key), "none,all"; got != want {
		t.Errorf("exact unwind: answers %s, want %s", got, want)
	}
	u = newUnwindFixture(t, src, "twice")
	key = u.run(pushInOrder)
	if got, want := u.answers(key), "unknown,unknown,unknown"; got != want {
		t.Errorf("failed release: answers %s, want %s", got, want)
	}
	u = newUnwindFixture(t, src, "literal")
	key = u.run(pushInOrder)
	if got, want := u.answers(key), "unknown,unknown"; got != want {
		t.Errorf("relevant literal: answers %s, want %s", got, want)
	}
	u = newUnwindFixture(t, src, "loop")
	key = u.run(pushInOrder)
	if got, want := u.answers(key), "unknown,unknown"; got != want {
		t.Errorf("relevant defer in a loop: answers %s, want %s", got, want)
	}
	u = newUnwindFixture(t, src, "many")
	key = u.run(func(ls *lockState, sites []*ssa.Defer) {
		for _, d := range sites[1:] {
			ls.pushDefer(sites[0], false)
			ls.pushDefer(d, true)
		}
		ls.pushDefer(sites[0], false)
	})
	want := "unknown" + strings.Repeat(",none", maxEntries+2)
	if got := u.answers("k"); got != want {
		t.Errorf("may entry: answers %s, want %s", got, want)
	}
}

// TestOpFailedSkippedEffects checks a deferred call whose release field cannot be resolved, or whose acquire
// goes through a discarded result, counts as a failed lock operation.
func TestOpFailedSkippedEffects(t *testing.T) {
	u := newUnwindFixture(t, unwindSource(), "once")
	guard := map[string]functionGuardInfo{"m": {Resolver: &returnGuard{}}}
	for _, tc := range []struct {
		name string
		lff  lockFunctionFacts
	}{
		{"release field unresolved", lockFunctionFacts{HeldOnEntry: guard}},
		{"acquire through a discarded result", lockFunctionFacts{HeldOnExit: guard}},
	} {
		u.pc.opFailed = false
		u.pc.postFunctionCallUpdate(newCallSite(u.sites[1]), &tc.lff, newLockState(), "")
		if !u.pc.opFailed {
			t.Errorf("%s: opFailed not set", tc.name)
		}
	}
}
