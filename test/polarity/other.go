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

package polarity

// The loop reads as moved.go, so the report below it, in this file, names it by that file.
func clauseFile(c func() bool, g *T) { // +checklocksfail=incompatible return states
//line moved.go:5
	for c() {
		x := get()
		x.mu.Lock()
//line other.go:24
		x.v = 1
		g.mu.Lock() // +checklocksfail=already locked
	}
} // +checklocksfail=the loop containing moved.go:5: &({*ssa.Call:clauseFile:t0}.mu))
