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
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/ssa"
)

// An object under construction is not shared, so its guards cannot be violated: nothing
// else can reach it to race with. The suppressions that state this by hand are the largest
// single family in an annotated code base, and each of them silences a line rather than the
// reason for it.
//
// Two annotations state the reason instead. A constructor declares that what it returns is
// unpublished, and a function declares that a parameter must arrive unpublished. Both are
// CHECKED — the first where it is written, the second at every call site — so an object is
// treated as unshared because something proved it, not because something asserted it.
//
// Everything the analysis does not understand publishes. A call with no summary, a value
// used in a way not on the table below, an interface dispatch: all of them end freshness,
// so the failure mode of not knowing is a report rather than a silence.
const (
	// checkLocksReturnsFresh declares that what a function returns cannot be reached by
	// any other goroutine yet:
	//
	//	// +checklocksreturnsfresh
	//	func newQueue() *Queue { ... }
	checkLocksReturnsFresh = "// +checklocksreturnsfresh"

	// checkLocksFresh declares that a parameter must arrive unpublished:
	//
	//	// +checklocksfresh:child
	//	func (q *Queue) addChild(child *Queue) { ... }
	checkLocksFresh = "// +checklocksfresh:"
)

// freshFacts records what a function says, and what it does, about unpublished objects.
type freshFacts struct {
	// ReturnsFresh is the declaration that the returned object is unpublished. It is
	// checked in the function that carries it.
	ReturnsFresh bool

	// FreshParams are the parameters declared to arrive unpublished, by index, with the
	// receiver as index zero. Checked at every call site.
	FreshParams []int

	// Publishes are the parameters the function may make reachable by another goroutine,
	// by index. This one is computed rather than declared, and it is what a caller
	// consults to know whether an object it passed is still its own afterwards.
	Publishes []int

	// Analyzed says the body was walked and Publishes is what it found. Without it the
	// absence of a fact would mean two different things — a function that publishes
	// nothing, and a function nothing was known about — and a caller has to treat the
	// second as publishing everything.
	Analyzed bool
}

func (*freshFacts) AFact() {}

func (f *freshFacts) String() string {
	s := ""
	if f.ReturnsFresh {
		s = "returnsfresh"
	}
	if f.Analyzed {
		s += " analyzed"
	}
	if len(f.FreshParams) > 0 {
		s += " fresh:" + intsString(f.FreshParams)
	}
	if len(f.Publishes) > 0 {
		s += " publishes:" + intsString(f.Publishes)
	}
	return s
}

// intsString renders a parameter index set for a fact string.
func intsString(in []int) string {
	out := ""
	for i, n := range in {
		if i > 0 {
			out += ","
		}
		out += string(rune('0' + n))
	}
	return out
}

// hasIndex reports whether an index set contains n.
func hasIndex(in []int, n int) bool {
	for _, i := range in {
		if i == n {
			return true
		}
	}
	return false
}

// maxPublishRounds bounds the fixpoint that works out what a function publishes. The sets
// only grow over a finite number of parameters, so it settles well inside this; the bound
// is a guard against a bug rather than an expected outcome.
const maxPublishRounds = 100

// escapePoint is one place a value stops being unpublished.
type escapePoint struct {
	// inst is where it happens.
	inst ssa.Instruction

	// field is the field made reachable, or allFields when the whole object is.
	field int
}

// allFields means the escape publishes the object itself rather than a pointer into it.
//
// The distinction matters because a pointer to a field is not a handle on the object that
// contains it: a goroutine holding &q.mu can lock the queue and cannot read q.name. Taking
// the address of the lock is what every lock call does, so treating an interior pointer as
// publishing the whole object would leave nothing fresh anywhere.
const allFields = -1

// freshState is what a function knows about the objects under construction in it.
type freshState struct {
	// escapes are the points at which each root stops being unpublished.
	escapes map[ssa.Value][]escapePoint

	// roots are the values that start out unpublished.
	roots map[ssa.Value]bool

	// order is the position of each instruction in its block.
	order map[ssa.Instruction]int

	// reaches[a][b] reports whether control can get from block a to block b.
	reaches map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool
}

