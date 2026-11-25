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
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// pairID names a (block, element) pair of one walk.
type pairID struct {
	block *ssa.BasicBlock
	key   string
}

// deferEntry is a deferred call. A no-op's after lists, as ranks in defers, the relevant defers first
// pushed after it, which run before it; may stands for all of a no-op's entries past maxEntries.
type deferEntry struct {
	d     *ssa.Defer
	after string
	may   bool
}

// maxEntries bounds the distinct entries one no-op defer keeps in a state.
const maxEntries = 32

// deferStack is the pending defers: the relevant ones in first-push order, and the no-op entries.
// A push returns a new stack and never writes into the old one's slices.
type deferStack struct {
	defers []*ssa.Defer
	noops  []deferEntry
}

// push is the one push rule, for the lockState and the join: see lockState.pushDefer.
func (s deferStack) push(d *ssa.Defer, relevant bool) deferStack {
	if !relevant {
		if e := (deferEntry{d: d}); !slices.Contains(s.noops, e) {
			s.noops = capEntries(append(slices.Clip(s.noops), e))
		}
		return s
	}
	rank := slices.Index(s.defers, d)
	if rank < 0 {
		rank = len(s.defers)
		s.defers = append(slices.Clip(s.defers), d)
	}
	r := string(rune(rank))
	noops := make([]deferEntry, 0, len(s.noops))
	for _, e := range s.noops {
		if !e.may && !strings.Contains(e.after, r) {
			e.after += r
		}
		if !slices.Contains(noops, e) {
			noops = append(noops, e)
		}
	}
	s.noops = noops
	return s
}

// capEntries collapses the entries of each no-op that has more than maxEntries, or has a may entry, into one may entry.
func capEntries(es []deferEntry) []deferEntry {
	count := make(map[*ssa.Defer]int)
	for _, e := range es {
		count[e.d]++
		if e.may {
			count[e.d] += maxEntries
		}
	}
	out := make([]deferEntry, 0, len(es))
	for _, e := range es {
		switch n := count[e.d]; {
		case n < 0:
		case n <= maxEntries:
			out = append(out, e)
		default:
			out = append(out, deferEntry{d: e.d, may: true})
			count[e.d] = -1
		}
	}
	return out
}

