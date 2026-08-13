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

// Package fresh is the corpus for objects under construction.
//
// The shapes here are taken from a real scheduler, because the point of the two annotations
// is that a constructor and the thing it hands its object to are usually two functions in
// two places, and neither of them can see the whole story on its own.
package fresh

import "sync"

// classLock is a lock that carries something of its own, so that a constructor has a reason
// to call a method ON THE LOCK. That call passes a pointer to the lock field, not to the
// object holding it, which is what keeps the object unpublished across it.
//
// +checklockslocktype
type classLock struct {
	mu    sync.Mutex
	class int
}

func (l *classLock) Lock()   { l.mu.Lock() }
func (l *classLock) Unlock() { l.mu.Unlock() }

// setClass writes the lock's own field, and does not let the lock escape.
func (l *classLock) setClass(c int) { l.class = c }

// Queue is the object under construction.
type Queue struct {
	lock classLock

	// +checklocks:lock
	name string

	// +checklocks:lock
	managed bool

	// +checklocks:lock
	children map[string]*Queue

	// +checklocks:lock
	template string
}

// registry is somewhere published objects can be reached from.
var (
	registryLock sync.Mutex
	// +checklocks:registryLock
	registry = map[string]*Queue{}
)

// --- a constructor that keeps its promise -----------------------------------------------

// newQueue is the shape every constructor in the code base has: allocate, register the lock
// class, write the fields, hand it back. None of the writes hold the lock, and none of them
// need to: nothing else can reach the queue yet.
//
// +checklocksreturnsfresh
func newQueue(name string) *Queue {
	q := &Queue{children: make(map[string]*Queue)}
	q.lock.setClass(1)
	q.name = name
	q.managed = true
	return q
}

// The result of a constructor that declares it is fresh is fresh in its caller too, which is
// what the parameter-first design could not express: the object never was an allocation
// here, it arrived from somewhere else.
func buildOne(name string) *Queue {
	q := newQueue(name)
	q.template = "from the caller"
	return q
}

// An object that was not allocated here and did not come from such a constructor is not
// fresh, and its guards apply as usual.
func writeToSomeoneElses(q *Queue) {
	q.name = "no" // +checklocksfail=invalid field access
}

// --- the constructor's promise is checked -------------------------------------------------

// A function that returns something it did not allocate does not return a fresh object,
// whatever it says.
//
// +checklocksreturnsfresh
func passThrough(q *Queue) *Queue {
	return q // +checklocksfail=returns an object that is not fresh
}

// Nor does one that publishes the object before handing it over.
//
// +checklocksreturnsfresh
func newAndRegister(name string) *Queue {
	q := newQueue(name)
	registryLock.Lock()
	registry[name] = q
	registryLock.Unlock()
	return q // +checklocksfail=returns an object that is not fresh
}

// --- freshness ends where the object is published -------------------------------------------

func publishThenWrite(name string) {
	q := newQueue(name)
	registryLock.Lock()
	registry[name] = q
	registryLock.Unlock()
	q.name = "too late" // +checklocksfail=invalid field access
}

func escapeIntoAGoroutine(name string) {
	q := newQueue(name)
	go func() {
		q.lock.Lock()
		q.name = "other goroutine"
		q.lock.Unlock()
	}()
	q.managed = false // +checklocksfail=invalid field access
}

func escapeIntoAChannel(name string, ch chan *Queue) {
	q := newQueue(name)
	ch <- q
	q.managed = false // +checklocksfail=invalid field access
}

// Taking the object's own lock does not publish it: the call is given the address of the
// lock, and there is no way from a field back to what contains it. Every constructor does
// this, so a rule that ended freshness here would leave nothing to elide.
func lockingDoesNotPublish(name string) {
	q := newQueue(name)
	q.lock.Lock()
	q.name = "still mine"
	q.lock.Unlock()
	q.managed = true
}

// --- a parameter that must arrive unpublished ------------------------------------------------

// addChild is the second half: the caller promises the child is unpublished, and this reads
// and writes it without its lock on the strength of that.
//
// The insert publishes the child into a map this function holds the lock of, so nothing can
// traverse to it for the rest of the critical section, and the reads below it are as safe as
// the ones above.
//
// +checklocksfresh:child
func (q *Queue) addChild(child *Queue) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.children[child.name] = child
	if !child.managed {
		child.template = q.template
	}
}

// A fresh object may be passed to such a parameter.
func buildAndAdd(parent *Queue, name string) {
	child := newQueue(name)
	parent.addChild(child)
}

// And a fresh parameter may be passed on to another one: the promise chains.
//
// +checklocksfresh:child
func addToBoth(a, b *Queue, child *Queue) {
	a.addChild(child)
}

// --- the call site is where a broken promise is caught -----------------------------------------

func publishThenAdd(parent *Queue, name string) {
	child := newQueue(name)
	registryLock.Lock()
	registry[name] = child
	registryLock.Unlock()
	parent.addChild(child) // +checklocksfail=requires an unpublished object
}

func addSomeoneElses(parent *Queue, child *Queue) {
	parent.addChild(child) // +checklocksfail=requires an unpublished object
}

// --- what the callee does to it carries back to the caller ---------------------------------------

// addChild puts the child in a map and then releases the lock, so by the time it returns the
// child is reachable. What the caller does next is not construction any more, and this is the
// asymmetry the protected publication rule turns on: safe inside that critical section, not
// safe after it.
func addThenWrite(parent *Queue, name string) {
	child := newQueue(name)
	parent.addChild(child)
	child.managed = true // +checklocksfail=invalid field access
}

// A callee that does NOT publish leaves the object fresh. It locks, which is what an
// ordinary method does, and locking is not publishing.
func describe(q *Queue) int {
	q.lock.Lock()
	defer q.lock.Unlock()
	return len(q.name)
}

func buildDescribeWrite(name string) {
	q := newQueue(name)
	_ = describe(q)
	q.managed = true
}
