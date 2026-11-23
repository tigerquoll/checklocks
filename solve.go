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
	"container/heap"
	"maps"

	"golang.org/x/tools/go/ssa"
)

type edge struct{ from, to *ssa.BasicBlock }

// cfgFacts is what the solver reads of a function's block graph.
type cfgFacts struct {
	// rpo is the reverse-postorder number of each block, by the order the walk visits successors in.
	rpo map[*ssa.BasicBlock]int

	// cyclic holds the blocks on a cycle: in a nontrivial SCC, or with a self-loop.
	cyclic map[*ssa.BasicBlock]bool

	// redefined maps each block in a nontrivial SCC of the graph without its dominance back
	// edges to the set holding only itself.
	redefined map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool

	// loops holds the natural loop of each dominance back edge met.
	loops map[edge]map[*ssa.BasicBlock]bool

	// region names the loop each cyclic block belongs to.
	region map[*ssa.BasicBlock]region
}

// region is the innermost natural loop holding a cyclic block, named by its header, or a
// cyclic block that lies in no natural loop, alone.
type region struct {
	block *ssa.BasicBlock
	loop  bool
}

// regionSet is a set of regions, never written once shared.
type regionSet map[region]struct{}

// with returns the set and r.
func (s regionSet) with(r region) regionSet {
	return s.union(regionSet{r: {}})
}

// union returns the set and o, sharing a set that already holds the other.
func (s regionSet) union(o regionSet) regionSet {
	switch {
	case len(o) == 0:
		return s
	case len(s) == 0:
		return o
	}
	missing := false
	for r := range o {
		if _, ok := s[r]; !ok {
			missing = true
			break
		}
	}
	if !missing {
		return s
	}
	u := make(regionSet, len(s)+len(o))
	maps.Copy(u, s)
	maps.Copy(u, o)
	return u
}

// cfgOf returns the block graph facts of fn, built once per pass.
func (pc *passContext) cfgOf(fn *ssa.Function) *cfgFacts {
	g := pc.cfgs[fn]
	if g == nil {
		if pc.cfgs == nil {
			pc.cfgs = make(map[*ssa.Function]*cfgFacts)
		}
		g = newCFGFacts(fn)
		pc.cfgs[fn] = g
	}
	return g
}

func newCFGFacts(fn *ssa.Function) *cfgFacts {
	g := &cfgFacts{
		rpo:       make(map[*ssa.BasicBlock]int),
		redefined: make(map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool),
		loops:     make(map[edge]map[*ssa.BasicBlock]bool),
	}
	var post []*ssa.BasicBlock
	visited := make(map[*ssa.BasicBlock]bool)
	var visit func(b *ssa.BasicBlock)
	visit = func(b *ssa.BasicBlock) {
		visited[b] = true
		for _, s := range succOrder(fn, b) {
			if !visited[s] {
				visit(s)
			}
		}
		post = append(post, b)
	}
	visit(fn.Blocks[0])
	for i, b := range post {
		g.rpo[b] = len(post) - 1 - i
	}
	g.cyclic = cyclicBlocks(post, func(from, to *ssa.BasicBlock) bool { return true })
	for b := range cyclicBlocks(post, func(from, to *ssa.BasicBlock) bool { return !to.Dominates(from) }) {
		g.redefined[b] = map[*ssa.BasicBlock]bool{b: true}
	}
	g.region = cyclicRegions(g.cyclic)
	return g
}

// cyclicRegions assigns each cyclic block its region. The header of a natural loop owns the
// union of the loops of all back edges into it.
func cyclicRegions(cyclic map[*ssa.BasicBlock]bool) map[*ssa.BasicBlock]region {
	bodies := make(map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool)
	for b := range cyclic {
		for _, s := range b.Succs {
			if s.Dominates(b) {
				if bodies[s] == nil {
					bodies[s] = make(map[*ssa.BasicBlock]bool)
				}
				maps.Copy(bodies[s], loopBody(s, b))
			}
		}
	}
	regions := make(map[*ssa.BasicBlock]region, len(cyclic))
	for b := range cyclic {
		r := region{block: b}
		// A header dominates its loop, so the nearest one holding b is the innermost loop.
		for h := b; h != nil; h = h.Idom() {
			if bodies[h][b] {
				r = region{block: h, loop: true}
				break
			}
		}
		regions[b] = r
	}
	return regions
}

