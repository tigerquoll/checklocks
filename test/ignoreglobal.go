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
	"sync/atomic"
)

var ignoreGlobalMu sync.Mutex

// +checklocks:ignoreGlobalMu
var ignoreGlobal int

// +checkatomic
var ignoreGlobalAtomic int32

// +checklocksignore
func testIgnoreGlobal() {
	x := ignoreGlobal
	_ = x
	ignoreGlobal = 1
}

// +checklocksignore
func testIgnoreGlobalClosures(n int) {
	func() {
		ignoreGlobal = 1
	}()
	func() {
		ignoreGlobal = n
	}()
}

func testIgnoreGlobalLiteral(n int) {
	// +checklocksignore
	func() {
		ignoreGlobal = n
	}()
}

// +checklocksignore
func testIgnoreGlobalAtomic() {
	ignoreGlobalAtomic = 1
	_ = atomic.LoadInt32(&ignoreGlobalAtomic)
}

func testIgnoreGlobalUnannotated() {
	x := ignoreGlobal // +checklocksfail=invalid field access
	_ = x
	ignoreGlobal = 1 // +checklocksfail=invalid field access
}
