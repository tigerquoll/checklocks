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
	"go/token"
	"go/types"
	"hash/fnv"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// decisionLogDir is where the decision log goes; empty writes none.
var decisionLogDir = os.Getenv("CHECKLOCKS_DECISION_LOG")

// walkOrder picks the order the walk visits successors in: empty for SSA order, "reverse", or a decimal seed.
var walkOrder = os.Getenv("CHECKLOCKS_WALK_ORDER")

// succOrder is the order the walk visits a block's successors in.
func succOrder(fn *ssa.Function, block *ssa.BasicBlock) []*ssa.BasicBlock {
	return permuteSuccs(walkOrder, fn, block)
}

// permuteSuccs returns block's successors in the given order, never changing block.Succs.
// A seed flips a per-block coin, so the same seed gives the same order in every process.
func permuteSuccs(order string, fn *ssa.Function, block *ssa.BasicBlock) []*ssa.BasicBlock {
	reversed := false
	switch order {
	case "":
	case "reverse":
		reversed = true
	default:
		seed, err := strconv.ParseUint(order, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("CHECKLOCKS_WALK_ORDER %q is not empty, \"reverse\" or a decimal seed", order))
		}
		h := fnv.New64a()
		fmt.Fprintf(h, "%d\x00%s\x00%d", seed, fn.String(), block.Index)
		reversed = h.Sum64()&1 == 1
	}
	if !reversed {
		return block.Succs
	}
	out := slices.Clone(block.Succs)
	slices.Reverse(out)
	return out
}

// parityCheck panics unless the typed path and object match the string builders'.
func parityCheck(rv *resolvedValue, ls *lockState, path lockPath, obj types.Object) {
	s, o := ls.oldValueAndObject(rv.value)
	typ := rv.value.Type()
	if rv.deref {
		s = fmt.Sprintf("*(%s)", s)
		if ptr, ok := typ.Underlying().(*types.Pointer); ok {
			typ = ptr.Elem()
		}
	}
	for _, entry := range rv.fieldList {
		s, o = oldSynthesize(entry, s, typ)
		typ = o.Type()
	}
	if fresh := path.fresh(); s != path.String() || s != fresh || o != obj {
		panic(fmt.Sprintf("lock path parity: old %q (%v), path %q, fresh %q (%v)", s, o, path.String(), fresh, obj))
	}
}

// parityCheckEntry panics unless an entry's path has the key it is held under and its cached key matches a
// fresh encoding.
func parityCheckEntry(key pathKey, info lockInfo) {
	if fresh := info.path.freshKey(); info.path.Key() != key || key != fresh {
		panic(fmt.Sprintf("lock table parity: key %q, path key %q, fresh %q", key, info.path.Key(), fresh))
	}
}

func oldSynthesize(entry fieldEntry, s string, typ types.Type) (string, types.Object) {
	switch f := entry.(type) {
	case *fieldStruct:
		field, ok := findField(typ, f.Field)
		if !ok {
			panic(fmt.Sprintf("unable to resolve field %d in %s", f.Field, typ.String()))
		}
		return fmt.Sprintf("&(%s.%s)", s, field.Name()), field
	case *fieldStructPtr:
		field, ok := findField(typ, f.Field)
		if !ok {
			panic(fmt.Sprintf("unable to resolve ptr field %d in %s", f.Field, typ.String()))
		}
		return fmt.Sprintf("*(&(%s.%s))", s, field.Name()), field
	}
	panic(fmt.Sprintf("unknown field entry %T", entry))
}

