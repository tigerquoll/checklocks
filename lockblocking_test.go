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
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// TestK8sRequest pins the rule that separates a call to the API server from getting hold of
// a client. It is a unit test because the corpus cannot exercise it: the kubernetes client
// is not a dependency of this module and must not become one to test a name match.
func TestK8sRequest(t *testing.T) {
	const typed = "k8s.io/client-go/kubernetes/typed/core/v1"
	tests := []struct {
		pkgPath, name string
		want          bool
	}{
		// The verbs of a generated client.
		{typed, "Get", true},
		{typed, "Update", true},
		{typed, "List", true},
		{typed, "Watch", true},
		{typed, "DeleteCollection", true},
		// The rest client underneath them.
		{"k8s.io/client-go/rest", "Do", true},
		// Getting hold of a client does no I/O, and reporting it would name the
		// wrong line as well as the wrong function.
		{"k8s.io/client-go/kubernetes", "CoreV1", false},
		{typed, "Secrets", false},
		// A lister reads the informer's local store rather than the API
		// server, so it is not a round trip however it is named.
		{"k8s.io/client-go/listers/core/v1", "Get", false},
		{"k8s.io/client-go/listers/core/v1", "List", false},
		{"k8s.io/client-go/tools/cache", "Get", false},
		// The same names elsewhere are ordinary functions.
		{"net/url", "Get", false},
		{"github.com/example/app", "Update", false},
		{"", "Get", false},
	}
	for _, tc := range tests {
		if got := isK8sRequest(tc.pkgPath, tc.name); got != tc.want {
			t.Errorf("isK8sRequest(%q, %q) = %v, want %v", tc.pkgPath, tc.name, got, tc.want)
		}
	}
}

// TestBlockingFuncNames guards the sink list against a silent typo: a name that does not
// match anything reports nothing, and nothing is exactly what a passing run looks like.
func TestBlockingFuncNames(t *testing.T) {
	for _, name := range []string{
		"time.Sleep",
		"(*sync.WaitGroup).Wait",
		"(*sync.Cond).Wait",
		"os/user.Lookup",
		"(*os/user.User).GroupIds",
		"(*net/http.Client).Do",
	} {
		if !blockingFuncs[name] {
			t.Errorf("%s must be on the sink list", name)
		}
	}
	// The shape a name takes is the type checker's, not the source's: a package level
	// function carries its import path and a method its receiver in parentheses. A key
	// written the way the call is written matches nothing.
	for _, name := range []string{"Sleep", "user.Lookup", "sync.WaitGroup.Wait"} {
		if blockingFuncs[name] {
			t.Errorf("%s is not the name the type checker produces", name)
		}
	}
}

// TestBlockingReleasedIntersectsOnMerge pins the fold direction for the blocking side of the
// summary, which is the one addAcquire uses: a sink reached with the caller's lock still
// held is not excused by another sink that is reached after releasing it.
func TestBlockingReleasedIntersectsOnMerge(t *testing.T) {
	f := &summaryFact{}
	if !f.addBlocking([]string{"Context", "Task"}, false) {
		t.Error("the first sink is a change")
	}
	if !f.Blocking {
		t.Fatal("the bit must be set")
	}
	if f.addBlocking([]string{"Context", "Task"}, false) {
		t.Error("the same sink again is not a change")
	}
	if !f.addBlocking([]string{"Task"}, false) {
		t.Error("narrowing the released set is a change")
	}
	if got, want := f.BlockingReleased, []string{"Task"}; !equalClasses(got, want) {
		t.Errorf("BlockingReleased = %v, want %v", got, want)
	}
	if !f.addBlocking(nil, false) {
		t.Error("a sink reached with nothing released empties the set")
	}
	if len(f.BlockingReleased) != 0 {
		t.Errorf("BlockingReleased = %v, want empty", f.BlockingReleased)
	}
}

// TestBlockingNamedSticks pins the reason a function blocks. A named wait is a statement
// about the callee that holds wherever it is called from, so it must survive being merged
// with an inferred one, and a function that reaches both is named.
func TestBlockingNamedSticks(t *testing.T) {
	f := &summaryFact{}
	f.addBlocking(nil, false)
	if f.BlockingNamed {
		t.Error("a channel operation does not name a wait")
	}
	if !f.addBlocking(nil, true) {
		t.Error("learning that a named wait is reachable is a change")
	}
	if !f.BlockingNamed {
		t.Error("a named wait must be recorded as named")
	}
	if f.addBlocking(nil, false) {
		t.Error("an inferred wait after a named one changes nothing")
	}
	if !f.BlockingNamed {
		t.Error("an inferred wait must not unname a named one")
	}

	// The bit survives a merge in both directions, since the fixpoint seeds a summary
	// from the previous round and folds the callees into it.
	g := &summaryFact{}
	if !g.merge(f) {
		t.Error("merging a named blocking summary is a change")
	}
	if !g.Blocking || !g.BlockingNamed {
		t.Errorf("merge lost the blocking state: %s", g)
	}
}

