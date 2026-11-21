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

type igT struct {
	mu sync.Mutex
	// +checklocks:mu
	x int
	y int
}

type igCB func()

func igRegister(f func()) {}

func igRegisterCB(f igCB) {}

func igInlineIgnored(t *igT) {
	// +checklocksignore
	func(t *igT) {
		t.x = 1
	}(t)
}

func igInlinePlain(t *igT) {
	func(t *igT) {
		t.x = 1 // +checklocksfail=invalid field access
	}(t)
}

// +checklocksignore
func igRegisterIgnoredFunc(t *igT) {
	igRegister(func() {
		t.x = 1
	})
}

func igRegisterIgnoredLit(t *igT) {
	// +checklocksignore
	igRegisterCB(func() {
		t.x = 1
	})
}

func igRegisterPlain(t *igT) {
	igRegister(func() {
		t.x = 1 // +checklocksfail=invalid field access
	})
}

func igRegisterCBPlain(t *igT) {
	igRegisterCB(func() {
		t.x = 1 // +checklocksfail=invalid field access
	})
}

// +checklocksignore
func igDeferIgnored(t *igT) {
	defer func() {
		t.x = 1
	}()
	for {
	}
}

func igDeferPlain(t *igT) {
	defer func() {
		t.x = 1 // +checklocksfail=invalid field access
	}()
	for {
	}
}

func igGoIgnoredLit(t *igT) {
	// +checklocksignore
	go func() {
		t.x = 1
	}()
}

// +checklocksignore
func igGoIgnoredFunc(t *igT) {
	go func(t *igT) {
		t.x = 1
	}(t)
}

func igGoPlain(t *igT) {
	go func() {
		t.x = 1 // +checklocksfail=invalid field access
	}()
}

// +checklocksignore
func igNested(t *igT) {
	igRegister(func() {
		igRegister(func() {
			t.x = 1
		})
	})
}

func igTrailingGo(t *igT) {
	t.y = 1 // +checklocksignore
	go func() {
		t.x = 1 // +checklocksfail=invalid field access
	}()
}

func igTrailingRegister(t *igT) {
	t.y = 1 // +checklocksignore
	igRegister(func() {
		t.x = 1 // +checklocksfail=invalid field access
	})
}

func igOuterLitIgnored(t *igT) {
	// +checklocksignore
	igRegister(func() {
		igRegister(func() {
			t.x = 1
		})
	})
}

func igTrailingSelect(t *igT) {
	select {
	default: // +checklocksignore
		igRegister(func() {
			t.x = 1 // +checklocksfail=invalid field access
		})
	}
}

func igTrailingFor(t *igT) {
	for { // +checklocksignore
		igRegister(func() {
			t.x = 1 // +checklocksfail=invalid field access
		})
		break
	}
}

func igStandaloneAfterTrailing(t *igT) {
	t.y = 1 // note
	// +checklocksignore
	igRegister(func() {
		t.x = 1
	})
}

func igBlockCommentTrailing(t *igT) {
	// +checklocksignore
	/*
	 */t.y = 1
	igRegister(func() {
		t.x = 1 // +checklocksfail=invalid field access
	})
}