// cyclicBlocks returns the blocks that lie on a cycle of the graph over the edges keep admits.
func cyclicBlocks(blocks []*ssa.BasicBlock, keep func(from, to *ssa.BasicBlock) bool) map[*ssa.BasicBlock]bool {
	index := make(map[*ssa.BasicBlock]int)
	low := make(map[*ssa.BasicBlock]int)
	onStack := make(map[*ssa.BasicBlock]bool)
	var stack []*ssa.BasicBlock
	out := make(map[*ssa.BasicBlock]bool)
	var strong func(v *ssa.BasicBlock)
	strong = func(v *ssa.BasicBlock) {
		index[v], low[v] = len(index), len(index)
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range v.Succs {
			if !keep(v, w) {
				continue
			}
			if w == v {
				out[v] = true
			}
			if _, ok := index[w]; !ok {
				strong(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		i := len(stack) - 1
		for stack[i] != v {
			i--
		}
		component := stack[i:]
		stack = stack[:i]
		for _, w := range component {
			onStack[w] = false
			if len(component) > 1 {
				out[w] = true
			}
		}
	}
	for _, b := range blocks {
		if _, ok := index[b]; !ok {
			strong(b)
		}
	}
	return out
}

// workItem is a block and the state it was reached in, waiting to be transferred.
type workItem struct {
	rank  [2]int
	block *ssa.BasicBlock
	state *lockState
	key   string
}

// workList pops the item of least rank.
type workList []workItem

func (w workList) Len() int { return len(w) }
func (w workList) Less(i, j int) bool {
	if w[i].rank[0] != w[j].rank[0] {
		return w[i].rank[0] < w[j].rank[0]
	}
	return w[i].rank[1] < w[j].rank[1]
}
func (w workList) Swap(i, j int) { w[i], w[j] = w[j], w[i] }
func (w *workList) Push(x any)   { *w = append(*w, x.(workItem)) }
func (w *workList) Pop() any {
	old := *w
	item := old[len(old)-1]
	*w = old[:len(old)-1]
	return item
}

// solver closes one walk of a function over the (block, element) pairs reachable from its entry.
type solver struct {
	pc   *passContext
	wc   *walkContext
	cfg  *cfgFacts
	seen map[*ssa.BasicBlock]map[string]map[string]*lockState
	work workList
	seq  int
}

// solve transfers every pair reachable from root entered with entry, once each, in a
// record of its own. It stops when the walk gives up or runs out of budget.
func (pc *passContext) solve(wc *walkContext, root *ssa.BasicBlock, entry *lockState) {
	sv := &solver{pc: pc, wc: wc, cfg: pc.cfgOf(wc.fn), seen: make(map[*ssa.BasicBlock]map[string]map[string]*lockState)}
	sv.insert(root, entry)
	for len(sv.work) > 0 && !wc.gaveUp && !pc.overBudget {
		sv.transfer(heap.Pop(&sv.work).(workItem))
	}
}

// insert queues the pair unless it is there already. A new class at a cycle block past the cap gives the walk up.
func (sv *solver) insert(block *ssa.BasicBlock, s *lockState) {
	class, mark := sv.pc.classKey(sv.wc.fn, s), markerKey(s)
	classes := sv.seen[block]
	marks, ok := classes[class]
	if kept, ok := marks[mark]; ok {
		sv.pc.cutCheck(sv.wc.fn, kept, s)
		return
	}
	if !ok {
		if len(classes) >= maxCycleStates && sv.cfg.cyclic[block] {
			sv.wc.gaveUp = true
			return
		}
		if classes == nil {
			classes = make(map[string]map[string]*lockState)
			sv.seen[block] = classes
		}
		marks = make(map[string]*lockState)
		classes[class] = marks
	}
	marks[mark] = s
	sv.seq++
	heap.Push(&sv.work, workItem{rank: workRank(sv.wc.fn, sv.cfg.rpo[block], sv.seq), block: block, state: s, key: class + mark})
}

// transfer runs block on parent and sends the result along each edge out of it.
func (sv *solver) transfer(item workItem) {
	pc, wc, block, parent := sv.pc, sv.wc, item.block, item.state
	if pc.walks++; pc.walks > maxFunctionWalks {
		pc.overBudget = true
	}
	pc.statWalk(wc, block, parent)
	rec := &blockRecord{wc: wc, block: block, parent: parent}
	pc.records = append(pc.records, rec)
	rec.parity.entered(pc, wc.fn, parent, item.key)
	outerRec, outerEmit := pc.current, pc.emit
	pc.current, pc.emit = rec, emitWalk
	ls, rls := pc.transferBlock(wc, block, parent)
	pc.current, pc.emit = outerRec, outerEmit
	rec.parity.walked(pc, wc.fn, ls)

	if rls != nil {
		text, summary := rls.acquiredString(wc.entryLocks)
		wc.returnStates[summary] = text
	}
	for _, succ := range succOrder(wc.fn, block) {
		if wc.gaveUp || pc.overBudget {
			return
		}
		sv.insert(succ, sv.carry(block, succ, ls))
	}
}

// carry returns the state the edge from block to succ delivers. A back edge loses the locks taken
// through values its loop defines; an edge into a cycle no back edge breaks, those succ defines.
func (sv *solver) carry(block, succ *ssa.BasicBlock, ls *lockState) *lockState {
	var dies map[*ssa.BasicBlock]bool
	if succ.Dominates(block) {
		e := edge{block, succ}
		if sv.cfg.loops[e] == nil {
			sv.cfg.loops[e] = loopBody(succ, block)
		}
		dies = sv.cfg.loops[e]
	} else {
		dies = sv.cfg.redefined[succ]
	}
	if dies == nil {
		return ls
	}
	s := ls.fork()
	if n := s.dropDead(func(p lockPath) bool { return pathDies(p, dies) }, sv.pc.regionsOf); n > 0 {
		sv.pc.statWidened(sv.wc, succ, n)
	}
	if s.markerTop {
		sv.pc.statTopRegions(len(s.topRegions))
	}
	return s
}
