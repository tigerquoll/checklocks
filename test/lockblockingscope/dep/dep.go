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

// Package dep is the out of scope dependency of the lockblockingscope corpus.
package dep

import (
	"sync"
	"time"
)

// Scoped carries a class, so a caller of this package holds one this analysis can name.
//
// +lockclass:Scoped
type Scoped struct {
	mu sync.Mutex
}

func (s *Scoped) Lock() { s.mu.Lock() }

func (s *Scoped) Unlock() { s.mu.Unlock() }

// SleepsThenSends sleeps with its caller's lock dropped and sends with it held.
//
// +checklocks:s.mu
func (s *Scoped) SleepsThenSends(c chan int) {
	s.mu.Unlock()
	time.Sleep(time.Millisecond)
	s.mu.Lock()
	c <- 1
}
