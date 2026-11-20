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
	"go/token"
	"slices"
	"testing"

	"golang.org/x/tools/go/analysis"
)

// TestGroupedPath pins the path a group names; a corpus cannot, as a matched site leaves its group.
func TestGroupedPath(t *testing.T) {
	const key = "blocking\x00one defect"
	type site struct {
		line   int
		path   string
		ignore bool

		// expect is an expectation the site states, as a corpus annotation would.
		expect string
	}
	tests := []struct {
		name  string
		sites []site
		want  []string
	}{
		{
			name:  "one path between them",
			sites: []site{{line: 10, path: "p"}, {line: 20, path: "p"}},
			want:  []string{"a wait (p) here"},
		},
		{
			name:  "two paths",
			sites: []site{{line: 10, path: "p"}, {line: 20, path: "q"}},
			want:  []string{"a wait here"},
		},
		{
			name:  "an ignored site does not vote",
			sites: []site{{line: 10, path: "p"}, {line: 20, path: "q", ignore: true}},
			want:  []string{"a wait (p) here"},
		},
		{
			name:  "an expected site does not vote",
			sites: []site{{line: 10, path: "p"}, {line: 20, path: "q", expect: "a wait (q) here"}},
			want:  []string{"a wait (p) here"},
		},
		{
			name:  "an expectation of the message the group prints is met",
			sites: []site{{line: 10, path: "p", expect: "a wait here"}, {line: 20, path: "q", expect: "a wait here"}},
			want:  nil,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file := fset.AddFile("group.go", -1, 1000)
			lines := make([]int, 100)
			for i := range lines {
				lines[i] = i * 10
			}
			file.SetLines(lines)
			var got []string
			pass := &analysis.Pass{
				Fset:   fset,
				Report: func(d analysis.Diagnostic) { got = append(got, d.Message) },
			}
			e := newExpectations(pass, lockBlockingAnnotations, true)
			for _, s := range test.sites {
				pos := file.LineStart(s.line)
				if s.ignore {
					e.addExemption(pos)
				}
				if s.expect != "" {
					e.addFailures(pos, "="+s.expect)
				}
				const plain = "a wait here"
				msg := plain
				if s.path != "" {
					msg = "a wait (" + s.path + ") here"
				}
				e.maybeFailGrouped(key, pos, s.path, plain, "%s", msg)
			}
			e.flushGroups()
			e.checkFailures()
			if !slices.Equal(got, test.want) {
				t.Errorf("reported %q, want %q", got, test.want)
			}
		})
	}
}
