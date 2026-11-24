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

// Package shadow is the corpus for the shadow comparison of the engine's held locks with lockorder's.
package shadow

import "sync"

// +checklockslocktype
// +lockclass:G
type Guard struct {
	mu sync.Mutex
}

func (g *Guard) Lock() { g.mu.Lock() }

func (g *Guard) Unlock() { g.mu.Unlock() }

type guarded struct {
	g Guard
}

func other() {}

func straight(x *guarded) {
	x.g.Lock()
	other()
	x.g.Unlock()
}

func twoStates(x *guarded, c bool) {
	if c {
		x.g.Lock()
		defer x.g.Unlock()
	}
	other()
}

func inLiteral(x *guarded) {
	x.g.Lock()
	func() {
		other()
	}()
	x.g.Unlock()
}

// +checklocks:x.g
func entryOnly(x *guarded) {
	other()
}

func commaOK(m map[string]*guarded, k string) {
	v, ok := m[k]
	if !ok {
		return
	}
	v.g.Lock()
	other()
	v.g.Unlock()
}

func keep(f func()) {}

func litEscapes(x *guarded) {
	f := func() {
		x.g.Lock()
		other()
		x.g.Unlock()
	}
	keep(f)
}
