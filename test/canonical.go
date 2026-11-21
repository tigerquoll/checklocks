// Copyright 2026 The gVisor Authors.
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

type canonStruct struct {
	mu  sync.Mutex
	mu2 sync.Mutex
}

// Three return states are one finding, and the two reported are the first two in order.
func canonThreeReturnStates(t *canonStruct, x int) { // +checklocksfail=incompatible return states (first: &({param:t}.mu) exclusively, second: &({param:t}.mu2) exclusively)
	switch x {
	case 0:
		t.mu.Lock()
		return // +checklocksfail=return with unexpected locks held
	case 1:
		t.mu2.Lock()
		return // +checklocksfail=return with unexpected locks held
	}
}

// A literal is a walk of its own, so its return states are not its caller's.
func canonClosureIsItsOwnRoot(t *canonStruct, x int) {
	inner := func() { // +checklocksfail=incompatible return states (first: &({param:t}.mu) exclusively, second: &({param:t}.mu2) exclusively)
		switch x {
		case 0:
			t.mu.Lock()
			return // +checklocksfail=return with unexpected locks held
		case 1:
			t.mu2.Lock()
			return // +checklocksfail=return with unexpected locks held
		}
	}
	inner()
}

// A force on the function line suppresses the finding, as it does for one pair.
func canonForcedReturnStates(t *canonStruct, x int) { // +checklocksforce
	switch x {
	case 0:
		t.mu.Lock()
		return // +checklocksfail=return with unexpected locks held
	case 1:
		t.mu2.Lock()
		return // +checklocksfail=return with unexpected locks held
	}
}

// An ignored function reports nothing, including this.
//
// +checklocksignore
func canonIgnoredReturnStates(t *canonStruct, x int) {
	switch x {
	case 0:
		t.mu.Lock()
		return
	case 1:
		t.mu2.Lock()
		return
	}
}

type canonHint struct {
	mu    sync.Mutex
	value int // +checklocksfail=may require checklocks annotation for mu, used with lock held 100% of the time
}

// Two instances of one lock field are held, and the hint is a share of the accesses.
func canonTwoInstancesHeld(a, b *canonHint) {
	a.mu.Lock()
	b.mu.Lock()
	a.value = 1
	b.mu.Unlock()
	a.mu.Unlock()
}
