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
	"go/token"
	"go/types"
	"maps"
	"slices"

	"golang.org/x/tools/go/ssa"
)

// wrapperSite is a lock method that is recognised by its receiver's class alone, with the
// lowest call site that reached it.
type wrapperSite struct {
	obj        *types.Func
	pos        token.Pos
	callee     string
	class      string
	annotation string
	verb       string
}

// checkWrapper records a call to a lock method that only its receiver's class marks as a
// lock operation, when the method carries no acquire or release annotation.
func (pc *lockOrderContext) checkWrapper(call ssa.CallInstruction, callee *ssa.Function, obj *types.Func) {
	op := lockOpName(obj.Name())
	if op == opNone || isStandardLock(pc.pass, obj) {
		return
	}
	recv, ok := receiverOf(call)
	if !ok {
		return
	}
	class := pc.classOf(recv)
	if class == "" || pc.lockAnnotated(obj) {
		return
	}
	site := &wrapperSite{obj: obj.Origin(), pos: call.Pos(), callee: displayName(callee), class: class}
	site.annotation, site.verb = "checklocksrelease", "drops"
	if op == opAcquire {
		site.annotation, site.verb = "checklocksacquire", "takes"
	}
	if lockOpShared(obj.Name()) {
		site.annotation += "read"
	}
	prev, seen := pc.wrappers[site.obj]
	if seen && comparePos(pc.pass.Fset, prev.pos, site.pos) <= 0 {
		return
	}
	if pc.wrappers == nil {
		pc.wrappers = make(map[*types.Func]*wrapperSite)
	}
	pc.wrappers[site.obj] = site
}

// lockAnnotated reports whether the method's facts take or drop a lock, as its name says.
// DowngradeLock counts either.
func (pc *lockOrderContext) lockAnnotated(obj *types.Func) bool {
	var lff lockFunctionFacts
	if !pc.pass.ImportObjectFact(obj.Origin(), &lff) {
		return false
	}
	takes := onlyIn(lff.HeldOnExit, lff.HeldOnEntry)
	drops := onlyIn(lff.HeldOnEntry, lff.HeldOnExit)
	switch lockOpName(obj.Name()) {
	case opAcquire:
		return takes
	case opRelease:
		return drops
	}
	return takes || drops
}

// onlyIn reports whether a guard is in a and not in b.
func onlyIn(a, b map[string]functionGuardInfo) bool {
	for guard := range a {
		if _, both := b[guard]; !both {
			return true
		}
	}
	return false
}

// reportWrappers reports each lock method once: at its declaration when this package
// declares it, otherwise at the lowest call site.
func (pc *lockOrderContext) reportWrappers() {
	type row struct {
		at   token.Pos
		site *wrapperSite
	}
	var rows []row
	for site := range maps.Values(pc.wrappers) {
		at := site.pos
		if site.obj.Pkg() == pc.pass.Pkg {
			at = site.obj.Pos()
		}
		rows = append(rows, row{at, site})
	}
	slices.SortFunc(rows, func(a, b row) int { return comparePos(pc.pass.Fset, a.at, b.at) })
	for _, r := range rows {
		s := r.site
		pc.maybeFail(r.at, "%s is a lock method of class %s with no +%s annotation: lockorder and lockblocking do not see the lock it %s",
			s.callee, s.class, s.annotation, s.verb)
	}
}
