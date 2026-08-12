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
	"strings"
)

// classFact is attached to a named type whose lock participates in the order. It is the
// only channel by which a class declaration reaches another package.
type classFact struct {
	// Class is the declared class name.
	Class string

	// Field is the name of the lock field the class applies to. It is empty when the type
	// carries a single lock, which is the common case.
	Field string
}

func (*classFact) AFact() {}

func (f *classFact) String() string {
	if f.Field == "" {
		return "lockclass:" + f.Class
	}
	return "lockclass:" + f.Class + " on " + f.Field
}

// orderFact carries the declared order out of the package that owns the taxonomy. The
// whole relation travels as one fact so that a dependent package sees a consistent
// taxonomy rather than a partially imported one.
type orderFact struct {
	Edges        []orderEdge
	Hierarchical []string
	Withheld     []string
}

func (*orderFact) AFact() {}

func (f *orderFact) String() string {
	parts := make([]string, 0, len(f.Edges))
	for _, e := range f.Edges {
		parts = append(parts, e.Before+"<"+e.After)
	}
	sort.Strings(parts)
	s := "lockorder:" + strings.Join(parts, ",")
	if len(f.Hierarchical) > 0 {
		s += " hierarchical:" + strings.Join(f.Hierarchical, ",")
	}
	if len(f.Withheld) > 0 {
		s += " withheld:" + strings.Join(f.Withheld, ",")
	}
	return s
}

// classPair is one realized "held then acquired" observation.
type classPair struct {
	Held     string
	Acquired string
}

// summaryFact is the per function summary. It is what makes the analysis modular: a call
// site consults the summary of the callee rather than walking into it, so a package can be
// analyzed with only the facts of its dependencies.
type summaryFact struct {
	// Acquires is the set of classes the function may acquire, directly or through any
	// statically dispatched callee. Sorted, so the fact encoding is stable.
	Acquires []string

	// Pairs is the set of class pairs the function itself realizes, kept for reporting and
	// for the corpus expectations.
	Pairs []classPair

	// Blocking reports whether the function may reach a blocking sink. It is unused by
	// this analyzer and is present so that the blocking-call analyzer can ride on the same
	// summary without a fact format migration.
	Blocking bool
}

func (*summaryFact) AFact() {}

func (f *summaryFact) String() string {
	s := "acquires:" + strings.Join(f.Acquires, ",")
	if len(f.Pairs) > 0 {
		parts := make([]string, 0, len(f.Pairs))
		for _, p := range f.Pairs {
			parts = append(parts, p.Held+">"+p.Acquired)
		}
		sort.Strings(parts)
		s += " pairs:" + strings.Join(parts, ",")
	}
	if f.Blocking {
		s += " blocking"
	}
	return s
}

// addAcquire adds a class to the summary, keeping the slice sorted and unique.
func (f *summaryFact) addAcquire(class string) bool {
	if class == "" {
		return false
	}
	i := sort.SearchStrings(f.Acquires, class)
	if i < len(f.Acquires) && f.Acquires[i] == class {
		return false
	}
	f.Acquires = append(f.Acquires, "")
	copy(f.Acquires[i+1:], f.Acquires[i:])
	f.Acquires[i] = class
	return true
}

// addPair records a realized pair, keeping the slice unique.
func (f *summaryFact) addPair(held, acquired string) bool {
	for _, p := range f.Pairs {
		if p.Held == held && p.Acquired == acquired {
			return false
		}
	}
	f.Pairs = append(f.Pairs, classPair{Held: held, Acquired: acquired})
	return true
}

// merge folds another summary into this one and reports whether anything changed, which is
// what drives the in package fixpoint.
func (f *summaryFact) merge(other *summaryFact) bool {
	changed := false
	for _, c := range other.Acquires {
		if f.addAcquire(c) {
			changed = true
		}
	}
	for _, p := range other.Pairs {
		if f.addPair(p.Held, p.Acquired) {
			changed = true
		}
	}
	if other.Blocking && !f.Blocking {
		f.Blocking = true
		changed = true
	}
	return changed
}

// funcFact records the function level annotations of this analyzer.
type funcFact struct {
	// Ignore suppresses reporting inside the function and at its call sites.
	Ignore bool
}

func (*funcFact) AFact() {}

func (f *funcFact) String() string {
	if f.Ignore {
		return "lockorderignore"
	}
	return ""
}

// String is used by the tests to compare a computed summary against an expectation.
func (p classPair) String() string { return fmt.Sprintf("%s>%s", p.Held, p.Acquired) }