// TestBlockingGroupKey pins the group key; corpus expectations are consumed per site, before grouping.
func TestBlockingGroupKey(t *testing.T) {
	fset := token.NewFileSet()
	file := fset.AddFile("group.go", -1, 1000)
	lines := make([]int, 100)
	for i := range lines {
		lines[i] = i * 10
	}
	file.SetLines(lines)
	var got []string
	pass := &analysis.Pass{
		Fset:   fset,
		Report: func(d analysis.Diagnostic) { got = append(got, d.Message) },
	}
	defer func(prev bool) { groupBlockingReports = prev }(groupBlockingReports)
	groupBlockingReports = true
	pc := &lockOrderContext{
		expectations: newExpectations(pass, lockBlockingAnnotations, true),
		pass:         pass,
		order:        newOrder(),
		check:        checkBlocking,
	}

	// Two callees of one name, each reaching its wait its own way.
	const what = "calling (Queue).Drain, which may block"
	var summary summaryFact
	pc.noteBlocking(file.LineStart(10), what, "(a.Queue).Drain", []string{"a wait at a.go:1"},
		[]string{"Grouped"}, nil, true /* named */, &summary, true /* report */)
	pc.noteBlocking(file.LineStart(20), what, "(b.Queue).Drain", []string{"a wait at b.go:1"},
		[]string{"Grouped"}, nil, true /* named */, &summary, true /* report */)
	pc.flushGroups()

	const tail = ") while holding Grouped: a wait under a lock stalls every other user of it"
	want := []string{what + " (a wait at a.go:1" + tail, what + " (a wait at b.go:1" + tail}
	if !slices.Equal(got, want) {
		t.Errorf("reported\n%q\nwant\n%q", got, want)
	}
}

// buildTestPackage builds one file under path, which decides whether a sink matches.
func buildTestPackage(t *testing.T, path, src string) *ssa.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path+"/x.go", src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	pkg, _, err := ssautil.BuildPackage(&types.Config{}, fset, types.NewPackage(path, f.Name.Name), []*ast.File{f}, ssa.BuilderMode(0))
	if err != nil {
		t.Fatalf("building %s: %v", path, err)
	}
	return pkg
}

// ssaMethod returns the declared method typeName.name.
func ssaMethod(t *testing.T, pkg *ssa.Package, typeName, name string) *ssa.Function {
	t.Helper()
	obj := pkg.Pkg.Scope().Lookup(typeName)
	if obj == nil {
		t.Fatalf("no type %s in %s", typeName, pkg.Pkg.Path())
	}
	sel := types.NewMethodSet(obj.Type()).Lookup(pkg.Pkg, name)
	if sel == nil {
		t.Fatalf("no method %s on %s", name, typeName)
	}
	return pkg.Prog.MethodValue(sel)
}

// ssaWrapper returns the wrapper ssa built whose name ends in suffix.
func ssaWrapper(t *testing.T, pkg *ssa.Package, suffix string) *ssa.Function {
	t.Helper()
	for fn := range ssautil.AllFunctions(pkg.Prog) {
		if strings.HasSuffix(fn.Name(), suffix) {
			return fn
		}
	}
	t.Fatalf("no %s wrapper in %s", suffix, pkg.Pkg.Path())
	return nil
}

// TestFuncObjectOrigin pins that funcObject returns the declaration and calledFunc the instance.
func TestFuncObjectOrigin(t *testing.T) {
	const src = `package q

type Gen[T any] struct{}

func (Gen[T]) Wait() {}

func calls(g Gen[int]) {
	g.Wait()
	func() {}()
}
`
	pkg := buildTestPackage(t, "example.com/q", src)
	var instance, closure *ssa.Function
	for fn := range ssautil.AllFunctions(pkg.Prog) {
		if fn.Origin() != nil {
			instance = fn
		}
		if fn.Name() == "calls$1" {
			closure = fn
		}
	}
	if instance == nil || closure == nil {
		t.Fatalf("instance %v, closure %v", instance, closure)
	}
	decl := instance.Origin().Object()
	if got := funcObject(instance); got != decl {
		t.Errorf("funcObject(instance) = %v, want the declaration %v", got, decl)
	}
	if got := calledFunc(instance); got == nil || got == decl || got != instance.Object() {
		t.Errorf("calledFunc(instance) = %v, want the instance's own object", got)
	}
	if funcObject(closure) != nil || calledFunc(closure) != nil {
		t.Errorf("a closure has no object")
	}
}

// TestCalleeKey pins callee identity: same-named methods differ, wrappers equal their method.
func TestCalleeKey(t *testing.T) {
	// The method value and the method expression are what make ssa build the wrappers.
	const src = `package q

type Queue struct{}

func (Queue) Drain(c chan int) {}

func drainers(q Queue) (func(chan int), func(Queue, chan int)) { return q.Drain, Queue.Drain }
`
	a := buildTestPackage(t, "example.com/a", src)
	b := buildTestPackage(t, "example.com/b", src)
	drainA, drainB := ssaMethod(t, a, "Queue", "Drain"), ssaMethod(t, b, "Queue", "Drain")
	if displayName(drainA) != displayName(drainB) {
		t.Fatalf("the two methods must print alike: %s, %s", displayName(drainA), displayName(drainB))
	}
	if calleeKey(drainA) == calleeKey(drainB) {
		t.Errorf("two methods of one name share the key %q", calleeKey(drainA))
	}
	for _, suffix := range []string{"$bound", "$thunk"} {
		wrapper := ssaWrapper(t, a, suffix)
		if got, want := calleeKey(wrapper), calleeKey(drainA); got != want {
			t.Errorf("calleeKey(%s) = %q, want %q", wrapper.Name(), got, want)
		}
	}
}

