// Copyright 2026 The gVisor Authors.
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
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// A guard may name a value recovered from a parameter by a type assertion:
//
//	// +checklocks:event.Args[0].(*Application).mu
//	func(event *Event) { ... }
//
// This is the shape of a callback invoked by a library: the subject arrives as
// an interface, inside a parameter, and is recovered by asserting it. It is
// not nameable otherwise, because it exists only as a local of the body, and
// an annotation is resolved where it is written.
//
// The path is written exactly as the Go expression that recovers the value, so
// there is no second syntax to learn, and it contains no spaces, so it cannot
// collide with the separator that lets one comment carry several annotations.

// assertStep is one step of the path leading to the asserted value.
type assertStep struct {
	// Field is the field name, for a field step.
	Field string

	// Index is the constant index, for an index step.
	Index int

	// IsIndex distinguishes the two.
	IsIndex bool
}

// typeAssertGuard is a guard on a value recovered by a type assertion.
type typeAssertGuard struct {
	// Param is the name of the parameter the path starts at.
	Param string

	// Index is the parameter's position, receiver included.
	ParamIndex int

	// Steps is the path from the parameter to the asserted operand.
	Steps []assertStep

	// TypeName is the asserted type, as written.
	TypeName string

	// FieldList is the traversal from the asserted value to the lock.
	FieldList fieldList
}

// parseAssertPath splits a guard of the asserted form into its parts.
//
// It reports whether the guard is of that form at all; a guard without an
// assertion is left to the other resolvers.
func parseAssertPath(guard string) (param string, steps []assertStep, typeName string, rest []string, ok bool) {
	i := strings.Index(guard, ".(")
	if i < 0 {
		return "", nil, "", nil, false
	}
	// The asserted type runs to the matching parenthesis. It cannot nest,
	// since a type literal that contains one is not nameable here.
	j := strings.Index(guard[i:], ")")
	if j < 0 {
		return "", nil, "", nil, false
	}
	typeName = guard[i+2 : i+j]
	head := guard[:i]
	tail := strings.TrimPrefix(guard[i+j+1:], ".")

	// The head is a parameter followed by field and index steps.
	for _, part := range strings.Split(head, ".") {
		name := part
		var indices []string
		if k := strings.Index(part, "["); k >= 0 {
			name = part[:k]
			for _, seg := range strings.Split(part[k:], "[") {
				seg = strings.TrimSuffix(seg, "]")
				if seg != "" {
					indices = append(indices, seg)
				}
			}
		}
		if param == "" {
			param = name
		} else if name != "" {
			steps = append(steps, assertStep{Field: name})
		}
		for _, idx := range indices {
			n, err := strconv.Atoi(idx)
			if err != nil {
				return "", nil, "", nil, false
			}
			steps = append(steps, assertStep{Index: n, IsIndex: true})
		}
	}
	if param == "" || typeName == "" {
		return "", nil, "", nil, false
	}
	if tail != "" {
		rest = strings.Split(tail, ".")
	}
	return param, steps, typeName, rest, true
}

// resolveTypeAssertGuard builds the resolver for a guard of the asserted form.
//
// The asserted type is not resolved here: the annotation is read before the
// body is available as ssa, and the assertion in the body is what the guard is
// matched against. What is checked here is that the parameter exists and that
// the trailing path names a lock on the asserted type.
func (pc *passContext) resolveTypeAssertGuard(pos token.Pos, params []*ast.Field, guard string) (functionGuardResolver, types.Object, bool, bool) {
	param, steps, typeName, rest, ok := parseAssertPath(guard)
	if !ok {
		return nil, nil, false, false
	}

	// Locate the parameter by name, counting positions the way the ssa
	// builder does.
	index := 0
	var paramObj types.Object
	for _, field := range params {
		if len(field.Names) == 0 {
			index++
			continue
		}
		found := false
		for _, name := range field.Names {
			if name.Name == param {
				paramObj = pc.pass.TypesInfo.ObjectOf(name)
				found = true
				break
			}
			index++
		}
		if found {
			break
		}
	}
	if paramObj == nil {
		pc.maybeFail(pos, "annotation %s does not name a parameter", guard)
		return nil, nil, true, false
	}

	// The lock is a field of the asserted type, which is named in the
	// annotation rather than reachable from the parameter's type, so it is
	// resolved from the package scope.
	lockObj, fl, ok := pc.resolveAssertedLock(pos, guard, typeName, rest)
	if !ok {
		return nil, nil, true, false
	}
	return &typeAssertGuard{
		Param:      param,
		ParamIndex: index,
		Steps:      steps,
		TypeName:   typeName,
		FieldList:  fl,
	}, lockObj, true, true
}

