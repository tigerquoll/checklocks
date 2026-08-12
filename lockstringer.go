// Copyright 2026 The gVisor Authors.
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
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/ssa"
)

const (
	lockStringerIgnore = "// +lockstringerignore"
	lockStringerFail   = "// +lockstringerfail"
)

// lockStringerAnnotations is the self-check annotation set for this analyzer.
//
// There is no force annotation: forcing means asserting that a lock is held,
// and this analysis is about the caller's lock state being unknowable.
var lockStringerAnnotations = annotationSet{
	fail:   lockStringerFail,
	ignore: lockStringerIgnore,
}

// LockStringerAnalyzer reports lock hazards in lazily evaluated methods.
//
// It requires the checklocks analyzer, whose guard facts say which fields are
// protected by a lock. Facts are inherited from a required analyzer, so this
// does not declare them itself; a fact type may be declared by only one
// analyzer in a binary.
var LockStringerAnalyzer = &analysis.Analyzer{
	Name:     "lockstringer",
	Doc:      "checks for lock hazards in lazily evaluated methods such as String",
	Run:      runLockStringer,
	Requires: []*analysis.Analyzer{buildssa.Analyzer, Analyzer},
}

// lazyMethods are the methods that a formatter, encoder or logger calls on a
// value at a point the value's author does not control.
//
// The signature is checked as well as the name, so that an ordinary helper
// that happens to be called String is not mistaken for a fmt.Stringer.
var lazyMethods = map[string]func(*types.Signature) bool{
	"String":      func(s *types.Signature) bool { return resultsAre(s, 0, "string") },
	"Error":       func(s *types.Signature) bool { return resultsAre(s, 0, "string") },
	"MarshalJSON": func(s *types.Signature) bool { return resultsAre(s, 0, "[]byte", "error") },
	// zapcore.ObjectMarshaler. The encoder type cannot be named here
	// without depending on zap, so the shape is matched instead.
	"MarshalLogObject": func(s *types.Signature) bool { return resultsAre(s, 1, "error") },
}

// extraLazyMethods is the -lockstringer.methods flag.
//
// Names given here are matched without a signature check, since the shape of a
// project specific lazy interface is not known.
var extraLazyMethods string

func init() {
	LockStringerAnalyzer.Flags.StringVar(&extraLazyMethods, "methods", "",
		"comma separated additional method names to treat as lazily evaluated")
}

// resultsAre reports whether the signature takes params arguments and returns
// exactly the given types.
func resultsAre(s *types.Signature, params int, want ...string) bool {
	if s.Params().Len() != params || s.Results().Len() != len(want) {
		return false
	}
	for i, w := range want {
		if s.Results().At(i).Type().String() != w {
			return false
		}
	}
	return true
}

// isLazyMethod reports whether fn is evaluated lazily by a formatter, encoder
// or logger, along with the name to use in diagnostics.
func isLazyMethod(fn *ssa.Function) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	name := fn.Name()
	if check, ok := lazyMethods[name]; ok {
		return check(fn.Signature)
	}
	for _, extra := range strings.Split(extraLazyMethods, ",") {
		if extra != "" && extra == name {
			return true
		}
	}
	return false
}

// runLockStringer is the entrypoint for the lockstringer analyzer.
func runLockStringer(pass *analysis.Pass) (any, error) {
	// N.B. passContext is reused for its fact importers. The parts that
	// belong to the checklocks analysis are simply unused here.
	pc := &passContext{
		expectations: newExpectations(pass, lockStringerAnnotations, true /* reportInvalidPos */),
		pass:         pass,
	}
	pc.extractLineFailures()

	ignored := ignoredFunctions(pass)
	state := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	for _, fn := range state.SrcFuncs {
		if !isLazyMethod(fn) {
			continue
		}
		if obj := fn.Object(); obj != nil {
			if _, ok := ignored[obj]; ok {
				continue
			}
		}
		pc.checkLazyMethod(fn)
	}

	pc.checkFailures()
	return nil, nil
}

// ignoredFunctions collects the functions carrying a function level ignore.
//
// A line level ignore is handled by the expectations, which drop a diagnostic
// reported on that line; a function level one has to suppress diagnostics
// reported anywhere in the body, so it is resolved to the object here.
func ignoredFunctions(pass *analysis.Pass) map[types.Object]struct{} {
	ignored := make(map[types.Object]struct{})
	for _, f := range pass.Files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.FuncDecl)
			if !ok || d.Doc == nil {
				continue
			}
			for _, c := range d.Doc.List {
				if !strings.HasPrefix(c.Text, lockStringerIgnore) {
					continue
				}
				if obj, ok := pass.TypesInfo.Defs[d.Name]; ok && obj != nil {
					ignored[obj] = struct{}{}
				}
			}
		}
	}
	return ignored
}

