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

import "testing"

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