// TestInterfaceSinkKey pins one key for one method reached through two interface types.
func TestInterfaceSinkKey(t *testing.T) {
	// A generated client's verb, whose path is what marks it as a round trip.
	const src = `package v1

type Reader interface{ Get() }

type ReadCloser interface {
	Reader
	Close()
}

func read(r Reader)           { r.Get() }
func readClose(c ReadCloser)  { c.Get() }
`
	pkg := buildTestPackage(t, "k8s.io/client-go/kubernetes/typed/core/v1", src)
	type sink struct {
		name, key string
		pos       token.Pos
	}
	var sinks []sink
	for _, name := range []string{"read", "readClose"} {
		fn := pkg.Func(name)
		if fn == nil {
			t.Fatalf("no function %s", name)
		}
		found := false
		for _, block := range fn.Blocks {
			for _, instr := range block.Instrs {
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				what, key, isSink := blockingCallee(call, nil)
				if !isSink {
					continue
				}
				sinks, found = append(sinks, sink{what, key, call.Pos()}), true
			}
		}
		if !found {
			t.Fatalf("the call in %s is not recognised as a sink", name)
		}
	}
	if len(sinks) != 2 {
		t.Fatalf("got %d sinks, want 2", len(sinks))
	}
	if sinks[0].name == sinks[1].name {
		t.Fatalf("each call must print through its own interface type, got %q for both", sinks[0].name)
	}
	if sinks[0].key != sinks[1].key {
		t.Errorf("one method through two interfaces has two keys: %q, %q", sinks[0].key, sinks[1].key)
	}

	// The two sites are one defect, so grouped mode reports them once.
	var got []string
	pass := &analysis.Pass{
		Fset:   pkg.Prog.Fset,
		Report: func(d analysis.Diagnostic) { got = append(got, d.Message) },
	}
	defer func(prev bool) { groupBlockingReports = prev }(groupBlockingReports)
	groupBlockingReports = true
	pc := &lockOrderContext{
		expectations: newExpectations(pass, lockBlockingAnnotations, true),
		pass:         pass,
		order:        newOrder(),
		check:        checkBlocking,
	}
	var summary summaryFact
	for _, s := range sinks {
		pc.noteBlocking(s.pos, "calling "+s.name+", which blocks", s.key, nil,
			[]string{"Grouped"}, nil, true /* named */, &summary, true /* report */)
	}
	pc.flushGroups()
	if len(got) != 1 {
		t.Errorf("reported %q, want one group", got)
	}
}

// TestChannelOpKey pins the key of a channel operation: a send and a receive are two defects.
func TestChannelOpKey(t *testing.T) {
	const src = `package c

type G struct{}

func f(g *G, c chan int) {
	c <- 1
	<-c
}
`
	pkg := buildTestPackage(t, "example.com/c", src)
	fn := pkg.Func("f")
	if fn == nil {
		t.Fatal("no function f")
	}
	var got []string
	view := &engineView{}
	pass := &analysis.Pass{
		Fset:     pkg.Prog.Fset,
		Pkg:      pkg.Pkg,
		Report:   func(d analysis.Diagnostic) { got = append(got, d.Message) },
		ResultOf: map[*analysis.Analyzer]any{Analyzer: &ownerResult{engine: view}},
		ImportObjectFact: func(obj types.Object, fact analysis.Fact) bool {
			cf, ok := fact.(*classFact)
			if ok && obj.Name() == "G" {
				cf.Class = "Grouped"
				return true
			}
			return false
		},
	}
	defer func(prev bool) { groupBlockingReports = prev }(groupBlockingReports)
	groupBlockingReports = true
	pc := &lockOrderContext{
		expectations: newExpectations(pass, lockBlockingAnnotations, true),
		pass:         pass,
		order:        newOrder(),
		check:        checkBlocking,
	}
	held := newLockPath(pathRootParam, fn.Params[0], nil, "g")
	ls := newLockState()
	ls.lockedMutexes[held.Key()] = lockInfo{path: held}
	cur := pc.recordState()
	var summary summaryFact
	for _, block := range fn.Blocks {
		for _, instr := range block.Instrs {
			view.add(instr, ls, nil)
			pc.visitChannelOp(fn, instr, cur, &summary, true /* report */)
		}
	}
	pc.flushGroups()
	if len(got) != 2 {
		t.Errorf("reported %q, want the send and the receive", got)
	}
	for _, msg := range got {
		if !strings.Contains(msg, "while holding Grouped") {
			t.Errorf("report %q does not name the held class", msg)
		}
	}
}