// suggestion is appended to every diagnostic: both hazards have the same fix.
const suggestion = "restrict it to fields fixed at construction, or log a snapshot taken by the caller under the lock"

// checkLazyMethod applies both rules to a single lazily evaluated method.
func (pc *passContext) checkLazyMethod(fn *ssa.Function) {
	structType, ok := resolveStruct(fn.Signature.Recv().Type())
	if !ok {
		return
	}

	// The analysis applies to types that have taken a position on locking:
	// a lock to hold, and at least one field annotated as guarded. Without
	// the annotations there is nothing to say which reads are racy.
	locks := pc.lockFields(structType)
	if len(locks) == 0 || !pc.hasGuardedField(structType) {
		return
	}

	recvName := receiverTypeName(fn)

	// Rule (b). Acquiring the receiver's own lock is a hazard because the
	// value may be formatted by a caller that already holds it, which
	// deadlocks on a non-reentrant lock.
	//
	// This is checked first, and suppresses rule (a): when the method
	// locks, its reads are guarded, and reporting them as racy as well
	// would describe the same code two contradictory ways. The remedy is
	// the same either way.
	if site, via, ok := pc.acquiresOwnLock(fn, structType, locks); ok {
		if via != "" {
			pc.maybeFail(site, "%s.%s may be evaluated while %s's lock is held, but %s acquires it: self-deadlock; %s",
				recvName, fn.Name(), recvName, via, suggestion)
		} else {
			pc.maybeFail(site, "%s.%s may be evaluated while %s's lock is held, but acquires it: self-deadlock; %s",
				recvName, fn.Name(), recvName, suggestion)
		}
		return
	}

	// Rule (a). A guarded field read with no lock held races, and the
	// caller's lock state is not knowable from here.
	//
	// Reads are grouped by line, since a single format call commonly reads
	// several guarded fields and one diagnostic naming all of them is more
	// use than one per field.
	var order []positionKey
	pos := make(map[positionKey]token.Pos)
	fields := make(map[positionKey][]string)
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			obj, at, ok := guardedFieldAccess(pc, inst, structType)
			if !ok {
				continue
			}
			key := pc.positionKey(at)
			if _, seen := fields[key]; !seen {
				order = append(order, key)
				pos[key] = at
			}
			if !slices.Contains(fields[key], obj.Name()) {
				fields[key] = append(fields[key], obj.Name())
			}
		}
	}
	for _, key := range order {
		names := fields[key]
		isAre := "is"
		if len(names) > 1 {
			isAre = "are"
		}
		pc.maybeFail(pos[key], "%s.%s reads %s, which %s lock-guarded, and is evaluated under unknown caller lock state: guarded read races; %s",
			recvName, fn.Name(), strings.Join(names, ", "), isAre, suggestion)
	}
}

