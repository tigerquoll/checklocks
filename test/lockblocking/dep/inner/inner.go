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

// Package inner is a dependency of dep, for a path that crosses two package boundaries.
package inner

import (
	"sync"
	"time"
)

// Inner carries a class of its own, so what this package releases is visible to a caller.
//
// +lockclass:Inner
type Inner struct {
	mu sync.Mutex
}

// SendsThenSleeps sends with its caller's lock held and sleeps with it released.
//
// +checklocks:i.mu
func (i *Inner) SendsThenSleeps(c chan int) {
	c <- 1
	i.mu.Unlock()
	time.Sleep(time.Millisecond)
	i.mu.Lock()
}
