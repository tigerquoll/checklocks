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

//go:build checklocks_parity

package checklocks

import "testing"

// TestDeferUnknownCount checks an inexact unwind in replay counts once, however often it is replayed, and an
// exact one not at all.
func TestDeferUnknownCount(t *testing.T) {
	src := unwindSource()
	for _, tc := range []struct {
		fn   string
		want int
	}{{"once", 0}, {"twice", 1}} {
		u := newUnwindFixture(t, src, tc.fn)
		u.run(pushInOrder)
		u.run(pushInOrder)
		if got := u.pc.stats.deferUnknown; got != tc.want {
			t.Errorf("%s: deferUnknown %d, want %d", tc.fn, got, tc.want)
		}
	}
}
