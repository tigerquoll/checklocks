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
	"cmp"
	"fmt"
	"go/token"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// walkStatsDir is where the walk statistics go; empty writes none.
var walkStatsDir = os.Getenv("CHECKLOCKS_WALK_STATS")

type blockKey struct {
	wc    *walkContext
	block *ssa.BasicBlock
}

// walkStats is what the parity build keeps about the walks of one pass.
type walkStats struct {
	// chain holds the functions of the walk in progress, outermost first.
	chain []*ssa.Function

	// storers holds, per function, the addresses it stores to.
	storers map[*ssa.Function]map[ssa.Value]bool

	// deferMay counts the collapsed no-op entries a defer unwind ran.
	reads, walks, nested, overflow, deferMay int
	deferUnknown                             int
	distinct                                 map[blockKey][]*lockState
	classes                                  map[blockKey]map[string]bool
	fnWalks                                  map[string]int

	// widened lists the marker widenings; silenced the releases a top marker silenced.
	widened  map[string]bool
	silenced map[string]bool

	// maxTopRegions is the most regions a top marker held.
	maxTopRegions int
}

// oracleRef gives a lockState the pass whose walk reads it.
type oracleRef struct{ pc *passContext }

func (pc *passContext) oracle() oracleRef { return oracleRef{pc} }

// walkChain adds fn to the walk chain and returns what removes it.
func (pc *passContext) walkChain(fn *ssa.Function) func() {
	pc.stats.chain = append(pc.stats.chain, fn)
	return func() { pc.stats.chain = pc.stats.chain[:len(pc.stats.chain)-1] }
}

// read panics when a stored read is of a slot that a function of the walk chain stores to and
// its pre-pass did not mark relevant.
func (o oracleRef) read(key ssa.Value) {
	pc := o.pc
	if pc == nil || pc.emit == emitReplay {
		return
	}
	st := &pc.stats
	st.reads++
	for _, fn := range st.chain {
		if pc.storesTo(fn)[key] && !pc.relevantSlots(fn).slots[key] {
			panic(fmt.Sprintf("stored read of %s (%s) in a walk of %s, which stores to it but did not mark it relevant", key.Name(), key, fn))
		}
	}
}

func (pc *passContext) storesTo(fn *ssa.Function) map[ssa.Value]bool {
	st := &pc.stats
	if addrs, ok := st.storers[fn]; ok {
		return addrs
	}
	addrs := map[ssa.Value]bool{}
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			if s, ok := inst.(*ssa.Store); ok {
				addrs[s.Addr] = true
			}
		}
	}
	if st.storers == nil {
		st.storers = map[*ssa.Function]map[ssa.Value]bool{}
	}
	st.storers[fn] = addrs
	return addrs
}

// cutCheck panics when the state a cut drops disagrees with the kept one about a substitution.
func (pc *passContext) cutCheck(fn *ssa.Function, kept, dropped *lockState) {
	for key, a := range kept.substitutions {
		if b, ok := dropped.substitutions[key]; ok && (a.path.Key() != b.path.Key() || a.obj != b.obj) {
			panic(fmt.Sprintf("substitution of %s differs between two states cut as one element in %s", key, fn))
		}
	}
}

// statWalk counts one walk of block on parent.
func (pc *passContext) statWalk(wc *walkContext, block *ssa.BasicBlock, parent *lockState) {
	if walkStatsDir == "" {
		return
	}
	st := &pc.stats
	st.walks++
	root, _, nested := strings.Cut(wc.context, ">")
	if nested {
		st.nested++
	}
	if st.fnWalks == nil {
		st.fnWalks = map[string]int{}
		st.distinct = map[blockKey][]*lockState{}
		st.classes = map[blockKey]map[string]bool{}
	}
	st.fnWalks[root]++
	key := blockKey{wc, block}
	if st.classes[key] == nil {
		st.classes[key] = map[string]bool{}
	}
	st.classes[key][pc.classKey(wc.fn, parent)] = true
	for _, e := range st.distinct[key] {
		if pc.sameElement(wc.fn, e, parent) {
			return
		}
	}
	st.distinct[key] = append(st.distinct[key], parent)
}

// statWidened notes a marker that went to top on the edge into block, from size keys.
func (pc *passContext) statWidened(wc *walkContext, block *ssa.BasicBlock, size int) {
	if walkStatsDir == "" {
		return
	}
	if pc.stats.widened == nil {
		pc.stats.widened = map[string]bool{}
	}
	pc.stats.widened[fmt.Sprintf("markerWidened %d %s b%d", size, wc.context, block.Index)] = true
}

