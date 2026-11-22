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
	"go/types"
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

// shadowOrigin says how the engine came to hold a lock; the highest wins when states are joined.
type shadowOrigin int

const (
	originBody shadowOrigin = iota
	originEntry
	originForced
)

func (o shadowOrigin) String() string { return [...]string{"body", "entry", "forced"}[o] }

// shadowLock is a lock the engine holds at a call.
type shadowLock struct {
	path      lockPath
	exclusive bool
	assumed   bool
	origin    shadowOrigin
}

// shadowEngine is what the engine records per pass: the locks held at each call of a top-level
// function, joined over every state the replay reaches it in, and the functions it gave up on.
type shadowEngine struct {
	held   map[ssa.Instruction]map[pathKey]shadowLock
	gaveUp map[*ssa.Function]string
}

// shadowCall records the locks held at a call of the function being replayed.
func (pc *passContext) shadowCall(site callSite, wc *walkContext, ls *lockState) {
	fn := wc.fn
	if shadowLogDir == "" || pc.emit != emitReplay || site.instr == nil || fn.Parent() != nil || site.instr.Parent() != fn {
		return
	}
	e := &pc.shadow
	if e.held == nil {
		e.held = make(map[ssa.Instruction]map[pathKey]shadowLock)
	}
	rec := e.held[site.instr]
	if rec == nil {
		rec = make(map[pathKey]shadowLock)
		e.held[site.instr] = rec
	}
	entry := pc.records[0].parent
	for key, info := range ls.lockedMutexes {
		origin := originBody
		if _, ok := entry.lockedMutexes[key]; ok {
			origin = originEntry
		}
		if info.assumed {
			origin = originForced
		}
		if cur, ok := rec[key]; ok {
			info.exclusive = info.exclusive || cur.exclusive
			info.assumed = info.assumed || cur.assumed
			origin = max(origin, cur.origin)
		}
		rec[key] = shadowLock{path: info.path, exclusive: info.exclusive, assumed: info.assumed, origin: origin}
	}
}

// shadowGaveUp records why the walk of fn was abandoned.
func (pc *passContext) shadowGaveUp(fn *ssa.Function) {
	if shadowLogDir == "" {
		return
	}
	if pc.shadow.gaveUp == nil {
		pc.shadow.gaveUp = make(map[*ssa.Function]string)
	}
	reason := fmt.Sprintf("more than %d lock states reach one loop", maxHeadStates)
	if pc.overBudget {
		reason = fmt.Sprintf("more than %d block walks", maxFunctionWalks)
	}
	pc.shadow.gaveUp[fn] = reason
}

// shadowView answers lockorder's questions about what the engine held in one pass; its zero value has no pass.
type shadowView struct {
	e *shadowEngine
}

func (pc *passContext) shadowResult() shadowView {
	if shadowLogDir == "" {
		return shadowView{}
	}
	e := pc.shadow
	return shadowView{&e}
}

// heldAt returns the locks the engine held at inst, and false when it recorded none or gave up on inst's function.
func (v shadowView) heldAt(inst ssa.Instruction) ([]shadowLock, bool) {
	rec, ok := v.e.held[inst]
	if !ok || v.e.gaveUp[inst.Parent()] != "" {
		return nil, false
	}
	keys := make([]pathKey, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]shadowLock, len(keys))
	for i, k := range keys {
		out[i] = rec[k]
	}
	return out, true
}

// whyNone says why the engine has no record in fn: "literal", "gave up: <reason>", or "".
func (v shadowView) whyNone(fn *ssa.Function) string {
	if fn.Parent() != nil {
		return "literal"
	}
	if reason := v.e.gaveUp[fn]; reason != "" {
		return "gave up: " + reason
	}
	return ""
}

type shadowLine struct {
	pos  token.Pos
	text string
}

