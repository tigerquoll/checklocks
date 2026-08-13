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

// The lockblocking analyzer reports waiting while a lock is held.
//
// A lock held across a wait is not a deadlock and no ordering rule is broken, so neither the
// ordering analyzer nor a runtime cycle detector has anything to say about it. What it does
// is bound the time every other user of that lock waits by the time the wait takes, which
// for a round trip to another process is unbounded: if the far side never answers, the lock
// is never released and the system stops rather than deadlocking.
//
// It rides on the summaries the lockorder analyzer builds. The same walk records, for every
// function, whether it may reach a blocking sink and what it had released by the time it
// does; this analyzer walks the package again asking only that question of each call site.
// The fact types are inherited from the required analyzer rather than declared again: a
// fact type may be declared by only one analyzer in a binary.
//
// What counts as waiting is a list, not an inference. The list holds the operations that
// wait by definition, plus the client libraries whose calls are round trips, and it is
// extended for anything else with the "+blocking" annotation on the function that waits.
package checklocks

import (
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const (
	// blockingAnnotation declares a function to be a blocking sink, for a wait this
	// analysis cannot see: behind an indirect call, or inside a dependency that is not
	// analyzed.
	//
	//	// +blocking
	//	func (c *Cache) resolve(name string) (*User, error)
	blockingAnnotation = "// +blocking"

	// lockBlockingIgnore suppresses the checks in a function or on a single line.
	lockBlockingIgnore = "// +lockblockingignore"

	// lockBlockingFail records an expected diagnostic in the test corpus.
	lockBlockingFail = "// +lockblockingfail"
)

// lockBlockingAnnotations is the self-check annotation set belonging to this analyzer.
//
// There is no force annotation: what this analysis asserts is that a wait is reached with a
// lock held, which a single position cannot express.
var lockBlockingAnnotations = annotationSet{
	fail:   lockBlockingFail,
	ignore: lockBlockingIgnore,
}

// LockBlockingAnalyzer reports waiting while a lock class is held.
var LockBlockingAnalyzer = &analysis.Analyzer{
	Name:     "lockblocking",
	Doc:      "checks that no lock class is held across a blocking call",
	Run:      runLockBlocking,
	Requires: []*analysis.Analyzer{buildssa.Analyzer, Analyzer, LockOrderAnalyzer},
}

func init() {
	LockBlockingAnalyzer.Flags.BoolVar(&groupBlockingReports, "group", false,
		"report the call sites of one blocking defect as a single grouped diagnostic")
}

// blockingFuncs are the functions that wait, named as they appear in the type checker.
//
// Taking a lock is deliberately absent. Every lock acquisition waits in principle, and
// including them would report all nesting as blocking and say nothing; that nesting is the
// ordering analyzer's subject, with a declared order to judge it by.
var blockingFuncs = map[string]bool{
	"time.Sleep":             true,
	"(*sync.WaitGroup).Wait": true,
	"(*sync.Cond).Wait":      true,

	// Resolving a user or a group goes through the name service switch, which reaches
	// LDAP or another directory over the network on a configured host.
	"os/user.Lookup":           true,
	"os/user.LookupId":         true,
	"os/user.LookupGroup":      true,
	"os/user.LookupGroupId":    true,
	"(*os/user.User).GroupIds": true,

	// HTTP round trips. The client methods and the transport underneath them, so a
	// hand-built client is covered as well as the package level helpers.
	"net/http.Get":                      true,
	"net/http.Head":                     true,
	"net/http.Post":                     true,
	"net/http.PostForm":                 true,
	"(*net/http.Client).Do":             true,
	"(*net/http.Client).Get":            true,
	"(*net/http.Client).Head":           true,
	"(*net/http.Client).Post":           true,
	"(*net/http.Client).PostForm":       true,
	"(*net/http.Transport).RoundTrip":   true,
	"(net/http.RoundTripper).RoundTrip": true,
}

// k8sClientPrefixes are the packages of the kubernetes client library that talk to the API
// server. Its clients are reached through interfaces, so there is no callee to consult; the
// package the method belongs to is what is recognised instead.
//
// The library is not listed whole. Most of what a scheduler calls is the informer side of
// it, and a lister reads the informer's local store: k8s.io/client-go/listers and
// tools/cache do no I/O at all, and reporting a lister read as a round trip would condemn
// every cache lookup under a lock in a code base built on informers.
var k8sClientPrefixes = []string{
	"k8s.io/client-go/kubernetes",
	"k8s.io/client-go/rest",
	"k8s.io/client-go/dynamic",
	"k8s.io/client-go/discovery",
	"k8s.io/client-go/metadata",
}

// k8sRequests are the methods of a generated kubernetes client that talk to the API server.
//
// The name matters as well as the package: the accessors that get hold of a client, such as
// CoreV1() and Secrets(), live in the same packages and do no I/O at all, so treating the
// whole library as blocking would report the wrong line and would call functions blocking
// that are not.
var k8sRequests = map[string]bool{
	"Get":              true,
	"List":             true,
	"Watch":            true,
	"Create":           true,
	"Update":           true,
	"UpdateStatus":     true,
	"Delete":           true,
	"DeleteCollection": true,
	"Patch":            true,
	"Apply":            true,
	"ApplyStatus":      true,
	// The rest client's own round trip, which the generated clients are built on.
	"Do":     true,
	"DoRaw":  true,
	"Stream": true,
}

// runLockBlocking is the entrypoint for the lockblocking analyzer.
func runLockBlocking(pass *analysis.Pass) (any, error) {
	// The walk, the class resolution and the summaries are the ordering analyzer's, which
	// has already run over this package and exported them; only the question asked at a
	// call site is this analyzer's. The order itself is not consulted, so it is left empty.
	pc := &lockOrderContext{
		expectations: newExpectations(pass, lockBlockingAnnotations, true /* reportInvalidPos */),
		pass:         pass,
		order:        newOrder(),
		check:        checkBlocking,
	}
	pc.extractLineFailures()

	state := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	for _, fn := range state.SrcFuncs {
		pc.analyzeFunction(fn, &summaryFact{}, true)
	}

	pc.flushGroups()
	pc.checkFailures()
	return nil, nil
}

// blockingCallee reports whether a call is a call to a known sink, and names it for the
// diagnostic.
//
// A call through an interface has no callee, which is the usual shape of a client library
// call: the method the interface declares is what is matched then.
func blockingCallee(call ssa.CallInstruction, callee *ssa.Function) (string, bool) {
	common := call.Common()
	if common.Method != nil {
		if isBlockingFunc(common.Method) {
			return "(" + shortType(common.Value.Type()) + ")." + common.Method.Name(), true
		}
		return "", false
	}
	obj := funcObject(callee)
	if obj == nil || !isBlockingFunc(obj) {
		return "", false
	}
	return displayName(callee), true
}

// isBlockingFunc reports whether a function is on the built-in sink list.
func isBlockingFunc(obj *types.Func) bool {
	if blockingFuncs[obj.FullName()] {
		return true
	}
	pkg := ""
	if obj.Pkg() != nil {
		pkg = obj.Pkg().Path()
	}
	return isK8sRequest(pkg, obj.Name())
}

// isK8sRequest reports whether a package path and a method name name a call that talks to
// the kubernetes API server.
func isK8sRequest(pkgPath, name string) bool {
	if !k8sRequests[name] {
		return false
	}
	for _, prefix := range k8sClientPrefixes {
		if pkgPath == prefix || strings.HasPrefix(pkgPath, prefix+"/") {
			return true
		}
	}
	return false
}