// statTopRegions notes a top marker holding n regions.
func (pc *passContext) statTopRegions(n int) {
	pc.stats.maxTopRegions = max(pc.stats.maxTopRegions, n)
}

// statTopSilenced notes the release of p at a site that a top marker silenced, not in a replay.
func (pc *passContext) statTopSilenced(at token.Pos, p lockPath) {
	st := &pc.stats
	if walkStatsDir == "" || pc.emit == emitReplay || len(st.chain) == 0 {
		return
	}
	if st.silenced == nil {
		st.silenced = map[string]bool{}
	}
	st.silenced[fmt.Sprintf("topSilenced %s %s", pc.pass.Fset.Position(at), p.String())] = true
}

// statOverflow counts a give-up.
func (pc *passContext) statOverflow() { pc.stats.overflow++ }

// statDeferMay counts n collapsed no-op entries run.
func (pc *passContext) statDeferMay(n int) { pc.stats.deferMay += n }

// statDeferUnknown counts an unwind in replay that marks a site unknown for the first time: a may entry, a failed relevant call,
// a relevant literal or a relevant defer in a cycle.
func (pc *passContext) statDeferUnknown() { pc.stats.deferUnknown++ }

// writeWalkStats writes the statistics of this vet unit to its own file in the stats directory.
func (pc *passContext) writeWalkStats() {
	if walkStatsDir == "" {
		return
	}
	st := &pc.stats
	name := url.PathEscape(pc.pass.Pkg.Path())
	for _, f := range pc.pass.Files {
		if strings.HasSuffix(pc.pass.Fset.File(f.Pos()).Name(), "_test.go") {
			name += "@test"
			break
		}
	}
	maxDistinct, where, over16, over64 := 0, "", 0, 0
	hist := map[int]int{}
	var big []string
	for key, states := range st.distinct {
		n := len(states)
		bucket := 1
		for bucket < n {
			bucket *= 2
		}
		hist[bucket]++
		if n > 16 {
			over16++
		}
		if n > 64 {
			over64++
		}
		at := fmt.Sprintf("%s b%d", key.wc.context, key.block.Index)
		if n > 16 {
			big = append(big, fmt.Sprintf("big %d %s", n, at))
		}
		if n > maxDistinct || (n == maxDistinct && at < where) {
			maxDistinct, where = n, at
		}
	}
	maxClasses, maxCyclic, cyclicOver64 := 0, 0, 0
	for key, set := range st.classes {
		maxClasses = max(maxClasses, len(set))
		if pc.cfgOf(key.wc.fn).cyclic[key.block] {
			maxCyclic = max(maxCyclic, len(set))
		}
	}
	for key, states := range st.distinct {
		if len(states) > 64 && pc.cfgOf(key.wc.fn).cyclic[key.block] {
			cyclicOver64++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "walks %d\nnested %d\nmaxDistinct %d\t%s\nover16 %d\nover64 %d\noverflow %d\noracleReads %d\noracleMisses 0\ndeferMay %d\ndeferUnknown %d\n",
		st.walks, st.nested, maxDistinct, where, over16, over64, st.overflow, st.reads, st.deferMay, st.deferUnknown)
	for _, bucket := range slices.Sorted(maps.Keys(hist)) {
		fmt.Fprintf(&out, "hist<=%d %d\n", bucket, hist[bucket])
	}
	for _, line := range slices.Sorted(slices.Values(big)) {
		out.WriteString(line + "\n")
	}
	fmt.Fprintf(&out, "maxClasses %d\nmaxCyclicClasses %d\ncyclic blocks over 64 elements: %d\nmaxTopRegions %d\n", maxClasses, maxCyclic, cyclicOver64, st.maxTopRegions)
	for _, line := range slices.Sorted(maps.Keys(st.widened)) {
		out.WriteString(line + "\n")
	}
	for _, line := range slices.Sorted(maps.Keys(st.silenced)) {
		out.WriteString(line + "\n")
	}
	fns := slices.SortedFunc(maps.Keys(st.fnWalks), func(a, b string) int {
		return cmp.Or(cmp.Compare(st.fnWalks[b], st.fnWalks[a]), strings.Compare(a, b))
	})
	for _, fn := range fns {
		fmt.Fprintf(&out, "fn %d\t%s\n", st.fnWalks[fn], fn)
	}
	if err := os.WriteFile(filepath.Join(walkStatsDir, name+".stats"), []byte(out.String()), 0o644); err != nil {
		panic(fmt.Sprintf("walk stats: %v", err))
	}
}