func (l *lockState) oldValueAndObject(v ssa.Value) (string, types.Object) {
	if s, ok := l.substitutions[v]; ok {
		return s.path.String(), s.obj
	}
	switch x := v.(type) {
	case *ssa.Parameter:
		v, ok := l.bound[x]
		if ok {
			return l.oldValueAndObject(v)
		}
		return fmt.Sprintf("{param:%s}", x.Name()), x.Object()
	case *ssa.Global:
		return fmt.Sprintf("{global:%s}", x.Name()), x.Object()
	case *ssa.FreeVar:
		v, ok := l.stored[x]
		if ok {
			stored, ok := l.stored[v]
			if ok {
				return l.oldValueAndObject(stored)
			}
		}
		return fmt.Sprintf("{freevar:%s}", x.Name()), nil
	case *ssa.Convert:
		return l.oldValueAndObject(x.X)
	case *ssa.ChangeType:
		return l.oldValueAndObject(x.X)
	case *ssa.UnOp:
		if x.Op != token.MUL {
			break
		}
		if fv, ok := x.X.(*ssa.FreeVar); ok {
			return l.oldValueAndObject(fv)
		}
		if _, ok := l.used[x.X]; !ok {
			v, ok := l.stored[x.X]
			if ok {
				l.used[x.X] = struct{}{}
				defer func() { delete(l.used, x.X) }()
				return l.oldValueAndObject(v)
			}
		}
		s, obj := l.oldValueAndObject(x.X)
		return fmt.Sprintf("*(%s)", s), obj
	case *ssa.Field:
		structType, ok := resolveStruct(x.X.Type())
		if !ok {
			panic(fmt.Sprintf("structType not available for struct: %#v", x.X))
		}
		fieldObj := structType.Field(x.Field)
		s, _ := l.oldValueAndObject(x.X)
		return fmt.Sprintf("%s.%s", s, fieldObj.Name()), fieldObj
	case *ssa.FieldAddr:
		structType, ok := resolveStruct(x.X.Type())
		if !ok {
			panic(fmt.Sprintf("structType not available for struct: %#v", x.X))
		}
		fieldObj := structType.Field(x.Field)
		s, _ := l.oldValueAndObject(x.X)
		return fmt.Sprintf("&(%s.%s)", s, fieldObj.Name()), fieldObj
	case *ssa.Index:
		s, _ := l.oldValueAndObject(x.X)
		i, _ := l.oldValueAndObject(x.Index)
		return fmt.Sprintf("%s[%s]", s, i), nil
	case *ssa.IndexAddr:
		s, _ := l.oldValueAndObject(x.X)
		i, _ := l.oldValueAndObject(x.Index)
		return fmt.Sprintf("&(%s[%s])", s, i), nil
	case *ssa.Lookup:
		s, _ := l.oldValueAndObject(x.X)
		i, _ := l.oldValueAndObject(x.Index)
		return fmt.Sprintf("%s[%s]", s, i), nil
	case *ssa.Extract:
		s, _ := l.oldValueAndObject(x.Tuple)
		return fmt.Sprintf("%s[%d]", s, x.Index), nil
	}
	return l.oldValueToken(v), nil
}

func (l *lockState) oldValueToken(v ssa.Value) string {
	if c, ok := v.(*ssa.Const); ok {
		if token, ok := l.consts[c]; ok {
			return token
		}
		return fmt.Sprintf("{%T:%s}", v, c.String())
	}
	if inst, ok := v.(ssa.Instruction); ok && inst.Block() == nil {
		return fmt.Sprintf("{%T:%d}", v, int(v.Pos()))
	}
	if fn := v.Parent(); fn != nil {
		return fmt.Sprintf("{%T:%s:%s}", v, relFuncName(fn), v.Name())
	}
	return fmt.Sprintf("{%T:%s}", v, v.String())
}

const replayRuns = 2

// blockCapture is what one transfer of a block produced.
type blockCapture struct {
	findings     []string
	observations []string
	decisions    []string
	state        string
}

// blockParity holds the walk's capture of a block and that of the replay run in progress.
type blockParity struct {
	walk, run blockCapture
}

func (pc *passContext) capture() *blockCapture {
	switch pc.emit {
	case emitWalk:
		return &pc.current.parity.walk
	case emitReplay:
		return &pc.current.parity.run
	}
	return nil
}

