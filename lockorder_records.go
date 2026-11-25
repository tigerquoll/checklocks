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
	case "", "walk":
	case "engine":
		if shadowLogDir != "" {
			panic("CHECKLOCKS_LOCKORDER_STATE=engine cannot be combined with CHECKLOCKS_SHADOW_LOG")
		}
	default:
		panic(fmt.Sprintf("CHECKLOCKS_LOCKORDER_STATE %q is not \"walk\" or \"engine\"", lockorderState))
	}
}

// recordCounts is the sites the record-based state found no record at, with the reason, and which of them were read.
type recordCounts struct {
	reason map[ssa.Instruction]string
	read   map[ssa.Instruction]bool
}

func (pc *lockOrderContext) recordState() siteLocks {
	if lockorderState != "engine" {
		return nil
	}
	return &recordLocks{pc: pc, released: make(map[string]bool)}
}

// recordLocks answers the walk's questions from the engine's record at the last site.
type recordLocks struct {
	pc *lockOrderContext

	// site is the instruction of the last at, and top the function outside every literal around it.
	site ssa.Instruction
	top  *ssa.Function

	released map[string]bool
}

func topOf(fn *ssa.Function) *ssa.Function {
	for fn.Parent() != nil {
		fn = fn.Parent()
	}
	return fn
}

func (r *recordLocks) record() (map[pathKey]heldLock, bool) {
	view := r.pc.engineView()
	if view == nil {
		return nil, false
	}
	return view.heldSet(r.site)
}

func (r *recordLocks) at(fn *ssa.Function, inst ssa.Instruction) {
	r.site, r.top = inst, topOf(fn)
	if _, ok := r.record(); ok {
		return
	}
	c := &r.pc.records
	if c.reason == nil {
		c.reason = make(map[ssa.Instruction]string)
	}
	c.reason[inst] = r.pc.noRecordReason(inst)
}

// noRecordReason says why the engine has no record at inst.
func (pc *lockOrderContext) noRecordReason(inst ssa.Instruction) string {
	view := pc.engineView()
	if view == nil {
		return "nil view"
	}
	fn := inst.Parent()
	if why := view.whyNone(fn); why != "" {
		return why
	}
	if _, walked := view.entryOf(fn); walked {
		return "walked, site unrecorded"
	}
	return "not walked"
}

func (r *recordLocks) held() []string {
	if r.site == nil {
		return nil
	}
	rec, ok := r.record()
	if !ok {
		c := &r.pc.records
		if c.read == nil {
			c.read = make(map[ssa.Instruction]bool)
		}
		c.read[r.site] = true
		return nil
	}
	classes := make(map[string]struct{})
	for _, l := range rec {
		if l.assumed {
			continue
		}
		cs, _ := r.pc.engineClasses(r.top, l)
		for _, c := range cs {
			classes[c] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(classes))
}

func (r *recordLocks) releasedClasses() []string {
	return slices.Sorted(maps.Keys(r.released))
}

func (r *recordLocks) acquire(class string) {
	delete(r.released, class)
}

func (r *recordLocks) release(class string, receiverOwned bool) {
	call, ok := r.site.(ssa.CallInstruction)
	if class == "" || !receiverOwned || !ok || r.pc.engineView() == nil {
		return
	}
	if r.pc.callerOwned(r.top, call, class) {
		r.released[class] = true
	}
}

func (r *recordLocks) fork() siteLocks {
	return &recordLocks{pc: r.pc, site: r.site, top: r.top, released: maps.Clone(r.released)}
}

func (r *recordLocks) merge(other siteLocks) {
	o := other.(*recordLocks)
	for k := range r.released {
		if !o.released[k] {
			delete(r.released, k)
		}
	}
}

func (r *recordLocks) after(fn *ssa.Function, call ssa.CallInstruction, _ bool) {
	if len(r.released) == 0 {
		return
	}
	view := r.pc.engineView()
	for _, gained := range []map[pathKey]lockPath{view.gainsOf(call), view.opGainsOf(call)} {
		for _, p := range gained {
			classes, _ := r.pc.engineClasses(topOf(fn), heldLock{path: p})
			for _, c := range classes {
				delete(r.released, c)
			}
		}
	}
}

// summaryDump writes this unit's summaries, one line per function, and in engine mode the sites with no record.
func (pc *lockOrderContext) summaryDump(fns []*ssa.Function, summaries map[*ssa.Function]*summaryFact) {
	if summaryDumpDir == "" || pc.check != checkOrder {
		return
	}
	state := lockorderState
	if state == "" {
		state = "walk"
	}
	pkg := pc.pass.Pkg.Path()
	var out strings.Builder
	fmt.Fprintf(&out, "# %s state=%s\n", pkg, state)
	for _, fn := range slices.SortedStableFunc(slices.Values(fns), func(a, b *ssa.Function) int {
		return strings.Compare(relFuncName(a), relFuncName(b))
	}) {
		s := summaries[fn]
		if s == nil || (s.String() == "acquires:" && len(s.ParamCalls) == 0 && len(s.Witnesses) == 0) {
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
	tmp, err := os.CreateTemp(summaryDumpDir, name+".tmp*")
	if err == nil {
		_, err = tmp.WriteString(out.String())
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), filepath.Join(summaryDumpDir, name+".summary"))
		}
		if err != nil {
			os.Remove(tmp.Name())
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "checklocks: summary dump: %v\n", err)
	}
}
