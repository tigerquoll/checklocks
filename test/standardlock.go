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

package test

import "sync"

// A standard lock is judged by its own named type, never by a type argument.

type wrapperGuard[L sync.Locker] struct {
	l L
}

// +checklocksignore
func (g *wrapperGuard[L]) Lock() { g.l.Lock() }

// +checklocksignore
func (g *wrapperGuard[L]) Unlock() { g.l.Unlock() }

type rejectedWrapper struct {
	gg wrapperGuard[*sync.Mutex]
	// +checklocks:gg
	v int // +checklocksfail=field gg is not a Mutex or an RWMutex|annotation gg cannot be resolved
}

func readsRejectedWrapper(x *rejectedWrapper) int {
	return x.v
}

// +checklockslocktype
type declaredWrapper[L sync.Locker] struct {
	l L
}

func (g *declaredWrapper[L]) Lock()   { g.l.Lock() }
func (g *declaredWrapper[L]) Unlock() { g.l.Unlock() }

type declaredWrapped struct {
	dw declaredWrapper[*sync.Mutex]
	// +checklocks:dw
	v int
}

func declaredWrapperHeld(x *declaredWrapped) {
	x.dw.Lock()
	x.v = 1
	x.dw.Unlock()
}

func declaredWrapperUnheld(x *declaredWrapped) {
	x.v = 1 // +checklocksfail
}

type MutexStats struct {
	n int
}

func (m *MutexStats) Lock()   { m.n++ }
func (m *MutexStats) Unlock() { m.n++ }

type rejectedStats struct {
	ms MutexStats
	// +checklocks:ms
	v int // +checklocksfail=field ms is not a Mutex or an RWMutex|annotation ms cannot be resolved
}

func readsRejectedStats(x *rejectedStats) int {
	return x.v
}

func locksStatsTwice(m *MutexStats) {
	m.Lock()
	m.Lock()
	m.Unlock()
	m.Unlock()
}

// +checklocks:mu
type lockHolders struct {
	mu     sync.Mutex
	shards [4]sync.Mutex
	byKey  map[string]*sync.Mutex
	inner  struct{ sync.Mutex }
	l      interface {
		sync.Locker
		Name() string
	}
	n int
}

func usesLockHolders(t *lockHolders, i int) {
	t.shards[i].Lock()
	t.shards[i].Unlock()
	_ = t.byKey["a"]
	t.inner.Lock()
	t.inner.Unlock()
	t.l.Lock()
	t.l.Unlock()
	t.n = 1 // +checklocksfail
}