// isFreshAt reports whether a value is still unpublished at an instruction.
func (fs *freshState) isFreshAt(v ssa.Value, at ssa.Instruction, field int) bool {
	if fs == nil || at == nil {
		return false
	}
	root, rootField := freshRootOf(v)
	if !fs.roots[root] {
		return false
	}
	if rootField != allFields {
		// The access is through a pointer into the object, so it is the field that
		// pointer names that has to be unpublished, not the one named here.
		field = rootField
	}
	for _, e := range fs.escapes[root] {
		if e.field != allFields && e.field != field {
			continue
		}
		if fs.canReach(e.inst, at) {
			return false
		}
	}
	return true
}

// canReach reports whether the first instruction can execute before the second.
func (fs *freshState) canReach(from, to ssa.Instruction) bool {
	fb, tb := from.Block(), to.Block()
	if fb == nil || tb == nil {
		return true // Cannot tell, so assume it does.
	}
	if fb == tb {
		// Within one block, order decides, except that a block reaching itself puts
		// everything in it after everything else on the second time round.
		if fs.reaches[fb][fb] {
			return true
		}
		return fs.order[from] < fs.order[to]
	}
	return fs.reaches[fb][tb]
}

// freshRootOf unwraps a value to the root it is derived from, and the field that was
// traversed to get there, if any.
//
// A load is deliberately not unwrapped. Reading a pointer out of a local variable produces
// the object it points AT, which has nothing to do with where the pointer was kept, and
// treating the two as one would call an object fresh because the variable holding it is.
func freshRootOf(v ssa.Value) (ssa.Value, int) {
	field := allFields
	for {
		switch x := v.(type) {
		case *ssa.FieldAddr:
			if field == allFields {
				field = x.Field
			}
			v = x.X
		case *ssa.Field:
			if field == allFields {
				field = x.Field
			}
			v = x.X
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		default:
			return v, field
		}
	}
}

// computeFresh works out which objects in a function start unpublished, and where each of
// them stops being so.
func (pc *passContext) computeFresh(fn *ssa.Function) *freshState {
	fs := &freshState{
		escapes: make(map[ssa.Value][]escapePoint),
		roots:   make(map[ssa.Value]bool),
		order:   make(map[ssa.Instruction]int),
	}
	if fn.Blocks == nil {
		return fs
	}
	for _, b := range fn.Blocks {
		for i, inst := range b.Instrs {
			fs.order[inst] = i
		}
	}
	fs.reaches = blockReachability(fn)

	// A parameter declared to arrive unpublished starts as a root.
	if obj := funcObject(fn); obj != nil {
		var ff freshFacts
		if pc.pass.ImportObjectFact(obj, &ff) {
			for _, index := range ff.FreshParams {
				if index < len(fn.Params) {
					fs.roots[fn.Params[index]] = true
				}
			}
		}
	}

	// An allocation, and the result of a constructor that declares its result
	// unpublished, are the other two roots.
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			switch x := inst.(type) {
			case *ssa.Alloc:
				fs.roots[x] = true
			case *ssa.Call:
				if pc.returnsFresh(x.Common()) {
					fs.roots[x] = true
				}
			}
		}
	}

	for root := range fs.roots {
		fs.escapes[root] = pc.escapesOf(root, allFields, false /* forExport */, make(map[ssa.Value]bool))
	}
	return fs
}

// returnsFresh reports whether a call is to a function declaring that what it returns is
// unpublished.
func (pc *passContext) returnsFresh(common *ssa.CallCommon) bool {
	callee := common.StaticCallee()
	if callee == nil {
		return false
	}
	obj := funcObject(callee)
	if obj == nil {
		return false
	}
	var ff freshFacts
	if !pc.pass.ImportObjectFact(obj, &ff) {
		return false
	}
	return ff.ReturnsFresh
}

