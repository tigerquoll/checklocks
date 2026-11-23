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
	return g
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
	rpo, seq int
	block    *ssa.BasicBlock
	state    *lockState
}

// workList pops the item of least reverse-postorder number, oldest first.
type workList []workItem

func (w workList) Len() int { return len(w) }
func (w workList) Less(i, j int) bool {
	if w[i].rpo != w[j].rpo {
		return w[i].rpo < w[j].rpo
	}
	return w[i].seq < w[j].seq
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
	seen map[*ssa.BasicBlock]map[string]*lockState
	work workList
	seq  int
}

// solve transfers every pair reachable from root entered with entry, once each, in a
// record of its own. It stops when the walk gives up or runs out of budget.
func (pc *passContext) solve(wc *walkContext, root *ssa.BasicBlock, entry *lockState) {
	sv := &solver{pc: pc, wc: wc, cfg: newCFGFacts(wc.fn), seen: make(map[*ssa.BasicBlock]map[string]*lockState)}
	sv.insert(root, entry)
	for len(sv.work) > 0 && !wc.gaveUp && !pc.overBudget {
		item := heap.Pop(&sv.work).(workItem)
		sv.transfer(item.block, item.state)
	}
}

// insert queues the pair unless it is there already. A new pair in a cycle past the cap gives the walk up.
func (sv *solver) insert(block *ssa.BasicBlock, s *lockState) {
	key := sv.pc.elementKey(sv.wc.fn, s)
	pairs := sv.seen[block]
	if kept, ok := pairs[key]; ok {
		sv.pc.cutCheck(sv.wc.fn, kept, s)
		return
	}
	if len(pairs) >= maxCycleStates && sv.cfg.cyclic[block] {
		sv.wc.gaveUp = true
		return
	}
	if pairs == nil {
		pairs = make(map[string]*lockState)
		sv.seen[block] = pairs
	}
	pairs[key] = s
	sv.seq++
	heap.Push(&sv.work, workItem{rpo: sv.cfg.rpo[block], seq: sv.seq, block: block, state: s})
}

// transfer runs block on parent and sends the result along each edge out of it.
func (sv *solver) transfer(block *ssa.BasicBlock, parent *lockState) {
	pc, wc := sv.pc, sv.wc
	if pc.walks++; pc.walks > maxFunctionWalks {
		pc.overBudget = true
	}
	pc.statWalk(wc, block, parent)
	rec := &blockRecord{wc: wc, block: block, parent: parent}
	pc.records = append(pc.records, rec)
	outerRec, outerEmit := pc.current, pc.emit
	pc.current, pc.emit = rec, emitWalk
	ls, rls := pc.transferBlock(wc, block, parent)
	pc.current, pc.emit = outerRec, outerEmit
	rec.parity.walked(ls)

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
	s.dropDead(func(p lockPath) bool { return pathDies(p, dies) })
	return s
}
