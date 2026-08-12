// Copyright 2020 The gVisor Authors.
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

const (
	checkLocksAnnotation     = "// +checklocks:"
	checkLocksAnnotationRead = "// +checklocksread:"
	checkLocksAcquires       = "// +checklocksacquire:"
	checkLocksAcquiresRead   = "// +checklocksacquireread:"
	checkLocksReleases       = "// +checklocksrelease:"
	checkLocksReleasesRead   = "// +checklocksreleaseread:"
	checkLocksExcludes       = "// +checklocksexclude:"
	checkLocksExcludesWrite  = "// +checklocksexcludewrite:"
	checkLocksIgnore         = "// +checklocksignore"
	checkLocksForce          = "// +checklocksforce"
	checkLocksFail           = "// +checklocksfail"
	checkLocksAlias          = "// +checklocksalias:"
	checkAtomicAnnotation    = "// +checkatomic"
)

// extractAnnotations extracts annotations from text.
func (pc *passContext) extractAnnotations(s string, fns map[string]func(p string)) {
	extractAnnotations(s, fns)
}