// escapesOf collects the points at which a value stops being unpublished.
//
// forExport asks the question a CALLER needs answered rather than the one the function
// itself needs: whether the value is reachable by another goroutine once this function
// returns. The two differ for a publication into a container the function holds the lock
// of, which is safe for the rest of that critical section and not afterwards.
func (pc *passContext) escapesOf(v ssa.Value, field int, forExport bool, seen map[ssa.Value]bool) []escapePoint {
	if seen[v] {
		return nil
	}
	seen[v] = true
	refs := v.Referrers()
	if refs == nil {
		return nil
	}
	var out []escapePoint
	escape := func(inst ssa.Instruction) {
		out = append(out, escapePoint{inst: inst, field: field})
	}
	for _, ref := range *refs {
		switch x := ref.(type) {
		case *ssa.FieldAddr:
			// A pointer into the object. Where it goes publishes that field, not
			// the object: there is no way back from a field to its container.
			out = append(out, pc.escapesOf(x, x.Field, forExport, seen)...)
		case *ssa.IndexAddr:
			out = append(out, pc.escapesOf(x, field, forExport, seen)...)
		case *ssa.ChangeType:
			out = append(out, pc.escapesOf(x, field, forExport, seen)...)
		case *ssa.Convert:
			out = append(out, pc.escapesOf(x, field, forExport, seen)...)
		case *ssa.UnOp:
			if x.Op == token.MUL && x.X == v {
				// A load through the pointer produces the value of a field,
				// which is a different object.
				continue
			}
			escape(x)
		case *ssa.Store:
			if x.Val != v {
				// A store INTO the object, which does not publish it.
				continue
			}
			if !forExport && pc.protectedPublication(x, x.Addr) {
				continue
			}
			escape(x)
		case *ssa.MapUpdate:
			if x.Value != v {
				continue
			}
			if !forExport && pc.protectedPublication(x, x.Map) {
				continue
			}
			escape(x)
		case *ssa.Send:
			escape(x)
		case *ssa.MakeInterface:
			// An interface value can be stored anywhere, and what it is passed
			// to is usually not analyzable.
			escape(x)
		case *ssa.MakeClosure:
			escape(x)
		case *ssa.Go:
			escape(x)
		case *ssa.Defer:
			escape(x)
		case *ssa.Return:
			// Returning is not a publication: nothing in this function runs
			// afterwards, and only the annotation tells the caller anything.
			continue
		case ssa.CallInstruction:
			if pc.callPublishes(x, v) {
				escape(x)
			}
		case ssa.Instruction:
			// Anything the table does not name publishes: not knowing has to
			// produce a report rather than a silence.
			escape(x)
		}
	}
	return out
}

// protectedPublication reports whether a value put into dst stays unreachable for the rest
// of the critical section it is put there in.
//
// The container's field has to be lock guarded. The lock is then held here, because the
// write to that field is itself checked and would be reported on this very line if it were
// not, so nothing can traverse the container to reach what was put in it.
func (pc *passContext) protectedPublication(inst ssa.Instruction, dst ssa.Value) bool {
	fa, ok := dstFieldAddr(dst)
	if !ok {
		return false
	}
	// The container must be an object this function did not allocate. A container that
	// is itself unpublished has no lock held on it, and its own publication would have
	// to end the freshness of everything inside it, which is a reachability question
	// this analysis does not answer.
	if fs := pc.fresh; fs != nil {
		root, _ := freshRootOf(fa.X)
		if fs.roots[root] {
			return false
		}
	}
	fieldObj, ok := findField(fa.X.Type(), fa.Field)
	if !ok {
		return false
	}
	var lgf lockGuardFacts
	pc.importLockGuardFacts(fieldObj, &lgf)
	return len(lgf.GuardedBy) > 0
}

// dstFieldAddr unwraps a store or map destination to the field it names.
func dstFieldAddr(dst ssa.Value) (*ssa.FieldAddr, bool) {
	for {
		switch x := dst.(type) {
		case *ssa.FieldAddr:
			return x, true
		case *ssa.UnOp:
			dst = x.X
		case *ssa.IndexAddr:
			dst = x.X
		default:
			return nil, false
		}
	}
}

// callPublishes reports whether passing a value to a call makes it reachable elsewhere.
//
// A call this analysis has no summary for publishes everything it is given. That is the
// conservative direction: an interface dispatch, or a function outside the analyzed
// packages, could store the object anywhere.
func (pc *passContext) callPublishes(call ssa.CallInstruction, v ssa.Value) bool {
	common := call.Common()
	callee := common.StaticCallee()
	if callee == nil {
		return true
	}
	obj := funcObject(callee)
	if obj == nil {
		return true
	}
	var ff freshFacts
	if !pc.pass.ImportObjectFact(obj, &ff) || !ff.Analyzed {
		// Nothing is known about this function, so it may do anything with what it
		// is given. An interface dispatch and a function outside the analyzed
		// packages both land here.
		return true
	}
	args := common.Args
	published := false
	for i, arg := range args {
		if arg != v {
			continue
		}
		if hasIndex(ff.Publishes, i) {
			published = true
		}
	}
	return published
}

