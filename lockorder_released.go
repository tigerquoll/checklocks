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

//go:build checklocks_parity

package checklocks

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// releasedShadowOn says whether the report walk keeps the record-based released set.
func (pc *lockOrderContext) releasedShadowOn(report bool) bool {
	return shadowLogDir != "" && report && pc.check == checkOrder && pc.witness == nil && pc.engineView() != nil
}

// shadowRelease sets d2 for a release of class on the function's own receiver when the record at the call
// shows the lock is the caller's.
func (pc *lockOrderContext) shadowRelease(fn *ssa.Function, call ssa.CallInstruction, recv ssa.Value, class string, cur *classSet, report bool) {
	if class == "" || !isReceiver(fn, recv) || !pc.releasedShadowOn(report) || !pc.callerOwned(fn, call, class) {
		return
	}
	if cur.d2 == nil {
		cur.d2 = make(map[string]bool)
	}
	cur.d2[class] = true
}

// callerOwned says whether the record at call, taken before it, leaves class known and held by no lock the body took.
func (pc *lockOrderContext) callerOwned(fn *ssa.Function, call ssa.CallInstruction, class string) bool {
	view := pc.engineView()
	rec, ok := view.heldSet(call)
	if !ok || view.inexact[call] {
		return false
	}
	sv := view.sites[call]
	if sv == nil || sv.top {
		return false
	}
	for _, p := range sv.marker {
		if c, _ := pc.engineClass(p); c == class {
			return false
		}
	}
	for _, l := range rec {
		if l.assumed || l.entry {
			continue
		}
		if classes, _ := pc.engineClasses(fn, l); slices.Contains(classes, class) {
			return false
		}
	}
	return true
}

// shadowGains clears d2 of the classes of the locks the call took.
func (pc *lockOrderContext) shadowGains(fn *ssa.Function, call ssa.CallInstruction, cur *classSet, report bool) {
	if len(cur.d2) == 0 || !pc.releasedShadowOn(report) {
		return
	}
	view := pc.engineView()
	for _, gained := range []map[pathKey]lockPath{view.gainsOf(call), view.opGainsOf(call)} {
		for _, p := range gained {
			classes, _ := pc.engineClasses(fn, heldLock{path: p})
			for _, c := range classes {
				delete(cur.d2, c)
			}
		}
	}
}

// shadowReleased logs where the released set a reader sees differs from d2.
func (pc *lockOrderContext) shadowReleased(fn *ssa.Function, call ssa.CallInstruction, cur *classSet, report bool, reader string) {
	if !pc.releasedShadowOn(report) {
		return
	}
	old, d2 := cur.releasedClasses(), slices.Sorted(maps.Keys(cur.d2))
	if slices.Equal(old, d2) {
		return
	}
	s := &pc.shadow
	pos := pc.pass.Fset.Position(call.Pos()).String()
	key := strings.Join([]string{pos, relFuncName(fn), reader}, "\t")
	if s.rseen[key] {
		return
	}
	if s.rseen == nil {
		s.rseen = make(map[string]bool)
	}
	s.rseen[key] = true
	s.rdiff++
	s.lines = append(s.lines, shadowLine{call.Pos(), fmt.Sprintf("R\t%s\t%s\t%s\told=%s\td2=%s\t%s", pc.pass.Pkg.Path(), pos, relFuncName(fn), strings.Join(old, ","), strings.Join(d2, ","), reader)})
}
