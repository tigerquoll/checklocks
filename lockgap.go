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

// The lockgap analyzer reports a lock that is released and taken again inside one function.
//
// A critical section is a promise that what was true at the top of it is still true at the
// bottom. Dropping the lock in the middle and picking it up again ends that promise without
// ending the section: every invariant established before the release has to be established
// again afterwards, and the code that follows usually reads as though it were still holding
// what it had. The window is invisible at the call site, which is where the caller's own
// invariants are.
//
// Two shapes, one analysis, both about the same window:
//
//   - A function that is given a lock and drops it. The caller's critical section is split
//     by a callee, and nothing at the call site says so.
//   - A lock released and taken again in one body, including the case where what is taken
//     back is stronger than what was dropped: a read lock released and a write lock taken
//     is the classic check-then-act, and the state read under the first is stale under the
//     second.
//
// It is off by default. A gap is sometimes deliberate — a long wait that must not be done
// under the lock is the usual reason — and those are worth reviewing rather than failing a
// build over on the day the analysis arrives.
//
// +checkalignedignore
package checklocks

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const (
	// lockGapIgnore suppresses the checks in a function or on a single line.
	lockGapIgnore = "// +lockgapignore"

	// lockGapFail records an expected diagnostic in the test corpus.
	lockGapFail = "// +lockgapfail"
)

// lockGapAnnotations is the self-check annotation set belonging to this analyzer.
//
// There is no force annotation: what this reports is a shape in the body, and forcing it
// would mean asserting that a release did not happen.
var lockGapAnnotations = annotationSet{
	fail:   lockGapFail,
	ignore: lockGapIgnore,
}

// enableLockGap is the -lockgap.enable flag.
//
// The analysis ships off. A registered analyzer has no off state in the multi analyzer
// binary — it runs and reports, or it is disabled by name on the command line — so an
// analysis that should not gate a build the day it arrives carries its own switch. This is
// the arrangement the hierarchical ordering check uses for the same reason.
var enableLockGap = false

func init() {
	LockGapAnalyzer.Flags.BoolVar(&enableLockGap, "enable", false,
		"report locks released and taken again within one function")
}

// LockGapAnalyzer reports a lock released and taken again inside one function.
var LockGapAnalyzer = &analysis.Analyzer{
	Name:     "lockgap",
	Doc:      "checks for a lock released and taken again within one function",
	Run:      runLockGap,
	Requires: []*analysis.Analyzer{buildssa.Analyzer, Analyzer},
}

// lockGapContext carries the per pass state.
type lockGapContext struct {
	*expectations
	pass *analysis.Pass
}

// runLockGap is the entrypoint for the lockgap analyzer.
func runLockGap(pass *analysis.Pass) (any, error) {
	if !enableLockGap {
		return nil, nil
	}
	pc := &lockGapContext{
		expectations: newExpectations(pass, lockGapAnnotations, true /* reportInvalidPos */),
		pass:         pass,
	}
	pc.extractLineFailures()

	state := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	for _, fn := range state.SrcFuncs {
		pc.checkFunction(fn)
	}

	pc.checkFailures()
	return nil, nil
}

// lockKey identifies the lock a call operates on.
//
// A lock is the object it belongs to and the way in to it, which is what makes two calls
// operate on the same lock: x.mu.Lock() and x.mu.Unlock() reach the same field of the same
// value, while y.mu.Unlock() reaches another object's. The path is kept as the field indices
// walked, so a lock behind more than one field is still one lock.
type lockKey struct {
	root ssa.Value
	path string
}

// lockOperation is one lock call in a function.
type lockOperation struct {
	key lockKey
	op  lockOp

	// inst is where the call is written.
	inst ssa.Instruction

	// at is where it takes effect, which is the call itself except for a deferred
	// acquisition: that one runs at the return, and is reached only if the defer
	// statement was.
	at ssa.Instruction

	// read is a read side operation: RLock or RUnlock.
	read bool

	// name is the method called, for the diagnostic.
	name string
}