// canonical sorts the entries by block, instruction, then after, whatever order they arrived in.
func canonical(es []deferEntry) []deferEntry {
	es = slices.Clone(es)
	slices.SortFunc(es, func(a, b deferEntry) int {
		return cmp.Or(cmp.Compare(a.d.Block().Index, b.d.Block().Index),
			cmp.Compare(slices.Index(a.d.Block().Instrs, ssa.Instruction(a.d)), slices.Index(b.d.Block().Instrs, ssa.Instruction(b.d))),
			cmp.Compare(a.after, b.after), cmp.Compare(btoi(a.may), btoi(b.may)))
	})
	return es
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// equal reports whether s and o hold the same relevant defers and the same entries, in any order.
func (s deferStack) equal(o deferStack) bool {
	return slices.Equal(s.defers, o.defers) && len(s.noops) == len(o.noops) &&
		!slices.ContainsFunc(s.noops, func(e deferEntry) bool { return !slices.Contains(o.noops, e) })
}

// union returns s with o's entries added, in canonical order, and whether that changed s.noops.
// Equal elements have the same relevant defers; any other pair panics.
func (s deferStack) union(o deferStack) (deferStack, bool) {
	if !slices.Equal(s.defers, o.defers) {
		panic("defer join: states of one element hold different relevant defers")
	}
	merged := slices.Clone(s.noops)
	for _, e := range o.noops {
		if !slices.Contains(merged, e) {
			merged = append(merged, e)
		}
	}
	merged = canonical(capEntries(merged))
	changed := !slices.Equal(merged, s.noops)
	s.noops = merged
	return s, changed
}

// through returns the stack after block runs on s.
func (pc *passContext) through(block *ssa.BasicBlock, s deferStack) deferStack {
	for _, inst := range block.Instrs {
		switch x := inst.(type) {
		case *ssa.Defer:
			s = s.push(x, pc.deferRelevant(x))
		case *ssa.RunDefers:
			s = deferStack{}
		}
	}
	return s
}

// runDefers unwinds ls's defers for the return run, nil outside a return. A no-op run as a may entry answers unknown
// to query at its site, and so does every site of an unwind in which a relevant call failed, a relevant deferred
// literal ran, or a relevant defer is in a cycle.
func (pc *passContext) runDefers(run *ssa.RunDefers, wc *walkContext, ls *lockState) {
	st := ls.deferStack
	var ran []*ssa.Defer
	failed := false
	n := unwindDefers(ls, func(d *ssa.Defer, at *lockState) {
		pc.opFailed = false
		site := newCallSite(d)
		site.run = run
		pc.checkCall(site, wc, at)
		failed = failed || pc.opFailed && slices.Contains(st.defers, d)
		ran = append(ran, d)
	})
	inexact := failed || slices.ContainsFunc(st.defers, func(d *ssa.Defer) bool {
		return runsLiteral(d) || pc.cfgOf(d.Parent()).cyclic[d.Block()]
	})
	if pc.emit != emitReplay {
		pc.statDeferMay(n)
		return
	}
	marked := false
	for _, d := range ran {
		if inexact || slices.ContainsFunc(st.noops, func(e deferEntry) bool { return e.d == d && e.may }) {
			marked = pc.engine.markInexact(d) || marked
		}
	}
	if marked {
		pc.statDeferUnknown()
	}
}

// runsLiteral says d calls a function literal; replay applies the fold stored for it, not a walk of its body.
func runsLiteral(d *ssa.Defer) bool {
	switch v := d.Common().Value.(type) {
	case *ssa.MakeClosure:
		return true
	case *ssa.Function:
		return v.Object() == nil
	}
	return false
}

// unwindDefers runs ls's relevant defers in reverse rank order, then each no-op entry on a fork of the start
// state holding what the relevant defers in its after left held, applied last to first; a may entry holds every
// lock held at any point. It returns the may entries run.
func unwindDefers(ls *lockState, call func(d *ssa.Defer, at *lockState)) (may int) {
	st := ls.deferStack
	// Emptied before any defer runs, so an inline literal a defer calls does not run its caller's pending defers.
	ls.deferStack = deferStack{}
	if len(st.noops) == 0 {
		for i := len(st.defers) - 1; i >= 0; i-- {
			call(st.defers[i], ls)
		}
		return 0
	}
	start := ls.fork()
	gone := make([]map[pathKey]lockInfo, len(st.defers))
	came := make([]map[pathKey]lockInfo, len(st.defers))
	ever := maps.Clone(ls.lockedMutexes)
	for rank := len(st.defers) - 1; rank >= 0; rank-- {
		before := maps.Clone(ls.lockedMutexes)
		call(st.defers[rank], ls)
		gone[rank], came[rank] = diff(before, ls.lockedMutexes, false), diff(ls.lockedMutexes, before, true)
		maps.Copy(ever, ls.lockedMutexes)
	}
	for _, e := range st.noops {
		held := ever
		if !e.may {
			held = maps.Clone(start.lockedMutexes)
			rs := []rune(e.after)
			for i := len(rs) - 1; i >= 0; i-- {
				maps.Copy(held, came[rs[i]])
				for k := range gone[rs[i]] {
					delete(held, k)
				}
			}
		} else {
			may++
		}
		at := start.fork()
		at.lockedMutexes = held
		call(e.d, at)
	}
	return may
}

// diff returns the locks of a that b does not hold, and with changed also those b holds with another
// exclusive or assumed; object and path are not compared, as only an alias rewrites them under one key.
func diff(a, b map[pathKey]lockInfo, changed bool) map[pathKey]lockInfo {
	out := make(map[pathKey]lockInfo)
	for k, info := range a {
		if o, ok := b[k]; !ok || changed && (o.exclusive != info.exclusive || o.assumed != info.assumed) {
			out[k] = info
		}
	}
	return out
}

// joinDefers gives each record of wc the union of the defer stacks of every arrival at its pair, not
// just the first. Equal elements ignore no-op defers, so the walk kept whichever arrived first.
func (pc *passContext) joinDefers(wc *walkContext, from int) {
	var recs []*blockRecord
	byPair := make(map[pairID]*blockRecord)
	in := make(map[*blockRecord]deferStack)
	for _, r := range pc.records[from:] {
		if r.wc == wc {
			recs = append(recs, r)
			byPair[r.pair] = r
			in[r] = r.parent.deferStack
			in[r] = deferStack{in[r].defers, canonical(in[r].noops)}
		}
	}
	out := make(map[*blockRecord]deferStack, len(recs))
	work := slices.Clone(recs)
	queued := make(map[*blockRecord]bool, len(recs))
	for _, r := range recs {
		queued[r] = true
	}
	for len(work) > 0 {
		r := work[0]
		work = work[1:]
		queued[r] = false
		out[r] = pc.through(r.block, in[r])
		for _, to := range r.outs {
			next := byPair[to]
			if next == nil {
				panic(fmt.Sprintf("defer join: %s block %d has an edge to a pair with no record", wc.fn, r.block.Index))
			}
			var grew bool
			if in[next], grew = in[next].union(out[r]); grew && !queued[next] {
				queued[next] = true
				work = append(work, next)
			}
		}
	}
	for _, r := range recs {
		if !slices.Equal(in[r].noops, r.parent.noops) {
			p := r.parent.fork()
			p.deferStack = in[r]
			r.parent = p
		}
		r.parity.joined(in[r], out[r])
	}
}