// parityFinding records a report, and panics if a check region raises one its last decision
// does not cover.
func (pc *passContext) parityFinding(pos token.Pos, fmtStr string, args ...any) {
	if pc.current != nil || pc.decisions.region > 0 {
		if last := pc.decisions.last; !last.open || last.pos != pos {
			panic(fmt.Sprintf("decision log: report without decision at %s", pc.pass.Fset.Position(pos)))
		}
		pc.decisions.last.open = false
	}
	if c := pc.capture(); c != nil {
		c.findings = append(c.findings, fmt.Sprintf("%d: %s", pos, fmt.Sprintf(fmtStr, args...)))
	}
}

func (pc *passContext) parityObservation(inst almostInst, accessObj types.Object, ls *lockState) {
	if c := pc.capture(); c != nil {
		c.observations = append(c.observations, fmt.Sprintf("%d %v %s", inst.Pos(), accessObj, ls.identitySummary(func(lockInfo) bool { return true })))
	}
}

func (p *blockParity) walked(ls *lockState) {
	p.walk.state = ls.identitySummary(func(lockInfo) bool { return true })
}

// replayed panics unless the replay run that just ended matches the walk's capture.
func (p *blockParity) replayed(r *blockRecord, ls *lockState, grew bool) {
	where := fmt.Sprintf("%s block %d", r.wc.fn.String(), r.block.Index)
	if grew {
		panic(fmt.Sprintf("block replay parity: %s: replay entered a nested walk", where))
	}
	p.run.state = ls.identitySummary(func(lockInfo) bool { return true })
	run, walk := p.run, p.walk
	p.run = blockCapture{}
	slices.Sort(run.findings)
	slices.Sort(walk.findings)
	slices.Sort(run.observations)
	slices.Sort(walk.observations)
	slices.Sort(run.decisions)
	slices.Sort(walk.decisions)
	if d := firstDifference(walk.findings, run.findings); d != "" {
		panic(fmt.Sprintf("block replay parity: %s: findings: %s", where, d))
	}
	if d := firstDifference(walk.observations, run.observations); d != "" {
		panic(fmt.Sprintf("block replay parity: %s: observations: %s", where, d))
	}
	if d := firstDifference(walk.decisions, run.decisions); d != "" {
		panic(fmt.Sprintf("block replay parity: %s: decisions: %s", where, d))
	}
	if run.state != walk.state {
		panic(fmt.Sprintf("block replay parity: %s: state: walk %q, replay %q", where, walk.state, run.state))
	}
}

// firstDifference describes the first place two sorted lists differ, or returns "".
func firstDifference(walk, run []string) string {
	for i := 0; i < max(len(walk), len(run)); i++ {
		switch {
		case i >= len(walk):
			return fmt.Sprintf("replay has extra %q", run[i])
		case i >= len(run):
			return fmt.Sprintf("replay lacks %q", walk[i])
		case walk[i] != run[i]:
			return fmt.Sprintf("walk %q, replay %q", walk[i], run[i])
		}
	}
	return ""
}

// decisionState is what the tagged build keeps about check decisions.
type decisionState struct {
	// last is the latest decision; it is open while it failed and no report has used it.
	last struct {
		pos  token.Pos
		open bool
	}

	// region is the number of checkFunction calls under way, whose reports each need a decision.
	region int

	lines map[string]struct{}

	// logged lists the lines in the order they entered lines.
	logged []string
}

func (d *decisionState) enter() { d.region++ }
func (d *decisionState) leave() { d.region-- }

// suppressed closes the last decision, whose failure is silenced and so has no report.
func (pc *passContext) suppressed() { pc.decisions.last.open = false }

// decide records the answer of one check. A walk keeps it for the block's parity; a
// replay run keeps it too and, like a direct check, logs it.
func (pc *passContext) decide(at decisionAt, kind decisionKind, ok bool, subject ...any) {
	line := pc.decisionLine(at, kind, ok, subject)
	pc.decisions.last.pos, pc.decisions.last.open = at.pos, !ok
	if c := pc.capture(); c != nil {
		c.decisions = append(c.decisions, line)
	}
	if pc.emit != emitWalk && decisionLogDir != "" {
		if pc.decisions.lines == nil {
			pc.decisions.lines = make(map[string]struct{})
		}
		if _, ok := pc.decisions.lines[line]; !ok {
			pc.decisions.lines[line] = struct{}{}
			pc.decisions.logged = append(pc.decisions.logged, line)
		}
	}
}

