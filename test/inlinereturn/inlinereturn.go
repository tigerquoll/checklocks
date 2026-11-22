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

// Package inlinereturn is the one corpus run with the wrappers on, so a report
// without a position would be output.
package inlinereturn

import "sync"

type inlineStruct struct {
	mu sync.Mutex
	// +checklocks:mu
	guardedField int
}

func inlineImplicitReturn(t *inlineStruct) {
	t.mu.Lock()
	func() {
		t.guardedField = 1
	}()
	t.mu.Unlock()
}
