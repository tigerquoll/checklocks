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

// Package lockblockingscope is the corpus for a dependency that is out of scope.
package lockblockingscope

import (
	"github.com/tigerquoll/checklocks/test/lockblockingscope/dep"
)

// holder owns the dependency's lock, so the callee acts on this receiver.
type holder struct {
	s dep.Scoped
}

// The only wait that crosses the boundary released the class reported, so no path is named.
func (h *holder) waitsOutOfScope(c chan int) {
	h.s.Lock()
	h.s.SleepsThenSends(c) // +lockblockingfail=which may block while holding Scoped
	h.s.Unlock()
}

// The same wait one call further away, where the dependency call is a hop and not the site.
func (h *holder) sleepsOutOfScope(c chan int) {
	h.s.SleepsThenSends(c) // +checklocksfail=must hold s.mu exclusively
}

func (h *holder) callsSleepsOutOfScope(c chan int) {
	h.s.Lock()
	h.sleepsOutOfScope(c) // +lockblockingfail=calling (*holder).sleepsOutOfScope, which may block while holding Scoped
	h.s.Unlock()
}
