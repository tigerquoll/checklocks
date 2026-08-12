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
	"sort"
	"strings"
)

// classSet is the set of lock classes held at a point in a function.
//
// It counts rather than just records: one class can legitimately be held more than once by
// a hierarchical class, and a release has to put the count back rather than clear it. The
// analysis is per class, not per instance, so two locks of one class are indistinguishable
// here by design; that is the whole point of a class based order.
type classSet struct {
	counts map[string]int
}

func newClassSet() *classSet {
	return &classSet{counts: make(map[string]int)}
}

// fork copies the set, for the separate paths of a branch.
func (cs *classSet) fork() *classSet {
	out := &classSet{counts: make(map[string]int, len(cs.counts))}
	for k, v := range cs.counts {
		out.counts[k] = v
	}
	return out
}

// acquire records that a class has been taken.
func (cs *classSet) acquire(class string) {
	if class == "" {
		return
	}
	cs.counts[class]++
}

// release records that a class has been dropped. A release with nothing held is ignored:
// the lock was taken somewhere this analysis cannot see, and inventing a negative count
// would make every later check wrong.
func (cs *classSet) release(class string) {
	if class == "" {
		return
	}
	if cs.counts[class] > 0 {
		cs.counts[class]--
		if cs.counts[class] == 0 {
			delete(cs.counts, class)
		}
	}
}

// held returns the classes currently held, sorted so diagnostics are deterministic.
func (cs *classSet) held() []string {
	out := make([]string, 0, len(cs.counts))
	for k := range cs.counts {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// empty reports whether nothing is held.
func (cs *classSet) empty() bool { return len(cs.counts) == 0 }

// String renders the set for a diagnostic.
func (cs *classSet) String() string {
	h := cs.held()
	if len(h) == 0 {
		return "no locks held"
	}
	return strings.Join(h, ", ")
}

// merge folds another set into this one, keeping the larger count of each class.
//
// Merging at a join point is a deliberate over approximation: if a class is held on one
// path into a block it is treated as held afterwards. That errs towards reporting rather
// than towards silence, which is the right direction for an ordering check, and matches
// what the runtime checker would observe on the path that does hold it.
func (cs *classSet) merge(other *classSet) bool {
	changed := false
	for k, v := range other.counts {
		if cs.counts[k] < v {
			cs.counts[k] = v
			changed = true
		}
	}
	return changed
}