// resolveAssertedLock finds the lock named by the trailing path on the
// asserted type.
func (pc *passContext) resolveAssertedLock(pos token.Pos, guard, typeName string, rest []string) (types.Object, fieldList, bool) {
	bare := strings.TrimPrefix(typeName, "*")
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	obj := pc.pass.Pkg.Scope().Lookup(bare)
	if obj == nil {
		// The type is not declared here. The assertion in the body is
		// still matched, but the lock cannot be validated, so the
		// annotation is refused rather than silently doing nothing.
		pc.maybeFail(pos, "annotation %s asserts a type that is not declared in this package", guard)
		return nil, nil, false
	}
	structType, ok := resolveStruct(obj.Type())
	if !ok {
		pc.maybeFail(pos, "annotation %s asserts a type that is not a struct", guard)
		return nil, nil, false
	}
	if len(rest) == 0 {
		pc.maybeFail(pos, "annotation %s does not name a lock on the asserted type", guard)
		return nil, nil, false
	}
	fl, _, objs, ok := pc.resolveFieldListParts(pos, structType, rest)
	if !ok {
		return nil, nil, false
	}
	lockObj := objs[len(objs)-1]
	if pc.lockKind(pos, lockObj) == nil {
		return nil, nil, false
	}
	return lockObj, fl, true
}

// resolveStatic implements functionGuardResolver.resolveStatic.
//
// The guard is matched against the assertions the body performs. Every
// assertion of the named type on the named path is bound: the first is the
// value the lock is recorded against, and any others are aliased to it, so
// that a body which recovers its subject on more than one path is covered
// while the lock is still counted once.
func (g *typeAssertGuard) resolveStatic(pc *passContext, ls *lockState, fn *ssa.Function, _ any) resolvedValue {
	matches := g.matchingAsserts(fn)
	if len(matches) == 0 {
		// The body performs no assertion the guard describes, so there
		// is nothing to record it against. This is not reported here:
		// the accesses the annotation was meant to cover are reported
		// in their own right, which says the same thing at the place
		// the reader needs it.
		return makeUnavailableValue()
	}
	first := makeResolvedValue(matches[0], g.FieldList)
	for _, other := range matches[1:] {
		ls.addAlias(first, makeResolvedValue(other, g.FieldList))
	}
	return first
}

// resolveCall implements functionGuardResolver.resolveCall.
//
// A guard of this form describes what the body of a literal may assume, not a
// precondition a caller can be checked against: the caller of a callback is
// the library that invokes it. There is nothing to resolve at a call site.
func (g *typeAssertGuard) resolveCall(_ *passContext, _ *lockState, _ []ssa.Value, _ ssa.Value) resolvedValue {
	return makeUnavailableValue()
}

// matchingAsserts returns the results of the assertions this guard describes.
func (g *typeAssertGuard) matchingAsserts(fn *ssa.Function) []ssa.Value {
	if g.ParamIndex >= len(fn.Params) {
		return nil
	}
	param := fn.Params[g.ParamIndex]
	var out []ssa.Value
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			ta, ok := inst.(*ssa.TypeAssert)
			if !ok {
				continue
			}
			if !typeNameMatches(ta.AssertedType, g.TypeName) {
				// An assertion to some other type on the same
				// value is a different subject and is left
				// unbound.
				continue
			}
			if !tracesToParam(ta.X, param, g.Steps) {
				continue
			}
			if ta.CommaOk {
				// The result is a tuple; the value is its
				// first element.
				if v := extractOf(ta, 0); v != nil {
					out = append(out, v)
				}
				continue
			}
			out = append(out, ta)
		}
	}
	return out
}

