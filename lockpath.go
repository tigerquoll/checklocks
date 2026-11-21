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
	"go/types"
	"slices"
	"strconv"

	"golang.org/x/tools/go/ssa"
)

type rootKind int

const (
	pathRootParam rootKind = iota
	pathRootGlobal
	pathRootFreeVar
	pathRootValue
)

// lockRoot is where a lock path starts.
type lockRoot struct {
	kind   rootKind
	value  ssa.Value
	object types.Object

	// name is the variable's name, or for a value root its rendered text.
	name string
}

// keyPart writes s length-prefixed, so no text can forge a boundary.
func keyPart(s string) string {
	return strconv.Itoa(len(s)) + ":" + s
}

func (r lockRoot) key() string {
	switch r.kind {
	case pathRootParam:
		if p, ok := r.value.(*ssa.Parameter); ok {
			fn := p.Parent()
			return "P" + keyPart(fn.String()) + keyPart(strconv.Itoa(slices.Index(fn.Params, p)))
		}
		return "P" + keyPart("") + keyPart(r.name)
	case pathRootGlobal:
		if g, ok := r.value.(*ssa.Global); ok {
			return "G" + keyPart(g.String())
		}
		return "G" + keyPart("") + keyPart(r.name)
	case pathRootFreeVar:
		if f, ok := r.value.(*ssa.FreeVar); ok {
			fn := f.Parent()
			return "F" + keyPart(fn.String()) + keyPart(strconv.Itoa(slices.Index(fn.FreeVars, f)))
		}
		return "F" + keyPart("") + keyPart(r.name)
	}
	return "V" + keyPart(valuePackage(r.value)) + keyPart(r.name)
}

// valuePackage is the path of the package of v's function, empty where valueToken
// does not ask for the parent.
func valuePackage(v ssa.Value) string {
	if v == nil {
		return ""
	}
	if _, ok := v.(*ssa.Const); ok {
		return ""
	}
	if inst, ok := v.(ssa.Instruction); ok && inst.Block() == nil {
		return ""
	}
	if fn := v.Parent(); fn != nil && fn.Pkg != nil {
		return fn.Pkg.Pkg.Path()
	}
	return ""
}

func (r lockRoot) render() string {
	switch r.kind {
	case pathRootParam:
		return fmt.Sprintf("{param:%s}", r.name)
	case pathRootGlobal:
		return fmt.Sprintf("{global:%s}", r.name)
	case pathRootFreeVar:
		return fmt.Sprintf("{freevar:%s}", r.name)
	}
	return r.name
}

type stepKind int

const (
	stepDeref stepKind = iota
	stepField
	stepFieldAddr
	stepIndex
	stepIndexAddr
	stepLookup
	stepConstIndex
)

// lockStep is one move from a lock path's root.
type lockStep struct {
	kind stepKind

	// field and n are set for a field step; n alone for a constant index.
	field *types.Var
	n     int

	// index is the path of the index of an index, index address or lookup step.
	index lockPath
}

func (s lockStep) key(index pathKey) string {
	switch s.kind {
	case stepDeref:
		return "D"
	case stepField:
		return "F" + keyPart(strconv.Itoa(s.n)) + keyPart(s.field.Name())
	case stepFieldAddr:
		return "A" + keyPart(strconv.Itoa(s.n)) + keyPart(s.field.Name())
	case stepIndex:
		return "I" + keyPart(string(index))
	case stepIndexAddr:
		return "X" + keyPart(string(index))
	case stepLookup:
		return "L" + keyPart(string(index))
	case stepConstIndex:
		return "C" + keyPart(strconv.Itoa(s.n))
	}
	panic(fmt.Sprintf("unknown lock path step kind %d", s.kind))
}

// render returns the step applied to base, with index the rendering of the step's index.
func (s lockStep) render(base, index string) string {
	switch s.kind {
	case stepDeref:
		return fmt.Sprintf("*(%s)", base)
	case stepField:
		return fmt.Sprintf("%s.%s", base, s.field.Name())
	case stepFieldAddr:
		return fmt.Sprintf("&(%s.%s)", base, s.field.Name())
	case stepIndex:
		return fmt.Sprintf("%s[%s]", base, index)
	case stepIndexAddr:
		return fmt.Sprintf("&(%s[%s])", base, index)
	case stepLookup:
		return fmt.Sprintf("%s[%s]", base, index)
	case stepConstIndex:
		return fmt.Sprintf("%s[%d]", base, s.n)
	}
	panic(fmt.Sprintf("unknown lock path step kind %d", s.kind))
}

// pathKey identifies a lock path: equal keys mean the same root and the same steps. It is
// never printed.
type pathKey string

// lockPath names a lock as a root and the steps taken from it. It is immutable.
type lockPath struct {
	root  lockRoot
	steps []lockStep
	str   string
	key   pathKey
}

func newLockPath(kind rootKind, v ssa.Value, obj types.Object, name string) lockPath {
	root := lockRoot{kind: kind, value: v, object: obj, name: name}
	return lockPath{root: root, str: root.render(), key: pathKey(root.key())}
}

func (p lockPath) String() string {
	return p.str
}

func (p lockPath) Key() pathKey {
	return p.key
}

// extend returns p followed by step, sharing no storage with p.
func (p lockPath) extend(step lockStep) lockPath {
	steps := make([]lockStep, len(p.steps)+1)
	copy(steps, p.steps)
	steps[len(p.steps)] = step
	return lockPath{root: p.root, steps: steps, str: step.render(p.str, step.index.str), key: p.key + pathKey(step.key(step.index.key))}
}

// fresh renders the path from its root and steps, ignoring the cached text.
func (p lockPath) fresh() string {
	s := p.root.render()
	for _, step := range p.steps {
		s = step.render(s, step.index.fresh())
	}
	return s
}

// freshKey encodes the path from its root and steps, ignoring the cached key.
func (p lockPath) freshKey() pathKey {
	k := pathKey(p.root.key())
	for _, step := range p.steps {
		k += pathKey(step.key(step.index.freshKey()))
	}
	return k
}
