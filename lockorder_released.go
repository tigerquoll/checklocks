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
func (pc *lockOrderContext) shadowRelease(fn *ssa.Function, call ssa.CallInstruction, recv ssa.Value, class string, cur siteLocks, report bool) {
	cs, ok := cur.(*classSet)
	if !ok || class == "" || !isReceiver(fn, recv) || !pc.releasedShadowOn(report) || !pc.callerOwned(fn, call, class) {
		return
	}
	if cs.d2 == nil {
		cs.d2 = make(map[string]bool)
	}
	cs.d2[class] = true
}

// shadowAcquire clears d2 of class: taking the lock back closes the gap.
func (pc *lockOrderContext) shadowAcquire(cur siteLocks, class string) {
	if cs, ok := cur.(*classSet); ok {
		delete(cs.d2, class)
	}
}

// shadowGains clears d2 of the classes of the locks the call took.
func (pc *lockOrderContext) shadowGains(fn *ssa.Function, call ssa.CallInstruction, cur siteLocks, report bool) {
	cs, ok := cur.(*classSet)
	if !ok || len(cs.d2) == 0 || !pc.releasedShadowOn(report) {
		return
	}
	view := pc.engineView()
	for _, gained := range []map[pathKey]lockPath{view.gainsOf(call), view.opGainsOf(call)} {
		for _, p := range gained {
			classes, _ := pc.engineClasses(fn, heldLock{path: p})
			for _, c := range classes {
				delete(cs.d2, c)
			}
		}
	}
}

// shadowReleased logs where the released set a reader sees differs from d2.
func (pc *lockOrderContext) shadowReleased(fn *ssa.Function, call ssa.Instruction, cur siteLocks, report bool, reader string) {
	cs, ok := cur.(*classSet)
	if !ok || !pc.releasedShadowOn(report) {
		return
	}
	old, d2 := cur.releasedClasses(), slices.Sorted(maps.Keys(cs.d2))
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
