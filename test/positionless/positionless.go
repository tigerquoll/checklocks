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

// Package positionless holds code checklocks must not report without a source position:
// a function's recover block and the escaping bound wrapper of a lock method value.
package positionless

import "sync"

type D struct {
	mu    sync.Mutex
	other sync.Mutex
	rw    sync.RWMutex
}

func noop() {}

// +checklocksacquire:d.mu
func recoverAcquire(d *D) {
	defer noop()
	d.mu.Lock()
}

// +checklocksacquire:d.mu
// +checklocksacquire:d.other
func recoverAcquireTwo(d *D) {
	defer noop()
	d.mu.Lock()
	d.other.Lock()
}

// +checklocksacquireread:d.rw
func recoverAcquireRead(d *D) {
	defer noop()
	d.rw.RLock()
}

// +checklocksrelease:d.mu
func recoverRelease(d *D) {
	defer noop()
	d.mu.Unlock()
}

// +checklocksacquire:d.mu
func recoverNamed() (d *D) {
	defer noop()
	d.mu.Lock()
	return d
}

func boundUnlock(mu *sync.Mutex) func() {
	mu.Lock()
	return mu.Unlock // +checklocksfail=return with unexpected locks held
}

func boundPhi(mu *sync.RWMutex, c bool) func() { // +checklocksfail=incompatible return states
	f := mu.Unlock
	if c {
		mu.Lock()
	} else {
		mu.RLock()
		f = mu.RUnlock
	}
	return f // +checklocksfail={param:mu} exclusively)|{param:mu} non-exclusively)
}

type W struct {
	mu   sync.RWMutex
	stop func()
}

func boundRLock(w *W) {
	w.stop = w.mu.RLock
}

func boundAny(mu *sync.Mutex) any {
	return any(mu.Lock)
}

func boundLocker(l sync.Locker) func() {
	return l.Unlock
}
