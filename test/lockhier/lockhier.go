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

// Package lockhier is the corpus for the hierarchical direction check, which is the
// -lockorder.hierarchy mode of the lockorder analyzer.
//
// Queue is hierarchical, so two queues may nest; what must not happen is a child locking its
// parent. Node is deliberately not hierarchical, to keep the plain same-class rule visible
// beside it.
//
// +lockhierarchical:Queue
// +lockhierarchical:Tree
package lockhier

import "sync"

// +lockclass:Queue
type Queue struct {
	mu sync.RWMutex

	// parent is the hierarchy edge. Read without the lock on purpose: it is set at
	// creation and never changed, which is what makes the parent-first idiom possible.
	//
	// +lockhierarchyedge
	parent *Queue

	children []*Queue
	name     string
}

// +checklocksignore
func (q *Queue) Lock() { q.mu.Lock() }

// +checklocksignore
func (q *Queue) Unlock() { q.mu.Unlock() }

// +checklocksignore
func (q *Queue) RLock() { q.mu.RLock() }

// +checklocksignore
func (q *Queue) RUnlock() { q.mu.RUnlock() }

// childThenParent is the violation: the parent is taken while the child is held.
func (q *Queue) childThenParent() {
	q.Lock()
	q.parent.Lock() // +lockorderfail=parent first
	q.parent.Unlock()
	q.Unlock()
}

// childThenGrandparent walks two edges up, which is the same violation.
func (q *Queue) childThenGrandparent() {
	q.Lock()
	q.parent.parent.Lock() // +lockorderfail=parent first
	q.parent.parent.Unlock()
	q.Unlock()
}

// childThenParentDeferred holds the child through a deferred unlock, which is the shape the
// idiom actually takes.
func (q *Queue) childThenParentDeferred() {
	q.Lock()
	defer q.Unlock()
	q.parent.RLock() // +lockorderfail=parent first
	q.parent.RUnlock()
}

// childThenParentBranch reaches the acquisition through a branch, so the child is held on
// only one path into the block.
func (q *Queue) childThenParentBranch(cond bool) {
	if cond {
		q.Lock()
	}
	q.parent.Lock() // +lockorderfail=parent first
	q.parent.Unlock()
	if cond {
		q.Unlock()
	}
}

// parentThenChild is the sanctioned direction and must be silent.
func (q *Queue) parentThenChild() {
	q.Lock()
	for _, child := range q.children {
		child.Lock()
		child.Unlock()
	}
	q.Unlock()
}

// parentFirstIdiom is the shape a real queue uses: the parent is consulted with no lock held,
// and only then is this queue locked. It must be silent.
func (q *Queue) parentFirstIdiom() int {
	var fromParent int
	if q.parent != nil {
		fromParent = q.parent.parentFirstIdiom()
	}
	return q.internal(fromParent)
}

func (q *Queue) internal(fromParent int) int {
	q.RLock()
	defer q.RUnlock()
	return fromParent + len(q.children)
}

// unrelatedInstances takes two queues that are not related by the parent edge. Mere
// same-class nesting is what the hierarchical exemption permits, so this must be silent.
func unrelatedInstances(a, b *Queue) {
	a.Lock()
	b.Lock()
	b.Unlock()
	a.Unlock()
}

// releasedFirst drops the child before taking the parent, which is not a nesting at all.
func (q *Queue) releasedFirst() {
	q.Lock()
	q.Unlock()
	q.parent.Lock()
	q.parent.Unlock()
}

// siblingThroughParent reaches a sibling by going up and back down. The acquisition is not
// the parent itself, so the walk up does not match and this is silent; it is a known escape
// rather than a case the analysis clears deliberately.
func (q *Queue) siblingThroughParent() {
	q.Lock()
	for _, sibling := range q.parent.children {
		sibling.Lock()
		sibling.Unlock()
	}
	q.Unlock()
}

// +lockorderignore
func (q *Queue) ignored() {
	q.Lock()
	q.parent.Lock()
	q.parent.Unlock()
	q.Unlock()
}

// Node is not hierarchical, so two of them must not nest at all. This keeps the plain
// same-class rule visible: it is reported by the class level check, not by this one.

// +lockclass:Node
type Node struct {
	mu sync.Mutex

	// +lockhierarchyedge
	parent *Node
}

// +checklocksignore
func (n *Node) Lock() { n.mu.Lock() }

// +checklocksignore
func (n *Node) Unlock() { n.mu.Unlock() }

func (n *Node) childThenParent() {
	n.Lock()
	n.parent.Lock() // +lockorderfail=two locks of one class must not nest
	n.parent.Unlock()
	n.Unlock()
}

// Tree holds its lock as a field and is locked through the promoted method, which is the
// shape a real scheduler queue has. The receiver of the lock call is then the lock field
// rather than the instance, so the instance has to be recovered before the parent edge can
// be seen at all.

// +lockclass:Tree
type Tree struct {
	sync.RWMutex

	// +lockhierarchyedge
	up *Tree

	kids []*Tree
}

func (t *Tree) childThenParent() int {
	t.Lock()
	defer t.Unlock()
	t.up.RLock() // +lockorderfail=parent first
	defer t.up.RUnlock()
	return len(t.up.kids)
}

func (t *Tree) parentThenChild() {
	t.Lock()
	defer t.Unlock()
	for _, kid := range t.kids {
		kid.Lock()
		kid.Unlock()
	}
}

func (t *Tree) parentFirstIdiom() int {
	var fromParent int
	if t.up != nil {
		fromParent = t.up.parentFirstIdiom()
	}
	t.RLock()
	defer t.RUnlock()
	return fromParent + len(t.kids)
}
