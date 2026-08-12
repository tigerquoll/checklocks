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

package lockorder

import (
	"fmt"
	"go/token"
	"sort"
	"strings"
)

// This mirrors the expectation and suppression machinery of the checklocks analyzer. It is
// kept here while the two analyzers are separate; when the shared multi analyzer machinery
// lands the parametrized version replaces it and this file goes away.

// failData indicates an expected diagnostic.
type failData struct {
	pos   token.Pos
	wants []string
}

// positionKey is a file and line, which is what an expectation can address: the annotation
// and the diagnostic it predicts do not share a column.
type positionKey string

func (pc *passContext) positionKey(pos token.Pos) positionKey {
	position := pc.pass.Fset.Position(pos)
	return positionKey(fmt.Sprintf("%s:%d", position.Filename, position.Line))
}

// addFailure records an expected diagnostic.
func (pc *passContext) addFailure(pos token.Pos, s string) {
	key := pc.positionKey(pos)
	fd, ok := pc.failures[key]
	if !ok {
		fd = &failData{pos: pos}
		pc.failures[key] = fd
	}
	fd.wants = append(fd.wants, strings.TrimSpace(strings.TrimPrefix(s, ":")))
}

// addExemption records a suppression.
func (pc *passContext) addExemption(pos token.Pos) {
	pc.exemptions[pc.positionKey(pos)] = struct{}{}
}

// extractAnnotations dispatches on the prefixes of a comment.
//
// The prefix must start the comment: a marker after other text is not seen, which is the
// same rule the checklocks annotations follow.
func (pc *passContext) extractAnnotations(s string, fns map[string]func(p string)) {
	for prefix, fn := range fns {
		if strings.HasPrefix(s, prefix) {
			fn(s[len(prefix):])
		}
	}
}

// extractLineAnnotations collects the line level expectations and suppressions.
func (pc *passContext) extractLineAnnotations() {
	for _, f := range pc.pass.Files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				pc.extractAnnotations(c.Text, map[string]func(string){
					lockOrderFail:   func(p string) { pc.addFailure(c.Pos(), p) },
					lockOrderIgnore: func(string) { pc.addExemption(c.Pos()) },
				})
			}
		}
	}
}

// maybeFail reports a diagnostic unless the position is suppressed, and consumes a matching
// expectation when the corpus predicted it.
func (pc *passContext) maybeFail(pos token.Pos, fmtStr string, args ...any) {
	key := pc.positionKey(pos)
	if fd, ok := pc.failures[key]; ok {
		s := fmt.Sprintf(fmtStr, args...)
		for i, w := range fd.wants {
			if w == "" || strings.Contains(s, w) {
				fd.wants = append(fd.wants[:i], fd.wants[i+1:]...)
				if len(fd.wants) == 0 {
					delete(pc.failures, key)
				}
				return
			}
		}
	}
	if _, ok := pc.exemptions[key]; ok {
		return
	}
	pc.pass.Reportf(pos, fmtStr, args...)
}

// checkFailures reports the expectations that were never met, which is how the corpus
// catches an analyzer that has stopped detecting something.
func (pc *passContext) checkFailures() {
	for _, fd := range pc.failures {
		wildcards := 0
		for _, want := range fd.wants {
			if want == "" {
				wildcards++
				continue
			}
			pc.pass.Reportf(fd.pos, "missing expected failure %q", want)
		}
		if wildcards != 0 {
			pc.pass.Reportf(fd.pos, "missing %d expected failures", wildcards)
		}
	}
}

// sortStrings is a small helper so the fact encodings are deterministic.
func sortStrings(s []string) { sort.Strings(s) }
