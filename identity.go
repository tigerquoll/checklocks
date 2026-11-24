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
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// sameElement reports whether two states are one element of the walk of fn: the solver keys on
// classKey plus markerKey. A lockState component is identity or payload:
//
//	held locks, with mode and assumed tag    identity, classKey
//	relevant defers, in stack order          identity, classKey
//	aliases                                  identity, classKey
//	stored values of fn's relevant slots     identity, classKey
//	marker or top, with its regions          identity, markerKey
//	no-op defers                             payload, joined over every arrival by joinDefers
//	other stored values, bound, used         payload, the first arrival's is kept
//	substitutions, consts                    payload, the first arrival's is kept
//
// See relevantSlots for the slots. Two answers read an element:
//
//	query (state.go)         held, notHeld or unknown, for one lock in one state. Unknown: the marker names
//	                         the lock, or is top and the lock has a value in a dropped lock's region.
//	engineView.query         all, some, none or unknown, for the states recorded at one site. Unknown:
//	                         no record, a function given up on, no state held the lock and a state's marker
//	                         names it or was top, or an inexact site: a no-op defer run as a may entry, an
//	                         unwind in which a relevant deferred call's lock operation failed or which runs
//	                         a relevant deferred literal, or a relevant defer in a cycle.
//
// Panic unwinds are not walked, so deferred-call answers cover normal returns only.
//
// Polarity: a client suppresses or derives only on a definite answer; unknown always reports and never
// creates a report. Where isHeld is read, unknown reads as not held.
func (pc *passContext) sameElement(fn *ssa.Function, a, b *lockState) bool {
	return a.isCompatible(b) && a.markerTop == b.markerTop && maps.Equal(a.topRegions, b.topRegions) && maps.EqualFunc(a.marker, b.marker, func(x, y regionSet) bool { return maps.Equal(x, y) }) && pc.sameRelevantDefers(a, b) && maps.EqualFunc(a.aliases, b.aliases, func(x, y lockPath) bool {
		return x.Key() == y.Key()
	}) && pc.sameRelevantStored(fn, a, b)
}

// emptyClassKey is the class key of a state with no identity component but the marker.
const emptyClassKey = "0;0;0;0;"

// elementKey encodes the identity components of s, so two states have one key exactly when sameElement holds.
func (pc *passContext) elementKey(fn *ssa.Function, s *lockState) string {
	return pc.classKey(fn, s) + markerKey(s)
}

// classKey encodes the identity components of s but its marker: two states share it exactly
// when they are one element once their markers are cleared.
func (pc *passContext) classKey(fn *ssa.Function, s *lockState) string {
	if len(s.lockedMutexes) == 0 && len(s.defers) == 0 && len(s.aliases) == 0 {
		if si := pc.relevantSlots(fn); si == nil || len(si.slots) == 0 {
			return emptyClassKey
		}
	}
	return pc.buildClassKey(fn, s)
}

// markerKey encodes the marker of s: top and its regions, then each key with its regions. Top
// has a part no key set has.
func markerKey(s *lockState) string {
	var b strings.Builder
	if s.markerTop {
		b.WriteString("T;")
		b.WriteString(s.topRegions.key())
	} else {
		b.WriteString("0;")
	}
	keys := make([]string, 0, len(s.marker))
	for k := range s.marker {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	b.WriteString(strconv.Itoa(len(keys)))
	b.WriteByte(';')
	for _, k := range keys {
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteString(s.marker[pathKey(k)].key())
	}
	return b.String()
}

// key encodes the set as sorted length-prefixed tokens, each a kind, a function and a block index.
func (s regionSet) key() string {
	tokens := make([]string, 0, len(s))
	for r := range s {
		kind := "B"
		if r.loop {
			kind = "L"
		}
		tokens = append(tokens, kind+relFuncName(r.block.Parent())+"#"+strconv.Itoa(r.block.Index))
	}
	slices.Sort(tokens)
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(tokens)))
	b.WriteByte(';')
	for _, t := range tokens {
		b.WriteString(strconv.Itoa(len(t)))
		b.WriteByte(':')
		b.WriteString(t)
	}
	return b.String()
}

