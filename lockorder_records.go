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
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

var (
	lockorderState = os.Getenv("CHECKLOCKS_LOCKORDER_STATE")

	// summaryDumpDir is where the summary dumps go; empty writes none.
	summaryDumpDir = os.Getenv("CHECKLOCKS_SUMMARY_DUMP")
)

func init() {
	switch lockorderState {
	case "walk":
	case "", "engine":
		if shadowLogDir != "" {
			panic("CHECKLOCKS_SHADOW_LOG needs CHECKLOCKS_LOCKORDER_STATE=walk")
		}
	default:
		panic(fmt.Sprintf("CHECKLOCKS_LOCKORDER_STATE %q is not \"walk\" or \"engine\"", lockorderState))
	}
}

func (pc *lockOrderContext) recordState() siteLocks {
	if lockorderState == "walk" {
		return nil
	}
	return &recordLocks{pc: pc, released: make(map[string]bool)}
}

// summaryDump writes this unit's summaries, one line per function, and in engine mode the sites with no record.
// The lockblocking pass writes the second part only, to a .blocking file.
func (pc *lockOrderContext) summaryDump(fns []*ssa.Function, summaries map[*ssa.Function]*summaryFact) {
	if summaryDumpDir == "" {
		return
	}
	state := lockorderState
	if state == "" {
		state = "engine"
	}
	pkg := pc.pass.Pkg.Path()
	var out strings.Builder
	fmt.Fprintf(&out, "# %s state=%s\n", pkg, state)
	for _, fn := range slices.SortedStableFunc(slices.Values(fns), func(a, b *ssa.Function) int {
		return strings.Compare(relFuncName(a), relFuncName(b))
	}) {
		s := summaries[fn]
		if s == nil || (len(s.Acquires) == 0 && len(s.Pairs) == 0 && !s.Blocking && !s.BlockingNamed && len(s.BlockingReleased) == 0 && len(s.ParamCalls) == 0 && len(s.Witnesses) == 0) {
			continue
		}
		var params, witnesses []string
		for _, p := range s.ParamCalls {
			params = append(params, fmt.Sprintf("%d.%s recv=%t released=%s at=%s", p.Index, p.Method, p.OnReceiver, strings.Join(p.Released, "+"), p.At))
		}
		for _, w := range s.Witnesses {
			witnesses = append(witnesses, fmt.Sprintf("released=%s named=%t hops=%s", strings.Join(w.Released, "+"), w.Named, strings.Join(w.Hops, " -> ")))
		}
		fmt.Fprintf(&out, "%s\t%s\tparam:%s\twitness:%s\n", relFuncName(fn), s.String(), strings.Join(params, " | "), strings.Join(witnesses, " | "))
	}
	sites, read := make(map[string]int), make(map[string]int)
	for inst, why := range pc.records.reason {
		sites[why]++
		if pc.records.read[inst] {
			read[why]++
		}
	}
	for _, why := range slices.Sorted(maps.Keys(sites)) {
		fmt.Fprintf(&out, "# norecord %s sites=%d read=%d\n", why, sites[why], read[why])
	}

	name := url.PathEscape(pkg)
	for _, f := range pc.pass.Files {
		if strings.HasSuffix(pc.pass.Fset.File(f.Pos()).Name(), "_test.go") {
			name += "@test"
			break
		}
	}
	ext := ".summary"
	if pc.check == checkBlocking {
		ext = ".blocking"
	}
	tmp, err := os.CreateTemp(summaryDumpDir, name+".tmp*")
	if err == nil {
		_, err = tmp.WriteString(out.String())
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(summaryDumpDir, name+ext))
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "checklocks: summary dump: %v\n", err)
	}
}
