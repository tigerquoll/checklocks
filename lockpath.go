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

// lockPath names a lock as a root and the steps taken from it. It is immutable.
type lockPath struct {
	root  lockRoot
	steps []lockStep
	str   string
}

func newLockPath(kind rootKind, v ssa.Value, obj types.Object, name string) lockPath {
	root := lockRoot{kind: kind, value: v, object: obj, name: name}
	return lockPath{root: root, str: root.render()}
}

func (p lockPath) String() string {
	return p.str
}

// extend returns p followed by step, sharing no storage with p.
func (p lockPath) extend(step lockStep) lockPath {
	steps := make([]lockStep, len(p.steps)+1)
	copy(steps, p.steps)
	steps[len(p.steps)] = step
	return lockPath{root: p.root, steps: steps, str: step.render(p.str, step.index.str)}
}

// fresh renders the path from its root and steps, ignoring the cached text.
func (p lockPath) fresh() string {
	s := p.root.render()
	for _, step := range p.steps {
		s = step.render(s, step.index.fresh())
	}
	return s
}
