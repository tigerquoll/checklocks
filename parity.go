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
	"slices"

	"golang.org/x/tools/go/ssa"
)

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

func (pc *passContext) parityFinding(pos token.Pos, fmtStr string, args ...any) {
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
	if d := firstDifference(walk.findings, run.findings); d != "" {
		panic(fmt.Sprintf("block replay parity: %s: findings: %s", where, d))
	}
	if d := firstDifference(walk.observations, run.observations); d != "" {
		panic(fmt.Sprintf("block replay parity: %s: observations: %s", where, d))
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
