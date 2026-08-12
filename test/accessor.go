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

import (
	"sync"

	"github.com/tigerquoll/checklocks/test/crosspkg"
)

type accessorGuarded struct {
	mu sync.Mutex
	// +checklocks:mu
	guardedField int
}

var accessorGlobal = &accessorGuarded{}

// getAccessorGlobal does nothing but return the package-level variable, so a
// lock reached through its result is the variable's lock.
func getAccessorGlobal() *accessorGuarded { return accessorGlobal }

func testAccessorValid() {
	d := getAccessorGlobal()
	d.mu.Lock()
	d.guardedField = 1
	// The same lock, named directly rather than through the accessor.
	accessorGlobal.guardedField = 2
	d.mu.Unlock()
}

func testAccessorInvalid() {
	d := getAccessorGlobal()
	d.guardedField = 1 // +checklocksfail
}

// Locking through the accessor satisfies an annotation naming the variable.

// +checklocks:accessorGlobal.mu
func testAccessorPreconditions() {
	accessorGlobal.guardedField = 1
}

func testAccessorPreconditionsValid() {
	d := getAccessorGlobal()
	d.mu.Lock()
	testAccessorPreconditions()
	d.mu.Unlock()
}

func testAccessorPreconditionsInvalid() {
	testAccessorPreconditions() // +checklocksfail
}

// +checklocksexclude:accessorGlobal.mu
func testAccessorExcludePreconditions() {
}

func testAccessorExcludeValid() {
	testAccessorExcludePreconditions()
}

func testAccessorExcludeInvalid() {
	d := getAccessorGlobal()
	d.mu.Lock()
	testAccessorExcludePreconditions() // +checklocksfail
	d.mu.Unlock()
}

// A lazily initialised singleton: the accessor runs an initialiser first and
// returns the same variable either way.
var (
	accessorOnce sync.Once
	accessorLazy *accessorGuarded
)

func getAccessorLazy() *accessorGuarded {
	accessorOnce.Do(func() { accessorLazy = &accessorGuarded{} })
	return accessorLazy
}

func testAccessorLazyValid() {
	d := getAccessorLazy()
	d.mu.Lock()
	d.guardedField = 1
	accessorLazy.guardedField = 2
	d.mu.Unlock()
}

func testAccessorLazyInvalid() {
	d := getAccessorLazy()
	d.guardedField = 1 // +checklocksfail
}

// An accessor that may return either of two variables is not substituted: its
// result is not always the same variable. Holding one variable's lock
// therefore does not cover a field reached through it.
var accessorAlt = &accessorGuarded{}

func getConditionalAccessor(useAlt bool) *accessorGuarded {
	if useAlt {
		return accessorAlt
	}
	return accessorGlobal
}

func testConditionalAccessorNotUnified() {
	accessorGlobal.mu.Lock()
	d := getConditionalAccessor(false)
	d.guardedField = 1 // +checklocksfail
	accessorGlobal.mu.Unlock()
}

// Cross-package: the fact travels with the accessor.

func testCrosspkgAccessorValid() {
	d := crosspkg.GetAccessorGlobal()
	d.Mu.Lock()
	d.Value = 1
	crosspkg.AccessorGlobal.Value = 2
	d.Mu.Unlock()
}

func testCrosspkgAccessorInvalid() {
	d := crosspkg.GetAccessorGlobal()
	d.Value = 1 // +checklocksfail
}

func testCrosspkgAccessorExcludeValid() {
	crosspkg.CallAccessorExcluded()
}

func testCrosspkgAccessorExcludeInvalid() {
	d := crosspkg.GetAccessorGlobal()
	d.Mu.Lock()
	crosspkg.CallAccessorExcluded() // +checklocksfail
	d.Mu.Unlock()
}