// blockReachability returns, for every block, the blocks control can get to from it.
func blockReachability(fn *ssa.Function) map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool {
	out := make(map[*ssa.BasicBlock]map[*ssa.BasicBlock]bool, len(fn.Blocks))
	for _, b := range fn.Blocks {
		seen := make(map[*ssa.BasicBlock]bool)
		var walk func(*ssa.BasicBlock)
		walk = func(b *ssa.BasicBlock) {
			for _, succ := range b.Succs {
				if seen[succ] {
					continue
				}
				seen[succ] = true
				walk(succ)
			}
		}
		walk(b)
		out[b] = seen
	}
	return out
}

// freshFunctionFacts reads the two declarations off a function and exports them.
//
// A fresh parameter is named rather than numbered, as every other annotation here names
// what it applies to; the index is what the SSA needs and is resolved once, here.
func (pc *passContext) freshFunctionFacts(d *ast.FuncDecl) {
	var ff freshFacts
	for _, l := range d.Doc.List {
		pc.extractAnnotations(l.Text, map[string]func(string){
			checkLocksReturnsFresh: func(string) { ff.ReturnsFresh = true },
			checkLocksFresh: func(name string) {
				index, ok := parameterIndex(d, strings.TrimSpace(name))
				if !ok {
					pc.maybeFail(d.Pos(), "%s does not name a parameter of %s", strings.TrimSpace(name), d.Name.Name)
					return
				}
				ff.FreshParams = append(ff.FreshParams, index)
			},
		})
	}
	if !ff.ReturnsFresh && len(ff.FreshParams) == 0 {
		return
	}
	funcObj, ok := pc.pass.TypesInfo.Defs[d.Name].(*types.Func)
	if !ok {
		return
	}
	pc.pass.ExportObjectFact(funcObj, &ff)
}

// parameterIndex resolves a parameter name to the index the SSA gives it, counting the
// receiver as zero.
func parameterIndex(d *ast.FuncDecl, name string) (int, bool) {
	index := 0
	if d.Recv != nil {
		for _, f := range d.Recv.List {
			for _, n := range f.Names {
				if n.Name == name {
					return index, true
				}
				index++
			}
			if len(f.Names) == 0 {
				index++
			}
		}
	}
	if d.Type.Params == nil {
		return 0, false
	}
	for _, f := range d.Type.Params.List {
		for _, n := range f.Names {
			if n.Name == name {
				return index, true
			}
			index++
		}
		if len(f.Names) == 0 {
			index++
		}
	}
	return 0, false
}

// computePublishes works out which parameters a function makes reachable elsewhere, and
// reports whether that changed the fact.
func (pc *passContext) computePublishes(fn *ssa.Function) bool {
	obj := funcObject(fn)
	if obj == nil || fn.Blocks == nil {
		return false
	}
	var ff freshFacts
	pc.pass.ImportObjectFact(obj, &ff)

	before := len(ff.Publishes)
	for i, param := range fn.Params {
		if hasIndex(ff.Publishes, i) {
			continue
		}
		if !isPointerLike(param.Type()) {
			continue
		}
		for _, e := range pc.escapesOf(param, allFields, true /* forExport */, make(map[ssa.Value]bool)) {
			if !pc.publishesObject(param, e) {
				continue
			}
			ff.Publishes = append(ff.Publishes, i)
			break
		}
	}
	if len(ff.Publishes) == before {
		return false
	}
	pc.pass.ExportObjectFact(obj, &ff)
	return true
}

// publishesObject reports whether an escape makes the object itself reachable, rather than
// one field of it.
//
// A pointer to a field is not a handle on the object, so passing one on says nothing about
// the rest of it. It does say something about that field, which is why a guarded field
// counts: a goroutine holding the address of one can race with a write to it. The lock
// itself is not a guarded field, which is what lets a constructor take its own lock.
func (pc *passContext) publishesObject(root ssa.Value, e escapePoint) bool {
	if e.field == allFields {
		return true
	}
	fieldObj, ok := findField(root.Type(), e.field)
	if !ok {
		return true
	}
	var lgf lockGuardFacts
	pc.importLockGuardFacts(fieldObj, &lgf)
	return len(lgf.GuardedBy) > 0
}

