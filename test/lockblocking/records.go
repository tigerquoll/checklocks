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

package lockblocking

import (
	"sync"
	"time"
)

// +lockclass:ZZOther
type zzOther struct{ mu sync.Mutex }

// A deferred call that runs at two returns with different locks held.
func zzTwoReturns(a *App, o *zzOther, c bool) { // +checklocksfail=incompatible return states
	defer time.Sleep(time.Millisecond) // +lockblockingfail=while holding App:|while holding ZZOther:
	if c {
		a.mu.Lock()
		return // +checklocksfail=return with unexpected locks held
	}
	o.mu.Lock()
} // +checklocksfail=return with unexpected locks held

// A go literal's own blocking while it holds a lock.
func zzGoLiteralWaits(a *App, c chan int) {
	go func() {
		a.mu.Lock()
		<-c // +lockblockingfail=a channel receive
		a.mu.Unlock()
	}()
}

// A literal called inline that blocks while the caller holds a lock.
func zzInlineLiteralWaits(a *App, c chan int) {
	a.mu.Lock()
	func() {
		<-c // +lockblockingfail=a channel receive
	}()
	a.mu.Unlock()
}

// Pins that a literal started with go is not covered by its function's lockblocking ignore.
//
// +lockblockingignore
func zzIgnoredGoLiteralWaits(a *App, c chan int) {
	go func() {
		a.mu.Lock()
		<-c // +lockblockingfail=a channel receive
		a.mu.Unlock()
	}()
}

// A go literal whose deferred wait runs under a lock when the parameter says so.
func zzGoLiteralDefersWait(a *App, wg *sync.WaitGroup, c bool) {
	go func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if c {
			defer wg.Wait() // +lockblockingfail=while holding App
		}
	}()
}

type zzSeven struct {
	a, b, c, d, e, f, g sync.Mutex
}

// Pins for the flip: a function the engine gave up in waits under a lock taken before the loop.
func zzGiveUpBlocks(f *zzSeven, a *App, wg *sync.WaitGroup, c func() bool) { // +checklocksfail=checklocks gave up on this function +lockorderfail=lockorder skipped this function +lockblockingfail=lockblocking skipped this function
	a.Lock()
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
	wg.Wait()
	a.Unlock()
}

func zzGiveUpWaits(f *zzSeven, wg *sync.WaitGroup, c func() bool) { // +checklocksfail=checklocks gave up on this function +lockorderfail=lockorder skipped this function +lockblockingfail=lockblocking skipped this function
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
	wg.Wait()
}

// Pins for the flip: the exported summary of a given-up function that waits, seen from a caller holding App.
func zzCallsGiveUpBlocks(f *zzSeven, a *App, wg *sync.WaitGroup, c func() bool) {
	a.Lock()
	zzGiveUpWaits(f, wg, c)
	a.Unlock()
}
