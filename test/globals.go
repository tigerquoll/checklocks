// Copyright 2020 The gVisor Authors.
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

var (
	globalMu   sync.Mutex
	globalRWMu sync.RWMutex
)

var globalStruct struct {
	mu sync.Mutex
	// +checklocks:mu
	guardedField int
}

var otherStruct struct {
	// +checklocks:globalMu
	guardedField1 int
	// +checklocks:globalRWMu
	guardedField2 int
	// +checklocks:globalStruct.mu
	guardedField3 int
}

// A single declaration attaches its documentation to the declaration, not to
// the specification within it. The annotation applies in either place, as it
// does for a trailing comment and for every name in the specification.

// +checklocks:globalMu
var guardedGlobal int

var guardedGlobalTrailing int // +checklocks:globalMu

// +checklocks:globalMu
var guardedGlobalFirst, guardedGlobalSecond int

// ptrGuarded is used via a pointer-typed global below. Such a variable holds a
// reference, and must be loaded before the lock is resolved.
type ptrGuarded struct {
	mu sync.Mutex
	// +checklocks:mu
	guardedField int
}

var (
	globalPtrStruct = &ptrGuarded{}
	globalPtrMu     = &sync.Mutex{}
)

var ptrOtherStruct struct {
	// +checklocks:globalPtrStruct.mu
	guardedField1 int
	// +checklocks:globalPtrMu
	guardedField2 int
}

func testGlobalValid() {
	globalMu.Lock()
	otherStruct.guardedField1 = 1
	globalMu.Unlock()

	globalRWMu.Lock()
	otherStruct.guardedField2 = 1
	globalRWMu.Unlock()

	globalRWMu.RLock()
	_ = otherStruct.guardedField2
	globalRWMu.RUnlock()

	globalStruct.mu.Lock()
	globalStruct.guardedField = 1
	otherStruct.guardedField3 = 1
	globalStruct.mu.Unlock()
}

// +checklocks:globalStruct.mu
func testGlobalValidPreconditions0() {
	globalStruct.guardedField = 1
}

// +checklocks:globalMu
func testGlobalValidPreconditions1() {
	otherStruct.guardedField1 = 1
}

// +checklocks:globalRWMu
func testGlobalValidPreconditions2() {
	otherStruct.guardedField2 = 1
}

// +checklocks:globalStruct.mu
func testGlobalValidPreconditions3() {
	otherStruct.guardedField3 = 1
}

// +checklocksexclude:globalMu
func testGlobalExcludePreconditions() {
}

func testGlobalExcludeValid() {
	testGlobalExcludePreconditions()
}

func testGlobalExcludeInvalid() {
	globalMu.Lock()
	testGlobalExcludePreconditions() // +checklocksfail
	globalMu.Unlock()
}

func testGlobalInvalid() {
	globalStruct.guardedField = 1 // +checklocksfail
	otherStruct.guardedField1 = 1 // +checklocksfail
	otherStruct.guardedField2 = 1 // +checklocksfail
	otherStruct.guardedField3 = 1 // +checklocksfail
}

func testGuardedGlobalValid() {
	globalMu.Lock()
	guardedGlobal = 1
	guardedGlobalTrailing = 1
	guardedGlobalFirst = 1
	guardedGlobalSecond = 1
	globalMu.Unlock()
}

func testGuardedGlobalInvalid() {
	guardedGlobal = 1         // +checklocksfail
	guardedGlobalTrailing = 1 // +checklocksfail
	guardedGlobalFirst = 1    // +checklocksfail
	guardedGlobalSecond = 1   // +checklocksfail
}

func testGlobalPtrValid() {
	globalPtrStruct.mu.Lock()
	globalPtrStruct.guardedField = 1
	ptrOtherStruct.guardedField1 = 1
	globalPtrStruct.mu.Unlock()

	globalPtrMu.Lock()
	ptrOtherStruct.guardedField2 = 1
	globalPtrMu.Unlock()
}

func testGlobalPtrInvalid() {
	globalPtrStruct.guardedField = 1 // +checklocksfail
	ptrOtherStruct.guardedField1 = 1 // +checklocksfail
	ptrOtherStruct.guardedField2 = 1 // +checklocksfail
}

// +checklocks:globalPtrStruct.mu
func testGlobalPtrPreconditions0() {
	globalPtrStruct.guardedField = 1
	ptrOtherStruct.guardedField1 = 1
}

// +checklocks:globalPtrMu
func testGlobalPtrPreconditions1() {
	ptrOtherStruct.guardedField2 = 1
}

func testGlobalPtrPreconditionsValid() {
	globalPtrStruct.mu.Lock()
	testGlobalPtrPreconditions0()
	globalPtrStruct.mu.Unlock()

	globalPtrMu.Lock()
	testGlobalPtrPreconditions1()
	globalPtrMu.Unlock()
}

func testGlobalPtrPreconditionsInvalid() {
	testGlobalPtrPreconditions0() // +checklocksfail
	testGlobalPtrPreconditions1() // +checklocksfail
}

// +checklocksexclude:globalPtrStruct.mu
func testGlobalPtrExcludePreconditions0() {
}

// +checklocksexclude:globalPtrMu
func testGlobalPtrExcludePreconditions1() {
}

func testGlobalPtrExcludeValid() {
	testGlobalPtrExcludePreconditions0()
	testGlobalPtrExcludePreconditions1()
}

func testGlobalPtrExcludeInvalid() {
	globalPtrStruct.mu.Lock()
	testGlobalPtrExcludePreconditions0() // +checklocksfail
	globalPtrStruct.mu.Unlock()

	globalPtrMu.Lock()
	testGlobalPtrExcludePreconditions1() // +checklocksfail
	globalPtrMu.Unlock()
}

func testCrosspkgGlobalValid() {
	crosspkg.FooMu.Lock()
	crosspkg.Foo = 1
	crosspkg.FooMu.Unlock()
}

func testCrosspkgGlobalInvalid() {
	crosspkg.Foo = 1 // +checklocksfail
}

func testCrosspkgGlobalExcludeValid() {
	crosspkg.CallFoo()
}

func testCrosspkgGlobalExcludeInvalid() {
	crosspkg.FooMu.Lock()
	crosspkg.CallFoo() // +checklocksfail
	crosspkg.FooMu.Unlock()
}

// The guard for the objects below is a package-level variable that crosspkg
// does not export, so it cannot be resolved here. It is not enforced from this
// package, and must not crash the analyzer.
func testCrosspkgUnexportedGlobalValid() {
	crosspkg.CallBar()
	crosspkg.Bar = 1
}

func testCrosspkgPtrGlobalValid() {
	crosspkg.PtrGuard.Mu.Lock()
	crosspkg.PtrGuard.Value = 1
	crosspkg.PtrGuard.Mu.Unlock()
}

func testCrosspkgPtrGlobalInvalid() {
	crosspkg.PtrGuard.Value = 1 // +checklocksfail
}

func testCrosspkgPtrGlobalExcludeValid() {
	crosspkg.CallPtrGuard()
}

func testCrosspkgPtrGlobalExcludeInvalid() {
	crosspkg.PtrGuard.Mu.Lock()
	crosspkg.CallPtrGuard() // +checklocksfail
	crosspkg.PtrGuard.Mu.Unlock()
}