// seedPublishes records that a function's body is available to be analyzed, which is what
// separates "publishes nothing" from "nothing is known".
//
// The fixpoint below starts from there and grows: a package's functions begin as publishing
// nothing and are found out one round at a time, which is the least answer consistent with
// the bodies. Starting from the other end, where an unvisited callee publishes everything,
// would fix the first round's guess forever, since the sets only grow.
func (pc *passContext) seedPublishes(fn *ssa.Function) {
	obj := funcObject(fn)
	if obj == nil || fn.Blocks == nil {
		return
	}
	var ff freshFacts
	pc.pass.ImportObjectFact(obj, &ff)
	ff.Analyzed = true
	pc.pass.ExportObjectFact(obj, &ff)
}

// isPointerLike reports whether a value of this type can carry a reference to an object.
func isPointerLike(t types.Type) bool {
	switch types.Unalias(t).Underlying().(type) {
	case *types.Pointer, *types.Interface, *types.Map, *types.Slice, *types.Chan, *types.Signature:
		return true
	}
	return false
}

// checkFreshReturns checks the declaration that a function returns an unpublished object.
//
// This is where the whole arrangement stops being an assertion: a call site trusts the
// annotation, so the annotation is verified here, against the same rules that decide
// freshness anywhere else.
func (pc *passContext) checkFreshReturns(fn *ssa.Function) {
	obj := funcObject(fn)
	if obj == nil || fn.Blocks == nil {
		return
	}
	var ff freshFacts
	if !pc.pass.ImportObjectFact(obj, &ff) || !ff.ReturnsFresh {
		return
	}
	fs := pc.computeFresh(fn)
	for _, b := range fn.Blocks {
		ret, ok := b.Instrs[len(b.Instrs)-1].(*ssa.Return)
		if !ok {
			continue
		}
		for _, res := range ret.Results {
			if !isPointerLike(res.Type()) {
				continue
			}
			if fs.isFreshAt(res, ret, allFields) {
				continue
			}
			pc.maybeFail(ret.Pos(), "%s returns an object that is not fresh, but is declared to: it is either not allocated here nor from a constructor declaring one, or it is published before the return", fn.Name())
		}
	}
}

// fieldIndexOf returns the index of the accessed field within the object it belongs to, or
// allFields when it cannot be told, which is the safe answer: it asks about the object.
func fieldIndexOf(accessObj types.Object, from ssa.Value) int {
	v, ok := accessObj.(*types.Var)
	if !ok {
		return allFields
	}
	st, ok := resolveStruct(from.Type())
	if !ok {
		return allFields
	}
	for i := 0; i < st.NumFields(); i++ {
		if st.Field(i) == v {
			return i
		}
	}
	return allFields
}

// freshArgument reports whether a call's lock precondition is rooted at an argument that is
// an object nothing else can reach yet.
func (pc *passContext) freshArgument(call callCommon, fg functionGuardInfo) bool {
	pg, ok := fg.Resolver.(*parameterGuard)
	if !ok {
		return false
	}
	inst, ok := call.(ssa.CallInstruction)
	if !ok {
		return false
	}
	common := call.Common()
	args := common.Args
	if common.Method != nil {
		args = append([]ssa.Value{common.Value}, args...)
	}
	if pg.Index >= len(args) {
		return false
	}
	return pc.fresh.isFreshAt(args[pg.Index], inst, allFields)
}

// checkFreshArgs checks, at a call site, that what is passed to a parameter declared to
// arrive unpublished is an object nothing else can reach yet.
func (pc *passContext) checkFreshArgs(call ssa.CallInstruction) {
	common := call.Common()
	callee := common.StaticCallee()
	if callee == nil {
		return
	}
	obj := funcObject(callee)
	if obj == nil {
		return
	}
	var ff freshFacts
	if !pc.pass.ImportObjectFact(obj, &ff) || len(ff.FreshParams) == 0 {
		return
	}
	for _, index := range ff.FreshParams {
		if index >= len(common.Args) {
			continue
		}
		arg := common.Args[index]
		if pc.fresh.isFreshAt(arg, call, allFields) {
			continue
		}
		pc.maybeFail(call.Pos(), "%s requires an unpublished object for parameter %d, and this one is not provably unpublished here", callee.Name(), index)
	}
}