func (pc *passContext) buildClassKey(fn *ssa.Function, s *lockState) string {
	var b strings.Builder
	part := func(sorted bool, parts []string) {
		if sorted {
			slices.Sort(parts)
		}
		b.WriteString(strconv.Itoa(len(parts)))
		b.WriteByte(';')
		for _, p := range parts {
			b.WriteString(strconv.Itoa(len(p)))
			b.WriteByte(':')
			b.WriteString(p)
		}
	}
	locks := make([]string, 0, len(s.lockedMutexes))
	for k, info := range s.lockedMutexes {
		locks = append(locks, fmt.Sprintf("%s\x00%t\x00%t", k, info.exclusive, info.assumed))
	}
	part(true, locks)
	var defers []string
	for _, d := range s.defers {
		if pc.deferRelevant(d) {
			defers = append(defers, fmt.Sprintf("%p", d))
		}
	}
	part(false, defers)
	aliases := make([]string, 0, len(s.aliases))
	for k, to := range s.aliases {
		aliases = append(aliases, fmt.Sprintf("%s\x00%s", k, to.Key()))
	}
	part(true, aliases)
	var stored []string
	if si := pc.relevantSlots(fn); si != nil {
		for slot := range si.slots {
			if v, ok := s.stored[slot]; ok {
				stored = append(stored, fmt.Sprintf("%p=%p", slot, v))
			} else {
				stored = append(stored, fmt.Sprintf("%p=-", slot))
			}
		}
	}
	part(true, stored)
	return b.String()
}

// sameRelevantStored reports whether a and b hold the same values in fn's relevant slots.
func (pc *passContext) sameRelevantStored(fn *ssa.Function, a, b *lockState) bool {
	si := pc.relevantSlots(fn)
	if si == nil {
		return true
	}
	for slot := range si.slots {
		va, oka := a.stored[slot]
		vb, okb := b.stored[slot]
		if oka != okb || va != vb {
			return false
		}
	}
	return true
}

// sameRelevantDefers reports whether the defers of a and b that can matter are the same, in order.
func (pc *passContext) sameRelevantDefers(a, b *lockState) bool {
	i, j := 0, 0
	for {
		for i < len(a.defers) && !pc.deferRelevant(a.defers[i]) {
			i++
		}
		for j < len(b.defers) && !pc.deferRelevant(b.defers[j]) {
			j++
		}
		if i == len(a.defers) || j == len(b.defers) {
			return i == len(a.defers) && j == len(b.defers)
		}
		if a.defers[i] != b.defers[j] {
			return false
		}
		i++
		j++
	}
}

// deferRelevant reports whether running d can add a decision, report or observation, or
// change the state. It mirrors the dispatch of checkCall and errs towards true.
func (pc *passContext) deferRelevant(d *ssa.Defer) (relevant bool) {
	if r, ok := pc.relevantDefers[d]; ok {
		return r
	}
	defer func() {
		if pc.relevantDefers == nil {
			pc.relevantDefers = make(map[*ssa.Defer]bool)
		}
		pc.relevantDefers[d] = relevant
	}()

	common := d.Common()
	var tf *types.Func
	args := common.Args
	if common.Method != nil {
		tf = common.Method
		args = append([]ssa.Value{common.Value}, args...)
	} else {
		switch fn := common.Value.(type) {
		case *ssa.Function:
			if fn.Object() == nil {
				return true
			}
			var ok bool
			if tf, ok = fn.Object().(*types.Func); !ok {
				return true
			}
		case *ssa.MakeClosure:
			return true
		default:
			return false
		}
	}

	var lff lockFunctionFacts
	pc.importLockFunctionFacts(tf, &lff)
	if len(lff.HeldOnEntry) != 0 || len(lff.HeldOnExit) != 0 || len(lff.ExcludedOnEntry) != 0 {
		return true
	}
	if _, _, _, ok := lockCallSite(pc.pass, common); ok {
		return true
	}
	if callee := common.StaticCallee(); callee != nil {
		if obj := funcObject(callee); obj != nil {
			var ff freshFacts
			if pc.pass.ImportObjectFact(obj, &ff) && len(ff.FreshParams) != 0 {
				return true
			}
		}
	}
	for _, arg := range args {
		if pc.hasAliasFacts(arg) {
			return true
		}
	}
	return false
}

// hasAliasFacts reports whether the chain applyTypeAliases walks from v meets a type with aliases.
func (pc *passContext) hasAliasFacts(v ssa.Value) bool {
	for v != nil {
		var ltf lockTypeFacts
		pc.importLockTypeFacts(v.Type(), &ltf)
		if len(ltf.Aliases) != 0 {
			return true
		}
		switch x := v.(type) {
		case *ssa.Field:
			v = x.X
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return false
			}
			v = x.X
		default:
			return false
		}
	}
	return false
}
