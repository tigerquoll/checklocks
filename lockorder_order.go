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
	"fmt"
	"sort"
)

// order is the declared partial order over lock classes, closed transitively.
//
// A pair that the closure does not relate is not checked: the taxonomy says nothing about
// it, and guessing would only produce noise.
type order struct {
	// precedes[a][b] means a must be acquired before b, so acquiring a while b is held is
	// a violation.
	precedes map[string]map[string]bool

	// hierarchical holds the classes whose locks may nest with each other by design.
	hierarchical map[string]bool

	// withheld holds the classes whose same class rule is not enforced yet.
	withheld map[string]bool

	// edges is the declared set, kept for diagnostics and for the cycle report.
	edges []orderEdge
}

func newOrder() *order {
	return &order{
		precedes:     make(map[string]map[string]bool),
		hierarchical: make(map[string]bool),
		withheld:     make(map[string]bool),
	}
}

// addEdge records a declared edge.
func (o *order) addEdge(e orderEdge) {
	o.edges = append(o.edges, e)
	if o.precedes[e.Before] == nil {
		o.precedes[e.Before] = make(map[string]bool)
	}
	o.precedes[e.Before][e.After] = true
}

// classes returns every class named by an edge or by a class level annotation, sorted so
// that the closure and any diagnostics are deterministic.
func (o *order) classes() []string {
	seen := make(map[string]bool)
	for _, e := range o.edges {
		seen[e.Before] = true
		seen[e.After] = true
	}
	for c := range o.hierarchical {
		seen[c] = true
	}
	for c := range o.withheld {
		seen[c] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// close computes the transitive closure of the declared edges and reports a cycle if the
// declarations do not form a partial order. A cycle is a declaration bug: it would make
// every acquisition of the classes involved a violation.
func (o *order) close() error {
	cs := o.classes()
	for _, k := range cs {
		for _, i := range cs {
			if !o.orders(i, k) {
				continue
			}
			for _, j := range cs {
				if o.orders(k, j) {
					if o.precedes[i] == nil {
						o.precedes[i] = make(map[string]bool)
					}
					o.precedes[i][j] = true
				}
			}
		}
	}
	for _, c := range cs {
		if o.orders(c, c) {
			return fmt.Errorf("lock order declarations contain a cycle through class %q", c)
		}
	}
	return nil
}

// orders reports whether a must be acquired before b.
func (o *order) orders(a, b string) bool {
	m := o.precedes[a]
	if m == nil {
		return false
	}
	return m[b]
}

// violates reports whether acquiring class acquired while holding class held breaks the
// order, and is the single place the policy lives.
func (o *order) violates(held, acquired string) bool {
	if held == acquired {
		// Nesting two locks of one class: by design for a hierarchy, not enforced for a
		// class whose rule is withheld, a violation otherwise.
		return !o.hierarchical[held] && !o.withheld[held]
	}
	// Acquiring upward: the class being taken comes before one that is already held.
	return o.orders(acquired, held)
}