// decisionMark returns a point withdrawDecisions can return the log to.
func (pc *passContext) decisionMark() int { return len(pc.decisions.logged) }

// withdrawDecisions drops the lines first logged since mark.
func (pc *passContext) withdrawDecisions(mark int) {
	for _, line := range pc.decisions.logged[mark:] {
		delete(pc.decisions.lines, line)
	}
	pc.decisions.logged = pc.decisions.logged[:mark]
}

func (pc *passContext) decisionLine(at decisionAt, kind decisionKind, ok bool, subject []any) string {
	fn, coord := at.fn, at.label
	if at.inst != nil {
		fn, coord = instCoord(at.inst)
	} else if at.label == "exit" {
		coord += ":" + strconv.Itoa(at.index)
	}
	pos := "-:0:0"
	if at.pos.IsValid() {
		p := pc.pass.Fset.PositionFor(at.pos, true)
		pos = fmt.Sprintf("%s:%d:%d", filepath.Base(p.Filename), p.Line, p.Column)
	}
	parts := make([]string, len(subject))
	for i, s := range subject {
		parts[i] = fmt.Sprint(s)
	}
	return strings.Join([]string{pos, relFuncName(fn), coord, string(kind), strings.Join(parts, " "), at.context, strconv.FormatBool(ok)}, "\t")
}

// instCoord returns the function holding inst and its place in it as block and index.
func instCoord(inst ssa.Instruction) (*ssa.Function, string) {
	if w, ok := inst.(noReferrers); ok {
		inst = w.Instruction
	}
	return inst.Parent(), fmt.Sprintf("b%d.i%d", inst.Block().Index, slices.Index(inst.Block().Instrs, inst))
}

// qualifiedName names an accessed object by its package if it is a global, or by its struct
// type if it is a field, so a global and a field of one name differ.
func qualifiedName(from ssa.Value, obj types.Object) string {
	if v, ok := obj.(*types.Var); !ok || !v.IsField() {
		return obj.Pkg().Name() + "." + obj.Name()
	}
	typ := from.Type()
	if p, ok := typ.Underlying().(*types.Pointer); ok {
		typ = p.Elem()
	}
	return types.TypeString(typ, func(p *types.Package) string { return p.Name() }) + "." + obj.Name()
}

// inlineContext names a walk by the top-level function and the call sites inlined to reach it.
// A walk with no call site of its own, a top-level or MakeClosure one, is named by its function.
func inlineContext(outer *walkContext, site callSite, fn *ssa.Function) string {
	if outer == nil || site.instr == nil {
		return relFuncName(fn)
	}
	_, coord := instCoord(site.instr)
	return outer.context + ">" + coord
}

// writeDecisions writes the sorted, deduplicated decisions of this vet unit to its own file
// in the decision log directory.
func (pc *passContext) writeDecisions() {
	if decisionLogDir == "" {
		return
	}
	name := url.PathEscape(pc.pass.Pkg.Path())
	for _, f := range pc.pass.Files {
		if strings.HasSuffix(pc.pass.Fset.File(f.Pos()).Name(), "_test.go") {
			name += "@test"
			break
		}
	}
	var out strings.Builder
	for _, line := range slices.Sorted(maps.Keys(pc.decisions.lines)) {
		out.WriteString(line + "\n")
	}
	tmp, err := os.CreateTemp(decisionLogDir, name+".tmp*")
	if err == nil {
		_, err = tmp.WriteString(out.String())
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(decisionLogDir, name+".log"))
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		panic(fmt.Sprintf("decision log: %v", err))
	}
}

// recoverPanics is off so that a panic fails the parity build loudly.
var recoverPanics = false
