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
	"cmp"
	"fmt"
	"go/token"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// shadowLogDir is where the shadow logs go; empty records and writes nothing.
var shadowLogDir = os.Getenv("CHECKLOCKS_SHADOW_LOG")

// originOf says how the engine came to hold a lock.
func originOf(l heldLock) string {
	switch {
	case l.assumed:
		return "forced"
	case l.entry:
		return "entry"
	}
	return "body"
}

type shadowLine struct {
	pos  token.Pos
	text string
}

// lockOrderShadow is what lockorder records per pass: the classes held at each call of the report walk,
// and the comparison with the engine. The classes are lockorder's state at the site, with the entry
// overlay applied and the call's own effect not.
type lockOrderShadow struct {
	held  map[ssa.Instruction]map[string]struct{}
	lines []shadowLine

	compared, diff, lonly, eonly, noengine, unmapped, unrecorded int
	noview                                                       bool
	panicked                                                     string
}

func (pc *lockOrderContext) shadowCall(call ssa.CallInstruction, cur *classSet, report bool) {
	if shadowLogDir == "" || !report || pc.check != checkOrder || pc.witness != nil {
		return
	}
	s := &pc.shadow
	if s.held == nil {
		s.held = make(map[ssa.Instruction]map[string]struct{})
	}
	rec := s.held[call]
	if rec == nil {
		rec = make(map[string]struct{})
		s.held[call] = rec
	}
	for _, c := range cur.held() {
		rec[c] = struct{}{}
	}
}

// shadowCompare compares the classes lockorder held at each call with the engine's.
func (pc *lockOrderContext) shadowCompare() {
	if shadowLogDir == "" || pc.check != checkOrder {
		return
	}
	s := &pc.shadow
	defer func() {
		if r := recover(); r != nil {
			s.panicked = strings.ReplaceAll(fmt.Sprint(r), "\n", " ")
		}
	}()
	view := pc.engineView()
	if view == nil {
		s.noview = true
		return
	}
	pkg := pc.pass.Pkg.Path()
	for inst, lo := range s.held {
		pos := pc.pass.Fset.Position(inst.Pos())
		fn := inst.Parent()
		eng, ok := view.heldAt(inst)
		if !ok {
			s.noengine++
			if len(lo) > 0 {
				if _, walked := view.entryOf(fn); walked {
					s.unrecorded++
				}
				s.lines = append(s.lines, shadowLine{inst.Pos(), fmt.Sprintf("X\t%s\t%s\t%s\tL=%s\t%s", pkg, pos, relFuncName(fn), strings.Join(slices.Sorted(maps.Keys(lo)), ","), view.whyNone(fn))})
			}
			continue
		}
		s.compared++
		ec := make(map[string]struct{})
		origins := make(map[string][]string)
		for _, l := range eng {
			class, rule := pc.engineClass(l.path)
			if class == "" {
				s.unmapped++
				continue
			}
			ec[class] = struct{}{}
			origins[class] = append(origins[class], fmt.Sprintf("%s/%c", originOf(l), rule))
		}
		var lOnly, eOnly []string
		for c := range lo {
			if _, ok := ec[c]; !ok {
				lOnly = append(lOnly, c)
			}
		}
		for c := range ec {
			if _, ok := lo[c]; !ok {
				eOnly = append(eOnly, c)
			}
		}
		if len(lOnly) == 0 && len(eOnly) == 0 {
			continue
		}
		slices.Sort(lOnly)
		slices.Sort(eOnly)
		s.diff++
		if len(lOnly) > 0 {
			s.lonly++
		}
		if len(eOnly) > 0 {
			s.eonly++
		}
		kind := "call"
		if obj := calledFunc(staticCallee(inst.(ssa.CallInstruction))); obj != nil {
			switch lockOpName(obj.Name()) {
			case opAcquire:
				kind = "acquire"
			case opRelease:
				kind = "release"
			}
		}
		fields := []string{"D", pkg, pos.String(), relFuncName(fn), kind, "Lonly=" + strings.Join(lOnly, ","), "Eonly=" + strings.Join(eOnly, ",")}
		for _, c := range eOnly {
			o := origins[c]
			slices.Sort(o)
			fields = append(fields, c+"="+strings.Join(slices.Compact(o), ","))
		}
		s.lines = append(s.lines, shadowLine{inst.Pos(), strings.Join(fields, "\t")})
	}
}

// shadowFlush writes this unit's shadow log; cycle is set for the declared-cycle return, which compares nothing.
func (pc *lockOrderContext) shadowFlush(cycle bool) {
	if shadowLogDir == "" {
		return
	}
	s := &pc.shadow
	pkg := pc.pass.Pkg.Path()
	slices.SortFunc(s.lines, func(a, b shadowLine) int {
		return cmp.Or(comparePos(pc.pass.Fset, a.pos, b.pos), strings.Compare(a.text, b.text))
	})
	var out strings.Builder
	for _, l := range s.lines {
		out.WriteString(l.text + "\n")
	}
	if s.panicked != "" {
		fmt.Fprintf(&out, "P\t%s\t%s\n", pkg, s.panicked)
	}
	fmt.Fprintf(&out, "N\t%s\tcompared=%d\tdiff=%d\tlonly=%d\teonly=%d\tnoengine=%d\tunmapped=%d\tunrecorded=%d", pkg, s.compared, s.diff, s.lonly, s.eonly, s.noengine, s.unmapped, s.unrecorded)
	switch {
	case cycle:
		out.WriteString(" cycle")
	case s.noview:
		out.WriteString(" noview")
	}
	out.WriteString("\n")

	name := url.PathEscape(pkg)
	for _, f := range pc.pass.Files {
		if strings.HasSuffix(pc.pass.Fset.File(f.Pos()).Name(), "_test.go") {
			name += "@test"
			break
		}
	}
	tmp, err := os.CreateTemp(shadowLogDir, name+".tmp*")
	if err == nil {
		_, err = tmp.WriteString(out.String())
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(shadowLogDir, name+".log"))
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "checklocks: shadow log: %v\n", err)
	}
}