// checkFunction reports the gaps in one function.
func (pc *lockGapContext) checkFunction(fn *ssa.Function) {
	if fn.Blocks == nil {
		return
	}
	if pc.functionIgnored(fn) {
		return
	}
	ops := pc.lockOperations(fn)
	if len(ops) == 0 {
		return
	}
	order := instructionOrder(fn)

	// The locks the function is given, which it is expected to hand back the way it got
	// them. A release of one of those is the caller's critical section being split.
	entry := pc.heldOnEntryKeys(fn)

	for i, rel := range ops {
		if rel.op != opRelease {
			continue
		}
		// What is taken again after this release, if anything.
		var reacquire *lockOperation
		for j := range ops {
			acq := &ops[j]
			if acq.op != opAcquire || acq.key != rel.key || j == i {
				continue
			}
			if !reaches(rel.at, acq.at, order) {
				continue
			}
			reacquire = acq
			break
		}
		heldOnEntry := entry[rel.key]

		switch {
		case reacquire != nil && heldOnEntry:
			pc.maybeFail(rel.inst.Pos(), "%s releases a lock its caller holds and takes it again at %s: the caller's critical section is split, and every invariant it established before the call has to hold again after it",
				fn.Name(), pc.position(reacquire.inst))
		case reacquire != nil:
			pc.maybeFail(rel.inst.Pos(), "%s is released here and taken again at %s: %s",
				rel.name, pc.position(reacquire.inst), gapConsequence(rel, reacquire))
		case heldOnEntry:
			pc.maybeFail(rel.inst.Pos(), "%s releases a lock its caller holds and does not take it back: the caller's critical section ends here, and nothing at the call site says so",
				fn.Name())
		}
	}
}

// gapConsequence says what the window between a release and the next acquisition means.
func gapConsequence(rel lockOperation, acq *lockOperation) string {
	if rel.read && !acq.read {
		return "what was read under the read lock may have changed before the write lock was taken, so anything decided from it is stale"
	}
	return "whatever the first critical section established has to be established again, since anything could have run in between"
}

