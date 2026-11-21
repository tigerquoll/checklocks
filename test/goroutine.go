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

type goT struct {
	mu sync.Mutex
	// +checklocks:mu
	x int
}

// +checklocks:t.mu
func goNeeds(t *goT) {
	t.x = 1
}

func goPlain(t *goT) {}

// +checklocks:t.mu
// +checklocksignore
func goNeedsIgnored(t *goT) {
	t.x = 1
}

// +checklocksrelease:t.mu
func goReleases(t *goT) {
	t.mu.Unlock()
}

// +checklocksacquire:t.mu
func goAcquires(t *goT) {
	t.mu.Lock()
}

// +checklocksexclude:t.mu
func goExcludes(t *goT) {
	t.mu.Lock()
	t.mu.Unlock()
}

var goNeedsFn = goNeeds

func goSpawn(t *goT) {
	go goNeeds(t) // +checklocksfail=goroutine started by go goNeeds
}

func goSpawnHeld(t *goT) {
	t.mu.Lock()
	go goNeeds(t) // +checklocksfail=goroutine started by go goNeeds
	t.mu.Unlock()
}

func goSpawnNew() {
	t := new(goT)
	go goNeeds(t) // +checklocksfail=goroutine started by go goNeeds
}

func goSpawnForced(t *goT) {
	go goNeeds(t) // +checklocksforce
	t.x = 1       // +checklocksfail=invalid field access
}

// +checklocksignore
func goSpawnIgnored(t *goT) {
	go goNeeds(t)
}

func goSpawnIgnoredCallee(t *goT) {
	go goNeedsIgnored(t)
}

func goSpawnDynamic(t *goT, f func(*goT)) {
	go f(t)
	go goNeedsFn(t)
}

func goSpawnPlain(t *goT) {
	go goPlain(t)
}

func goSpawnRelease(t *goT) {
	t.mu.Lock()
	go goReleases(t) // +checklocksfail=goroutine started by go goReleases
	t.mu.Unlock()
}

func goHandOff(t *goT) {
	t.mu.Lock()
	go goReleases(t) // +checklocksfail=goroutine started by go goReleases
	return           // +checklocksfail=return with unexpected locks held
}

func goReleaseForced(t *goT) {
	t.mu.Lock()
	go goReleases(t) // +checklocksforce
	t.mu.Unlock()
}

// +checklocksignore
func goReleaseIgnored(t *goT) {
	t.mu.Lock()
	go goReleases(t)
	t.mu.Unlock()
}

func goSpawnAcquire(t *goT) {
	go goAcquires(t)
	t.x = 1 // +checklocksfail=invalid field access
}

func goSpawnExcludes(t *goT) {
	t.mu.Lock()
	go goExcludes(t)
	t.mu.Unlock()
}

func goSpawnLitHeld(t *goT) {
	t.mu.Lock()
	go func(t *goT) {
		t.x = 1 // +checklocksfail=invalid field access
	}(t)
	t.mu.Unlock()
}

type goE struct{ sync.Mutex }

func goUnlock(t *goT) {
	t.mu.Lock()
	go t.mu.Unlock() // +checklocksfail=goroutine started by go Unlock does not hold
	t.mu.Unlock()
}

func goUnlockEmbedded(e *goE) {
	e.Lock()
	go e.Unlock() // +checklocksfail=goroutine started by go Unlock does not hold
	e.Unlock()
}

func goUnlockThunk(t *goT) {
	t.mu.Lock()
	go (*sync.Mutex).Unlock(&t.mu) // +checklocksfail=goroutine started by go Unlock does not hold
	t.mu.Unlock()
}

func goUnlockLocker(l sync.Locker) {
	l.Lock()
	go l.Unlock() // +checklocksfail=goroutine started by go Unlock does not hold
	l.Unlock()
}

func goLock(t *goT) {
	go t.mu.Lock()
}

func goUnlockForced(t *goT) {
	t.mu.Lock()
	go t.mu.Unlock() // +checklocksforce
	t.mu.Unlock()
}

// +checklocksignore
func goUnlockIgnored(t *goT) {
	t.mu.Lock()
	go t.mu.Unlock()
	t.mu.Unlock()
}

// +checklocksacquire:r.mu
func goAcquiresResult() (r *goT) {
	r = new(goT)
	r.mu.Lock()
	return r
}

func goSpawnAcquireResult() {
	go goAcquiresResult() // +checklocksfail=acquired through a discarded result
}

func goSpawnAcquireResultForced() {
	go goAcquiresResult() // +checklocksforce
}

func goSpawnAcquireLit() {
	// +checklocksacquire:r.mu
	go func() (r *goT) { // +checklocksfail=acquired through a discarded result
		r = new(goT)
		r.mu.Lock()
		return r
	}()
}

func goSpawnLitNeeds(t *goT) {
	// +checklocks:u.mu
	go func(u *goT) { // +checklocksfail=goroutine started by go func literal does not hold u.mu exclusively (&({param:t}.mu))
		u.x = 1
	}(t)
}

func goSpawnLitNeedsIgnored(t *goT) {
	// +checklocks:u.mu
	// +checklocksignore
	go func(u *goT) {
		u.x = 1
	}(t)
}

func goSpawnLitNeedsForced(t *goT) {
	// +checklocks:u.mu
	go func(u *goT) { // +checklocksforce
		u.x = 1
	}(t)
}

// +checklocksignore
func goSpawnLitNeedsInIgnored(t *goT) {
	// +checklocks:u.mu
	go func(u *goT) {
		u.x = 1
	}(t)
}

// +checklockslocktype
type goPrim struct {
	gate sync.Mutex
}

// +checklocks:p.gate
func (p *goPrim) Lock() {}

// +checklocksrelease:p.gate
func (p *goPrim) Unlock() {
	p.gate.Unlock()
}

func goPrimLock(p *goPrim) {
	go p.Lock() // +checklocksfail=goroutine started by go Lock does not hold p.gate exclusively
}

func goPrimUnlock(p *goPrim) {
	p.gate.Lock()
	go p.Unlock() // +checklocksfail=goroutine started by go Unlock does not hold p.gate exclusively
	p.gate.Unlock()
}
