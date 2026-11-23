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
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// siteDumpDir is where the site dumps go; empty writes none.
var siteDumpDir = os.Getenv("CHECKLOCKS_SITE_DUMP")

// dumpKind names the kind of site inst is.
func dumpKind(inst ssa.Instruction) string {
	switch i := inst.(type) {
	case ssa.CallInstruction:
		return "call"
	case *ssa.Send:
		return "send"
	case *ssa.Select:
		return "select"
	case *ssa.UnOp:
		if i.Op == token.ARROW {
			return "recv"
		}
	}
	return fmt.Sprintf("%T", inst)
}

// heldString renders locks as sorted paths, each starred if held at entry; "-" for none.
func heldString(locks []heldLock) string {
	if len(locks) == 0 {
		return "-"
	}
	parts := make([]string, len(locks))
	for i, l := range locks {
		parts[i] = l.path.String()
		if l.entry {
			parts[i] += "*"
		}
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}

// writeSiteDump writes one line per engine site, entry and give-up record of this vet unit to its own file in the dump directory.
func (pc *passContext) writeSiteDump() {
	if siteDumpDir == "" || pc.engine == nil {
		return
	}
	name := url.PathEscape(pc.pass.Pkg.Path())
	for _, f := range pc.pass.Files {
		if strings.HasSuffix(pc.pass.Fset.File(f.Pos()).Name(), "_test.go") {
			name += "@test"
			break
		}
	}
	e := pc.engine
	var lines []string
	for inst := range e.held {
		pos := "?"
		if p := inst.Pos(); p.IsValid() {
			at := pc.pass.Fset.Position(p)
			pos = fmt.Sprintf("%s:%d:%d", filepath.Base(at.Filename), at.Line, at.Column)
		}
		held, _ := e.heldSet(inst)
		lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s", relFuncName(inst.Parent()), pos, dumpKind(inst), heldString(sortedHeld(held))))
	}
	for fn, locks := range e.entry {
		lines = append(lines, fmt.Sprintf("%s\tentry\t%s", relFuncName(fn), heldString(locks)))
	}
	for fn, reason := range e.gaveUp {
		lines = append(lines, fmt.Sprintf("%s\tgaveup\t%s", relFuncName(fn), reason))
	}
	slices.Sort(lines)
	out := strings.Join(lines, "\n")
	if len(lines) > 0 {
		out += "\n"
	}
	if err := os.WriteFile(filepath.Join(siteDumpDir, name+".sites"), []byte(out), 0o644); err != nil {
		panic(fmt.Sprintf("site dump: %v", err))
	}
}
