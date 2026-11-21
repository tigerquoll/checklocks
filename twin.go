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

//go:build checklocks_parity

package checklocks

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// twinNote logs two locks that share a rendering but not a key, to the file
// CHECKLOCKS_TWIN_LOG names.
func twinNote(op, display string, a, b pathKey) {
	if a == b {
		return
	}
	name := os.Getenv("CHECKLOCKS_TWIN_LOG")
	if name == "" {
		return
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(fmt.Sprintf("twin\t%s\t%s\t%s\t%s\n", op, display, a, b))
}

// twinSummary lists the key and mode of each entry keep accepts, in order.
func twinSummary(l *lockState, keep func(lockInfo) bool) string {
	var parts []string
	for _, info := range l.lockedMutexes {
		if keep(info) {
			parts = append(parts, fmt.Sprintf("%q %t", string(info.path.Key()), info.exclusive))
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}