// lockOrderShadow is what lockorder records per pass: the classes held at each call of the report walk,
// and the comparison with the engine.
type lockOrderShadow struct {
	held  map[ssa.Instruction]map[string]struct{}
	lines []shadowLine

	compared, diff, lonly, eonly, noengine, unmapped int
	noview                                           bool
	panicked                                         string
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

// shadowOwner walks the types along a lock path to the struct that owns its last field step,
// and returns that owner, the field, and the type of the lock itself.
func shadowOwner(p lockPath) (owner *types.Named, field string, final types.Type) {
	if p.root.value == nil {
		return nil, "", nil
	}
	deref := func(t types.Type) types.Type {
		if ptr, ok := types.Unalias(t).Underlying().(*types.Pointer); ok {
			return ptr.Elem()
		}
		return t
	}
	t := p.root.value.Type()
	for _, s := range p.steps {
		switch s.kind {
		case stepDeref:
			t = deref(t)
		case stepField, stepFieldAddr:
			base := t
			if s.kind == stepFieldAddr {
				base = deref(t)
			}
			st, ok := types.Unalias(base).Underlying().(*types.Struct)
			if !ok || s.n >= st.NumFields() {
				return nil, "", nil
			}
			owner, _ = types.Unalias(base).(*types.Named)
			field = st.Field(s.n).Name()
			t = st.Field(s.n).Type()
			if s.kind == stepFieldAddr {
				t = types.NewPointer(t)
			}
		case stepIndexAddr:
			switch x := deref(t).Underlying().(type) {
			case *types.Array:
				t = types.NewPointer(x.Elem())
			case *types.Slice:
				t = types.NewPointer(x.Elem())
			default:
				return nil, "", nil
			}
		case stepIndex, stepConstIndex:
			if tup, ok := t.(*types.Tuple); ok && s.n < tup.Len() {
				t = tup.At(s.n).Type()
				continue
			}
			switch x := types.Unalias(t).Underlying().(type) {
			case *types.Array:
				t = x.Elem()
			case *types.Slice:
				t = x.Elem()
			case *types.Map:
				t = x.Elem()
			default:
				return nil, "", nil
			}
		case stepLookup:
			m, ok := types.Unalias(t).Underlying().(*types.Map)
			if !ok {
				return nil, "", nil
			}
			t = m.Elem()
		}
	}
	return owner, field, t
}

// shadowClass maps an engine lock to a class by the field rule 'F', else the type rule 'T', else 'U' for none.
func (pc *lockOrderContext) shadowClass(p lockPath) (string, byte) {
	owner, field, final := shadowOwner(p)
	if owner != nil {
		if c := pc.classForNamed(owner, field); c != "" {
			return c, 'F'
		}
	}
	if final != nil {
		if named, ok := namedOf(final); ok {
			if c := pc.classForNamed(named, ""); c != "" {
				return c, 'T'
			}
		}
	}
	return "", 'U'
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
	res, _ := pc.pass.ResultOf[Analyzer].(*ownerResult)
	if res == nil || res.shadow.e == nil {
		s.noview = true
		return
	}
	view := res.shadow
	pkg := pc.pass.Pkg.Path()
	for inst, lo := range s.held {
		pos := pc.pass.Fset.Position(inst.Pos())
		fn := inst.Parent()
		eng, ok := view.heldAt(inst)
		if !ok {
			s.noengine++
			if len(lo) > 0 {
				s.lines = append(s.lines, shadowLine{inst.Pos(), fmt.Sprintf("X\t%s\t%s\t%s\tL=%s\t%s", pkg, pos, relFuncName(fn), strings.Join(slices.Sorted(maps.Keys(lo)), ","), view.whyNone(fn))})
			}
			continue
		}
		s.compared++
		ec := make(map[string]struct{})
		origins := make(map[string][]string)
		for _, l := range eng {
			class, rule := pc.shadowClass(l.path)
			if class == "" {
				s.unmapped++
				continue
			}
			ec[class] = struct{}{}
			origins[class] = append(origins[class], fmt.Sprintf("%s/%c", l.origin, rule))
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
	fmt.Fprintf(&out, "N\t%s\tcompared=%d\tdiff=%d\tlonly=%d\teonly=%d\tnoengine=%d\tunmapped=%d", pkg, s.compared, s.diff, s.lonly, s.eonly, s.noengine, s.unmapped)
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
