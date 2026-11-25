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

// Package wrapperwarning is the corpus for the warning on a lock method that only its class marks as one.
package wrapperwarning

import (
	"sync"

	"github.com/tigerquoll/checklocks/test/wrapperwarning/dep"
)

// Plain forwards with no acquire or release annotation.
//
// +lockclass:Plain
type Plain struct {
	mu sync.RWMutex
}

// +checklocksignore
func (p *Plain) Lock() { p.mu.Lock() } // +lockorderfail=checklocksacquire annotation: lockorder and lockblocking do not see the lock it takes +lockblockingfail=checklocksacquire annotation: lockorder and lockblocking do not see the lock it takes

// +checklocksignore
func (p *Plain) RUnlock() { p.mu.RUnlock() } // +lockorderfail=checklocksreleaseread annotation: lockorder and lockblocking do not see the lock it drops +lockblockingfail=checklocksreleaseread annotation: lockorder and lockblocking do not see the lock it drops

// +checklocksignore
func (p *Plain) DowngradeLock() {}

func plainDowngrade(p *Plain) {
	p.DowngradeLock()
}

func plainFirst(p *Plain) {
	p.Lock()
	p.RUnlock()
}

func plainSecond(p *Plain) {
	p.Lock()
	p.RUnlock()
}

// Tagged carries every annotation the warning asks for.
//
// +lockclass:Tagged
type Tagged struct {
	mu sync.RWMutex
}

// +checklocksignore +checklocksacquire:t.mu
func (t *Tagged) Lock() { t.mu.Lock() }

// +checklocksignore +checklocksrelease:t.mu
func (t *Tagged) Unlock() { t.mu.Unlock() }

// +checklocksignore +checklocksacquireread:t.mu
func (t *Tagged) RLock() { t.mu.RLock() }

// +checklocksignore +checklocksreleaseread:t.mu
func (t *Tagged) RUnlock() { t.mu.RUnlock() }

func taggedUse(t *Tagged) {
	t.Lock()
	t.Unlock()
	t.RLock()
	t.RUnlock()
}

// Both guards the lock on entry and on exit, which is neither an acquire nor a release.
//
// +lockclass:Both
type Both struct {
	mu sync.Mutex
}

// +checklocksignore +checklocksacquire:b.mu
func (b *Both) Lock() { b.mu.Lock() }

// +checklocks:b.mu +checklocksignore
func (b *Both) Unlock() { b.mu.Unlock() } // +lockorderfail=checklocksrelease annotation: lockorder and lockblocking do not see the lock it drops +lockblockingfail=checklocksrelease annotation: lockorder and lockblocking do not see the lock it drops +lockgapignore

// +checklocks:b.mu
func holdBoth(b *Both) {
	b.Unlock()
}

// Free has no class, and a standard lock needs no annotation.
type Free struct {
	mu sync.Mutex
}

// +checklocksignore
func (f *Free) Lock() { f.mu.Lock() }

// +checklocksignore
func (f *Free) Unlock() { f.mu.Unlock() }

func freeUse(f *Free) {
	f.Lock()
	f.Unlock()
}

func classedStandard(p *Plain) {
	p.mu.Lock()
	p.mu.Unlock()
}

func crossBranch(b *dep.Bare, cond bool) {
	if cond {
		b.Lock() // +lockorderfail=checklocksacquire annotation: lockorder and lockblocking do not see the lock it takes +lockblockingfail=checklocksacquire annotation: lockorder and lockblocking do not see the lock it takes
	} else {
		b.Lock()
	}
	b.Unlock()
}

func crossFirst(b *dep.Bare) {
	b.Lock()
	b.Unlock()
}

func crossSecond(b *dep.Bare) {
	b.Lock()
	b.Unlock()
}

func crossTagged(t *dep.Tagged) {
	t.Lock()
	t.Unlock()
}
