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
