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

package checklocks

import (
	"slices"
	"strings"
	"testing"
)

// TestLockStateString pins the text of a lock state, which a map order would vary.
func TestLockStateString(t *testing.T) {
	keys := []string{
		"&({param:t}.mu)",
		"&({param:t}.inner.mu)",
		"&({global:Mu})",
		"&({*ssa.Alloc:(*T).m:t0}.mu)",
		"&({*ssa.Call:(*T).m:t4}.mu)",
		"&({*ssa.Phi:(*T).m:t9}.mu)",
		"&(&({param:t}.mus)[{*ssa.Const:(*T).m:k0}])",
		"&(&({param:t}.mus)[{*ssa.Const:(*T).m:k1}])",
		"*(&({param:t}.ptr.mu))",
		"&({freevar:fv}.mu)",
	}
	stateOf := func(order []string) string {
		ls := newLockState()
		for _, key := range order {
			// The mode is the key's, not the insertion order's.
			ls.lockedMutexes[key] = lockInfo{exclusive: len(key)%2 == 0}
		}
		return ls.String()
	}
	reversed := slices.Clone(keys)
	slices.Reverse(reversed)
	forward, backward := stateOf(keys), stateOf(reversed)
	if forward != backward {
		t.Errorf("two insertion orders print differently:\n%s\n%s", forward, backward)
	}
	printed := strings.Split(forward, ",")
	if !slices.IsSorted(printed) {
		t.Errorf("the locks are not printed in order: %s", forward)
	}
	if len(printed) != len(keys) {
		t.Errorf("printed %d locks, want %d", len(printed), len(keys))
	}
}