// receiverTypeName returns the name of fn's receiver type for diagnostics.
func receiverTypeName(fn *ssa.Function) string {
	typ := fn.Signature.Recv().Type()
	if ptr, ok := typ.Underlying().(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	if named, ok := types.Unalias(typ).(*types.Named); ok {
		return named.Obj().Name()
	}
	return typ.String()
}

// lockFields returns the indices of the fields of structType that are locks.
func (pc *passContext) lockFields(structType *types.Struct) map[int]types.Object {
	locks := make(map[int]types.Object)
	for i := 0; i < structType.NumFields(); i++ {
		field := structType.Field(i)
		if isLockTypeIn(pc.pass, field.Type()) {
			locks[i] = field
		}
	}
	return locks
}

// hasGuardedField reports whether any field of structType is lock-guarded.
func (pc *passContext) hasGuardedField(structType *types.Struct) bool {
	for i := 0; i < structType.NumFields(); i++ {
		var lgf lockGuardFacts
		pc.importLockGuardFacts(structType.Field(i), &lgf)
		if len(lgf.GuardedBy) > 0 {
			return true
		}
	}
	return false
}

// guardedFieldAccess returns the guarded field an instruction accesses, if the
// field belongs to structType.
//
// Only the receiver's own type is considered. A guarded field reached through
// some other object is a plain violation, which the checklocks analysis
// already reports, and the advice here would not apply to it.
func guardedFieldAccess(pc *passContext, inst ssa.Instruction, structType *types.Struct) (types.Object, token.Pos, bool) {
	var (
		x     ssa.Value
		index int
	)
	switch i := inst.(type) {
	case *ssa.Field:
		x, index = i.X, i.Field
	case *ssa.FieldAddr:
		x, index = i.X, i.Field
	default:
		return nil, token.NoPos, false
	}
	owner, ok := resolveStruct(x.Type())
	if !ok || owner != structType {
		return nil, token.NoPos, false
	}
	obj, ok := findField(x.Type(), index)
	if !ok {
		return nil, token.NoPos, false
	}
	var lgf lockGuardFacts
	pc.importLockGuardFacts(obj, &lgf)
	if len(lgf.GuardedBy) == 0 {
		return nil, token.NoPos, false
	}
	return obj, inst.(interface{ Pos() token.Pos }).Pos(), true
}

// acquiresOwnLock reports whether fn acquires one of the receiver type's own
// locks, returning the site and the name of the callee responsible.
//
// Three shapes are recognised: the method locks directly; it calls another
// method of the same type that locks directly; or it calls a function that an
// annotation says acquires a lock. The nesting is deliberately shallow. A
// self-locking accessor is the shape that occurs in practice, and going deeper
// needs the summary facts that the lock ordering analysis will introduce.
func (pc *passContext) acquiresOwnLock(fn *ssa.Function, structType *types.Struct, locks map[int]types.Object) (token.Pos, string, bool) {
	if pos, ok := pc.directlyAcquires(fn, structType, locks); ok {
		return pos, "", true
	}
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			call, ok := inst.(ssa.CallInstruction)
			if !ok {
				continue
			}
			callee, ok := call.Common().Value.(*ssa.Function)
			if !ok || callee == fn || callee.Blocks == nil {
				continue
			}
			// Another method of the same type that locks.
			if callee.Signature.Recv() != nil {
				if owner, ok := resolveStruct(callee.Signature.Recv().Type()); ok && owner == structType {
					if _, ok := pc.directlyAcquires(callee, structType, locks); ok {
						return call.Common().Pos(), callee.Name(), true
					}
				}
			}
			// A callee annotated as acquiring a lock.
			obj := callee.Object()
			if obj == nil {
				continue
			}
			funcObj, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			var lff lockFunctionFacts
			pc.importLockFunctionFacts(funcObj, &lff)
			for name := range lff.HeldOnExit {
				if _, held := lff.HeldOnEntry[name]; !held {
					return call.Common().Pos(), callee.Name(), true
				}
			}
		}
	}
	return token.NoPos, "", false
}

// directlyAcquires reports whether fn contains an acquisition of one of the
// given lock fields of structType.
func (pc *passContext) directlyAcquires(fn *ssa.Function, structType *types.Struct, locks map[int]types.Object) (token.Pos, bool) {
	for _, block := range fn.Blocks {
		for _, inst := range block.Instrs {
			call, ok := inst.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := call.Common()
			if !isLockAcquire(pc.pass, common) {
				continue
			}
			// See checkFunctionCall: an interface dispatch carries
			// the receiver in Value rather than in Args.
			args := common.Args
			if common.Method != nil {
				args = append([]ssa.Value{common.Value}, args...)
			}
			if len(args) == 0 {
				continue
			}
			if isLockField(args[0], structType, locks) {
				return common.Pos(), true
			}
		}
	}
	return token.NoPos, false
}

// isLockAcquire reports whether the call acquires a lock.
func isLockAcquire(pass *analysis.Pass, common *ssa.CallCommon) bool {
	var fn *types.Func
	if common.Method != nil {
		fn = common.Method
	} else if sf, ok := common.Value.(*ssa.Function); ok && sf.Object() != nil {
		fn, _ = sf.Object().(*types.Func)
	}
	if fn == nil {
		return false
	}
	switch fn.Name() {
	case "Lock", "RLock", "NestedLock":
	default:
		return false
	}
	full := fn.FullName()
	if rwMutexRE.MatchString(full) || mutexRE.MatchString(full) || lockerRE.MatchString(full) {
		return true
	}
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		_, declared := lockPrimitiveIn(pass, sig.Recv().Type())
		return declared
	}
	return false
}

// isLockField reports whether v is one of the lock fields of structType.
func isLockField(v ssa.Value, structType *types.Struct, locks map[int]types.Object) bool {
	for {
		switch x := v.(type) {
		case *ssa.ChangeType:
			v = x.X
		case *ssa.Convert:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return false
			}
			v = x.X
		case *ssa.FieldAddr:
			owner, ok := resolveStruct(x.X.Type())
			if !ok || owner != structType {
				return false
			}
			_, ok = locks[x.Field]
			return ok
		case *ssa.Field:
			owner, ok := resolveStruct(x.X.Type())
			if !ok || owner != structType {
				return false
			}
			_, ok = locks[x.Field]
			return ok
		default:
			return false
		}
	}
}