// position renders an instruction's position for a diagnostic.
func (pc *lockGapContext) position(inst ssa.Instruction) string {
	p := pc.pass.Fset.Position(inst.Pos())
	if !p.IsValid() {
		return "a later point in the function"
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// lockOperations collects the lock calls of a function, in the order they are written.
//
// A call inside a function started with "go" belongs to the new goroutine, which has a lock
// state of its own; the closure is walked separately, as its own function.
func (pc *lockGapContext) lockOperations(fn *ssa.Function) []lockOperation {
	var out []lockOperation
	for _, b := range fn.Blocks {
		for _, inst := range b.Instrs {
			var (
				call     ssa.CallInstruction
				deferred bool
			)
			switch t := inst.(type) {
			case *ssa.Go:
				continue
			case *ssa.Defer:
				call, deferred = t, true
			case ssa.CallInstruction:
				call = t
			default:
				continue
			}
			callee := call.Common().StaticCallee()
			obj := funcObject(callee)
			if obj == nil {
				continue
			}
			op := lockOpName(obj.Name())
			if op == opNone {
				continue
			}
			recv, ok := receiverOf(call)
			if !ok {
				continue
			}
			if !isStandardLock(pc.pass, obj) && !pc.isLockWrapper(obj) {
				continue
			}
			key, ok := lockKeyOf(recv)
			if !ok {
				continue
			}
			if deferred && op == opRelease {
				// A release that runs at the return cannot open a window inside
				// the body: what follows it is the end of the function. The
				// universal "take it, then defer the release" is not a gap, and
				// treating the defer statement as the release would make it one.
				continue
			}
			at := inst
			if deferred {
				// A deferred acquisition runs at the return, and runs at all
				// only if control reached the defer statement. That is what the
				// defer statement dominates, so it is what the reachability
				// question is asked about.
				at = inst
			}
			out = append(out, lockOperation{
				key:  key,
				op:   op,
				inst: inst,
				at:   at,
				read: obj.Name() == "RLock" || obj.Name() == "RUnlock",
				name: lockDisplayName(recv, obj),
			})
		}
	}
	return out
}

// isLockWrapper reports whether a method is a lock operation on a type that declares itself
// a lock, or on one that wraps a lock and forwards to it.
func (pc *lockGapContext) isLockWrapper(obj *types.Func) bool {
	if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil {
		if _, declared := lockPrimitiveIn(pc.pass, sig.Recv().Type()); declared {
			return true
		}
	}
	return false
}

// lockDisplayName renders the lock a call operates on.
func lockDisplayName(recv ssa.Value, obj *types.Func) string {
	if fa, ok := underlyingFieldAddr(recv); ok {
		if _, fieldName, ok := ownerOf(fa); ok {
			return "the " + fieldName + " lock"
		}
	}
	if sig, ok := obj.Type().(*types.Signature); ok && sig.Recv() != nil {
		return "the " + shortType(sig.Recv().Type()) + " lock"
	}
	return "the lock"
}

// lockKeyOf identifies the lock a call's receiver names.
func lockKeyOf(v ssa.Value) (lockKey, bool) {
	path := ""
	for {
		switch t := v.(type) {
		case *ssa.FieldAddr:
			path = fmt.Sprintf(".%d%s", t.Field, path)
			v = t.X
		case *ssa.Field:
			path = fmt.Sprintf(".%d%s", t.Field, path)
			v = t.X
		case *ssa.ChangeType:
			v = t.X
		case *ssa.Convert:
			v = t.X
		case *ssa.UnOp:
			if t.Op != token.MUL {
				return lockKey{}, false
			}
			v = t.X
		default:
			return lockKey{root: v, path: path}, true
		}
	}
}

// heldOnEntryKeys returns the locks a function is given and is expected to hand back the
// way it got them.
//
// A lock the function declares it RELEASES is not one of them. The split is declared there,
// the caller is told about it, and the call site is checked against the declaration; there
// is nothing hidden left to report. The declaration has to be read from the source, because
// a lock that must be held on entry and a lock that is released before returning are the
// same entry in the facts: both say the caller has to be holding it.
func (pc *lockGapContext) heldOnEntryKeys(fn *ssa.Function) map[lockKey]bool {
	out := make(map[lockKey]bool)
	obj := funcObject(fn)
	if obj == nil {
		return out
	}
	var lff lockFunctionFacts
	if !pc.pass.ImportObjectFact(obj, &lff) {
		return out
	}
	handed := pc.declaredHandovers(obj)
	for name, fg := range lff.HeldOnEntry {
		if handed[name] {
			continue
		}
		pg, ok := fg.Resolver.(*parameterGuard)
		if !ok || pg.Index >= len(fn.Params) {
			continue
		}
		out[lockKey{root: fn.Params[pg.Index], path: fieldListPath(pg.FieldList)}] = true
	}
	return out
}

// fieldListPath renders a guard's traversal path the way lockKeyOf renders one.
func fieldListPath(fl fieldList) string {
	path := ""
	for _, entry := range fl {
		switch f := entry.(type) {
		case *fieldStruct:
			path += fmt.Sprintf(".%d", f.Field)
		case *fieldStructPtr:
			path += fmt.Sprintf(".%d", f.Field)
		default:
			return path
		}
	}
	return path
}

// functionIgnored reports whether reporting is suppressed for a whole function.
func (pc *lockGapContext) functionIgnored(fn *ssa.Function) bool {
	ignore, _ := pc.functionAnnotations(fn)
	return ignore
}

// declaredHandovers returns the guards a function declares it releases before it returns.
func (pc *lockGapContext) declaredHandovers(obj *types.Func) map[string]bool {
	_, handed := pc.functionAnnotationsFor(obj)
	return handed
}

// functionAnnotations reads the annotations of a function that this analysis acts on.
func (pc *lockGapContext) functionAnnotations(fn *ssa.Function) (ignore bool, handed map[string]bool) {
	obj := funcObject(fn)
	if obj == nil {
		return false, nil
	}
	return pc.functionAnnotationsFor(obj)
}

// functionAnnotationsFor reads them from the declaration of a function object.
func (pc *lockGapContext) functionAnnotationsFor(obj *types.Func) (ignore bool, handed map[string]bool) {
	handed = make(map[string]bool)
	for _, f := range pc.pass.Files {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Doc == nil {
				continue
			}
			if pc.pass.TypesInfo.Defs[fd.Name] != types.Object(obj) {
				continue
			}
			for _, l := range fd.Doc.List {
				extractAnnotations(l.Text, map[string]func(string){
					lockGapIgnore:          func(string) { ignore = true },
					checkLocksReleases:     func(name string) { handed[name] = true },
					checkLocksReleasesRead: func(name string) { handed[name] = true },
				})
			}
		}
	}
	return ignore, handed
}

// instructionOrder numbers the instructions of a function within their blocks.
func instructionOrder(fn *ssa.Function) map[ssa.Instruction]int {
	out := make(map[ssa.Instruction]int)
	for _, b := range fn.Blocks {
		for i, inst := range b.Instrs {
			out[inst] = i
		}
	}
	return out
}

// reaches reports whether the second operation is reached from the first on every path that
// reaches it at all.
//
// Dominance is the question rather than reachability, and that is what keeps a loop out of
// this. A lock taken and released each time round is a whole critical section per
// iteration, not a window in one: the release in the body does not dominate the acquisition
// at the head, because the head is also reached from outside the loop. A release and an
// acquisition on a straight path, where the second cannot happen without the first, is the
// window this reports.
//
// The cost is a real gap on a path that is only one of several being missed, which is the
// conservative direction for an analysis that is about to be pointed at a code base for the
// first time.
func reaches(from, to ssa.Instruction, order map[ssa.Instruction]int) bool {
	fb, tb := from.Block(), to.Block()
	if fb == nil || tb == nil {
		return false
	}
	if fb == tb {
		return order[from] < order[to]
	}
	return fb.Dominates(tb)
}
