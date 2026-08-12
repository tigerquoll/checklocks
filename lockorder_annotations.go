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
	"strings"
)

// The annotations understood by this analyzer. The order relation is declared once, in the
// package that owns the taxonomy, and the classes are declared on the types that carry the
// locks. Everything else mirrors the ignore and expectation machinery of checklocks.
const (
	// lockClassAnnotation names the class of the lock carried by a type, or by a single
	// field when a type carries more than one lock:
	//
	//	// +lockclass:Application
	//	type Application struct { ... }
	lockClassAnnotation = "// +lockclass:"

	// lockOrderAnnotation declares one edge of the order, in the package doc of the package
	// that owns the taxonomy:
	//
	//	// +lockorder:ClusterContext < PartitionContext
	//
	// The relation is a partial order: classes that are not related by the transitive
	// closure of the declared edges are not checked against each other.
	lockOrderAnnotation = "// +lockorder:"

	// lockHierarchicalAnnotation marks a class whose locks legitimately nest with each
	// other, such as a queue that walks its own hierarchy:
	//
	//	// +lockhierarchical:Queue
	lockHierarchicalAnnotation = "// +lockhierarchical:"

	// lockOrderWithheldAnnotation marks a class whose same class rule is known to be
	// broken by the code base and is not enforced yet:
	//
	//	// +lockorderwithheld:Application
	//
	// It exists so that the state of the static check and the state of a runtime checker
	// using the same taxonomy stay visibly in step. It is not an exemption on merit, and
	// the annotation should carry a comment saying what is withheld and why.
	lockOrderWithheldAnnotation = "// +lockorderwithheld:"

	// lockOrderIgnore suppresses the checks in a function or on a single line.
	lockOrderIgnore = "// +lockorderignore"

	// lockOrderFail records an expected diagnostic in the test corpus.
	lockOrderFail = "// +lockorderfail"
)

// lockOrderAnnotations is the self-check annotation set belonging to this analyzer.
//
// There is no force annotation: forcing means asserting a lock is held, and what this
// analysis needs asserted is the ORDER, which a single position cannot express.
var lockOrderAnnotations = annotationSet{
	fail:   lockOrderFail,
	ignore: lockOrderIgnore,
}

// orderEdge is one declared "a is acquired before b" relation.
type orderEdge struct {
	Before string
	After  string
}

// parseOrderEdge parses the payload of a lockorder annotation, "A < B".
func parseOrderEdge(payload string) (orderEdge, error) {
	parts := strings.Split(payload, "<")
	if len(parts) != 2 {
		return orderEdge{}, fmt.Errorf("want \"A < B\", got %q", strings.TrimSpace(payload))
	}
	before := strings.TrimSpace(parts[0])
	after := strings.TrimSpace(parts[1])
	if before == "" || after == "" {
		return orderEdge{}, fmt.Errorf("want \"A < B\", got %q", strings.TrimSpace(payload))
	}
	if before == after {
		return orderEdge{}, fmt.Errorf("class %q cannot precede itself", before)
	}
	return orderEdge{Before: before, After: after}, nil
}

// parseClassName parses the payload of an annotation that names a single class.
func parseClassName(payload string) (string, error) {
	name := strings.TrimSpace(payload)
	if name == "" {
		return "", fmt.Errorf("want a class name")
	}
	if strings.ContainsAny(name, " \t") {
		return "", fmt.Errorf("want a single class name, got %q", name)
	}
	return name, nil
}