// extractOf returns the extraction of one element of a tuple value.
func extractOf(v ssa.Value, index int) ssa.Value {
	refs := v.Referrers()
	if refs == nil {
		return nil
	}
	for _, inst := range *refs {
		if x, ok := inst.(*ssa.Extract); ok && x.Tuple == v && x.Index == index {
			return x
		}
	}
	return nil
}

// tracesToParam reports whether v is reached from the parameter by the given
// steps.
func tracesToParam(v ssa.Value, param *ssa.Parameter, steps []assertStep) bool {
	var seen []assertStep
	cur := v
	for i := 0; i < maxAssertSteps; i++ {
		switch x := unwrapAssertValue(cur).(type) {
		case *ssa.Parameter:
			if x != param {
				return false
			}
			// The steps were collected from the value back to the
			// parameter, so compare them reversed.
			if len(seen) != len(steps) {
				return false
			}
			for j, s := range steps {
				if seen[len(seen)-1-j] != s {
					return false
				}
			}
			return true
		case *ssa.FieldAddr:
			structType, ok := resolveStruct(x.X.Type())
			if !ok || x.Field >= structType.NumFields() {
				return false
			}
			seen = append(seen, assertStep{Field: structType.Field(x.Field).Name()})
			cur = x.X
		case *ssa.Field:
			structType, ok := resolveStruct(x.X.Type())
			if !ok || x.Field >= structType.NumFields() {
				return false
			}
			seen = append(seen, assertStep{Field: structType.Field(x.Field).Name()})
			cur = x.X
		case *ssa.IndexAddr:
			n, ok := constIndex(x.Index)
			if !ok {
				return false
			}
			seen = append(seen, assertStep{Index: n, IsIndex: true})
			cur = x.X
		case *ssa.Index:
			n, ok := constIndex(x.Index)
			if !ok {
				return false
			}
			seen = append(seen, assertStep{Index: n, IsIndex: true})
			cur = x.X
		default:
			return false
		}
	}
	return false
}

// maxAssertSteps bounds the walk back to the parameter.
const maxAssertSteps = 32

// unwrapAssertValue strips the operations that do not change which value is
// being referred to.
func unwrapAssertValue(v ssa.Value) ssa.Value {
	for {
		switch x := v.(type) {
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.MakeInterface:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return v
			}
			v = x.X
		default:
			return v
		}
	}
}

// constIndex returns the value of a constant index.
func constIndex(v ssa.Value) (int, bool) {
	c, ok := v.(*ssa.Const)
	if !ok || c.Value == nil {
		return 0, false
	}
	n, err := strconv.Atoi(c.Value.String())
	if err != nil {
		return 0, false
	}
	return n, true
}

// typeNameMatches reports whether a type is the one the annotation names.
//
// The annotation may write the type as it appears in the source, with or
// without a package qualifier, so several renderings are accepted.
func typeNameMatches(t types.Type, want string) bool {
	want = strings.TrimSpace(want)
	if t.String() == want {
		return true
	}
	// With the package name rather than its path.
	short := types.TypeString(t, func(p *types.Package) string { return p.Name() })
	if short == want {
		return true
	}
	// With no qualifier at all.
	bare := types.TypeString(t, func(*types.Package) string { return "" })
	return strings.ReplaceAll(bare, ".", "") == strings.ReplaceAll(want, ".", "")
}

// String renders the guard for diagnostics.
func (g *typeAssertGuard) String() string {
	var sb strings.Builder
	sb.WriteString(g.Param)
	for _, s := range g.Steps {
		if s.IsIndex {
			fmt.Fprintf(&sb, "[%d]", s.Index)
			continue
		}
		sb.WriteString("." + s.Field)
	}
	fmt.Fprintf(&sb, ".(%s)", g.TypeName)
	return sb.String()
}
