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

import "testing"

// yunikornOrder builds the taxonomy the yunikorn scheduler declares, which is the order the
// runtime checker in its locking package enforces. It is used here to keep the two in step.
func yunikornOrder(t *testing.T) *order {
	t.Helper()
	o := newOrder()
	for _, e := range []orderEdge{
		{"ClusterContext", "PartitionContext"},
		{"PartitionContext", "Application"},
		{"Application", "Queue"},
		{"Application", "Node"},
		{"Application", "UGMManager"},
		{"UGMManager", "UserTracker"},
		{"UGMManager", "GroupTracker"},
	} {
		o.addEdge(e)
	}
	o.hierarchical["Queue"] = true
	if err := o.close(); err != nil {
		t.Fatalf("closing the order: %v", err)
	}
	return o
}

func TestOrderClosure(t *testing.T) {
	o := yunikornOrder(t)
	tests := []struct {
		a, b string
		want bool
	}{
		// declared edges
		{"ClusterContext", "PartitionContext", true},
		{"UGMManager", "UserTracker", true},
		// transitive
		{"ClusterContext", "Application", true},
		{"ClusterContext", "UserTracker", true},
		{"PartitionContext", "GroupTracker", true},
		{"Application", "UserTracker", true},
		// reverse never holds
		{"PartitionContext", "ClusterContext", false},
		{"UserTracker", "ClusterContext", false},
		// siblings are unrelated
		{"Queue", "Node", false},
		{"Node", "Queue", false},
		{"UserTracker", "GroupTracker", false},
	}
	for _, tc := range tests {
		if got := o.orders(tc.a, tc.b); got != tc.want {
			t.Errorf("orders(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestViolates(t *testing.T) {
	o := yunikornOrder(t)
	tests := []struct {
		name           string
		held, acquired string
		want           bool
	}{
		{"downward is allowed", "ClusterContext", "PartitionContext", false},
		{"downward transitively", "ClusterContext", "UserTracker", false},
		{"upward is a violation", "Application", "PartitionContext", true},
		{"upward transitively", "UserTracker", "ClusterContext", true},
		{"unrelated pair is silent", "Node", "Queue", false},
		{"unrelated pair is silent either way", "Queue", "Node", false},
		{"same class nests only for a hierarchy", "Queue", "Queue", false},
		{"same class is otherwise a violation", "Application", "Application", true},
		{"same class on an unordered class too", "Node", "Node", true},
	}
	for _, tc := range tests {
		if got := o.violates(tc.held, tc.acquired); got != tc.want {
			t.Errorf("%s: violates(%q, %q) = %v, want %v", tc.name, tc.held, tc.acquired, got, tc.want)
		}
	}
}

// TestWithheldSuppressesSameClass pins the state that mirrors the runtime checker: a class
// whose same class rule is withheld must not be reported until the code is fixed.
func TestWithheldSuppressesSameClass(t *testing.T) {
	o := yunikornOrder(t)
	if !o.violates("Application", "Application") {
		t.Fatal("precondition: the application same class rule is enforced by default")
	}
	o.withheld["Application"] = true
	if o.violates("Application", "Application") {
		t.Error("a withheld class must not be reported for same class nesting")
	}
	// Withholding the same class rule must not weaken the ordered pairs of that class.
	if !o.violates("Application", "PartitionContext") {
		t.Error("withholding the same class rule must not affect ordered pairs")
	}
}

func TestCycleIsReported(t *testing.T) {
	o := newOrder()
	o.addEdge(orderEdge{"A", "B"})
	o.addEdge(orderEdge{"B", "C"})
	o.addEdge(orderEdge{"C", "A"})
	if err := o.close(); err == nil {
		t.Error("a cyclic declaration must be reported")
	}
}

func TestParseOrderEdge(t *testing.T) {
	tests := []struct {
		in      string
		want    orderEdge
		wantErr bool
	}{
		{in: "A < B", want: orderEdge{"A", "B"}},
		{in: "  Cluster  <  Partition ", want: orderEdge{"Cluster", "Partition"}},
		{in: "A", wantErr: true},
		{in: "A < ", wantErr: true},
		{in: " < B", wantErr: true},
		{in: "A < B < C", wantErr: true},
		{in: "A < A", wantErr: true},
	}
	for _, tc := range tests {
		got, err := parseOrderEdge(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseOrderEdge(%q): want an error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseOrderEdge(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseOrderEdge(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestSummaryFactMerge(t *testing.T) {
	a := &summaryFact{}
	a.addAcquire("Queue")
	a.addAcquire("Application")
	a.addPair("Application", "Queue")

	b := &summaryFact{}
	b.addAcquire("Node")
	b.addAcquire("Queue") // already present in a
	b.addPair("Application", "Node")

	if !a.merge(b) {
		t.Error("merging new content must report a change")
	}
	if got, want := len(a.Acquires), 3; got != want {
		t.Errorf("Acquires = %v, want %d entries", a.Acquires, want)
	}
	// The set must stay sorted so the fact encoding is stable across runs.
	for i := 1; i < len(a.Acquires); i++ {
		if a.Acquires[i-1] > a.Acquires[i] {
			t.Fatalf("Acquires is not sorted: %v", a.Acquires)
		}
	}
	if len(a.Pairs) != 2 {
		t.Errorf("Pairs = %v, want 2 entries", a.Pairs)
	}
	if a.merge(b) {
		t.Error("merging content that is already present must report no change")
	}
}

// TestBlockingBitSurvivesMerge pins the hook the blocking-call analyzer rides on: the bit is
// unused here but must not be dropped, or that analyzer needs a fact format migration.
func TestBlockingBitSurvivesMerge(t *testing.T) {
	a := &summaryFact{}
	b := &summaryFact{Blocking: true}
	if !a.merge(b) {
		t.Error("merging a blocking summary into a non blocking one is a change")
	}
	if !a.Blocking {
		t.Error("the blocking bit must survive a merge")
	}
}
