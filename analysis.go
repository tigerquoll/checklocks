// Copyright 2020 The gVisor Authors.
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
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"slices"
	"strings"

	"golang.org/x/tools/go/ssa"
)

func gcd(a, b atomicAlignment) atomicAlignment {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// typeAlignment returns the type alignment for the given type.
func (pc *passContext) typeAlignment(pkg *types.Package, obj types.Object) atomicAlignment {
	requiredOffset := atomicAlignment(1)
	if pc.pass.ImportObjectFact(obj, &requiredOffset) {
		return requiredOffset
	}

	switch x := obj.Type().Underlying().(type) {
	case *types.Struct:
		fields := make([]*types.Var, x.NumFields())
		for i := 0; i < x.NumFields(); i++ {
			fields[i] = x.Field(i)
		}
		offsets := pc.pass.TypesSizes.Offsetsof(fields)
		for i := 0; i < x.NumFields(); i++ {
			// Check the offset, and then assuming that this offset
			// aligns with the offset for the broader type.
			fieldRequired := pc.typeAlignment(pkg, fields[i])
			if offsets[i]%int64(fieldRequired) != 0 {
				// The offset of this field is not compatible.
				pc.maybeFail(fields[i].Pos(), "have alignment %d, need %d", offsets[i], fieldRequired)
			}
			// Ensure the requiredOffset is the LCM of the offset.
			requiredOffset *= fieldRequired / gcd(requiredOffset, fieldRequired)
		}
	case *types.Array:
		// Export direct alignment requirements.
		if named, ok := types.Unalias(x.Elem()).(*types.Named); ok && !hasTypeParams(named) {
			requiredOffset = pc.typeAlignment(pkg, named.Obj())
		}
	default:
		// Use the compiler's underlying alignment.
		requiredOffset = atomicAlignment(pc.pass.TypesSizes.Alignof(obj.Type().Underlying()))
	}

	if pkg == obj.Pkg() {
		// Cache as an object fact, to subsequent calls. Note that we
		// can only export object facts for the package that we are
		// currently analyzing. There may be no exported facts for
		// array types or alias types, for example.
		pc.pass.ExportObjectFact(obj, &requiredOffset)
	}

	return requiredOffset
}

// hasTypeParams returns true iff the named type has type parameters.
func hasTypeParams(typ *types.Named) bool {
	return typ.TypeParams() != nil && typ.TypeParams().Len() > 0
}

// checkTypeAlignment checks the alignment of the given type.
//
// This calls typeAlignment, which resolves all types recursively. This method
// should be called for all types individual to ensure full coverage.
func (pc *passContext) checkTypeAlignment(pkg *types.Package, typ *types.Named) {
	if !hasTypeParams(typ) {
		_ = pc.typeAlignment(pkg, typ.Obj())
	}
}

// atomicRules specify read constraints.
type atomicRules int

const (
	nonAtomic atomicRules = iota
	readWriteAtomic
	readOnlyAtomic
	mixedAtomic
)

// checkAtomicCall checks for an atomic access.
//
// inst is the instruction analyzed, object names the field in the decision log.
func (pc *passContext) checkAtomicCall(inst ssa.Instruction, object string, ar atomicRules, context string) {
	checked, ok, forceable, msg := pc.atomicVerdict(inst, ar)
	if checked == nil {
		return
	}
	pc.decide(decisionAt{inst: checked, pos: checked.Pos(), context: context}, atomicAccess, ok, object)
	if ok {
		return
	}
	if forceable {
		if _, forced := pc.forced[pc.positionKey(checked.Pos())]; forced {
			pc.suppressed()
			return
		}
	}
	if msg == "" {
		pc.maybeFail(checked.Pos(), "illegal use of atomic-only field by %T instruction", checked)
		return
	}
	pc.maybeFail(checked.Pos(), "%s", msg)
}

// atomicVerdict says whether an atomic access is legal. It returns the instruction the
// verdict is about, nil when the access cannot fail, and whether a force silences a failure.
// A failure with no message is an illegal use of the field by that instruction.
func (pc *passContext) atomicVerdict(inst ssa.Instruction, ar atomicRules) (checked ssa.Instruction, ok, forceable bool, msg string) {
	switch x := inst.(type) {
	case *ssa.Call:
		if x.Common().IsInvoke() {
			if ar != nonAtomic {
				// This is an illegal interface dispatch.
				return inst, false, false, "dynamic dispatch with atomic-only field"
			}
			return nil, true, false, ""
		}
		fn, ok := x.Common().Value.(*ssa.Function)
		if !ok {
			if ar != nonAtomic {
				// This is an illegal call to a non-static function.
				return inst, false, false, "dispatch to non-static function with atomic-only field"
			}
			return nil, true, false, ""
		}
		pkg := fn.Package()
		if pkg == nil {
			if ar != nonAtomic {
				// This is a call to some shared wrapper function.
				return inst, false, false, "dispatch to shared function or wrapper"
			}
			return nil, true, false, ""
		}
		var lff lockFunctionFacts // Check for exemption.
		if obj := fn.Object(); obj != nil && pc.pass.ImportObjectFact(obj, &lff) && lff.Ignore {
			if ar != nonAtomic {
				return inst, true, false, ""
			}
			return nil, true, false, ""
		}
		if name := pkg.Pkg.Name(); name != "atomic" && name != "atomicbitops" {
			if ar != nonAtomic {
				// This is an illegal call to a non-atomic package function.
				return inst, false, false, "dispatch to non-atomic function with atomic-only field"
			}
			return nil, true, false, ""
		}
		if ar == nonAtomic {
			if fn.Signature.Recv() != nil {
				// Always allow calls to methods of atomic wrappers such as
				// atomic.Int32 introduced in Go 1.19 when no annotation exists.
				return nil, true, false, ""
			}
			// We are *not* expecting an atomic dispatch.
			return inst, false, true, "unexpected call to atomic function"
		}
		if !strings.HasPrefix(fn.Name(), "Load") && ar == readOnlyAtomic {
			// We are not allowing any reads in this context.
			return inst, false, true, "unexpected call to atomic write function, is a lock missing?"
		}
		return inst, true, false, ""
	case *ssa.ChangeType:
		// Allow casts for atomic values, but nothing else.
		if refs := x.Referrers(); refs != nil && len(*refs) == 1 {
			return pc.atomicVerdict((*refs)[0], ar)
		}
	case *ssa.UnOp:
		if x.Op == token.MUL && ar == mixedAtomic {
			// This is allowed; this is a strict reading.
			return inst, true, false, ""
		}
	}
	if ar != nonAtomic {
		// This is something else entirely.
		return inst, false, true, ""
	}
	return nil, true, false, ""
}

func resolveStruct(typ types.Type) (*types.Struct, bool) {
	switch typ := typ.Underlying().(type) {
	case *types.Struct:
		return typ, true
	case *types.Pointer:
		return resolveStruct(typ.Elem())
	}
	return nil, false
}

func (pc *passContext) applyTypeAliases(ls *lockState, v ssa.Value) {
	for v != nil {
		var ltf lockTypeFacts
		pc.importLockTypeFacts(v.Type(), &ltf)
		for _, alias := range ltf.Aliases {
			ls.addAlias(makeResolvedValue(v, alias.Left), makeResolvedValue(v, alias.Right))
		}
		switch x := v.(type) {
		case *ssa.Field:
			v = x.X
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.UnOp:
			if x.Op != token.MUL {
				return
			}
			v = x.X
		default:
			return
		}
	}
}

func findField(typ types.Type, field int) (types.Object, bool) {
	structType, ok := resolveStruct(typ)
	if !ok || field >= structType.NumFields() {
		return nil, false
	}
	return structType.Field(field), true
}

// almostInst is a generalization over ssa.Field, ssa.FieldAddr, ssa.Global.
type almostInst interface {
	Pos() token.Pos
	Referrers() *[]ssa.Instruction
}

// checkGuards checks the guards held.
//
// This also enforces atomicity constraints for fields that must be accessed
// atomically. The parameter isWrite indicates whether this field is used
// downstream for a write operation.
//
// Note that this function is not called if lff.Ignore is true, since it cannot
// discover any local anonymous functions or closures.
func (pc *passContext) checkGuards(wc *walkContext, inst almostInst, from ssa.Value, accessObj types.Object, ls *lockState, isWrite bool) {
	var (
		lgf         lockGuardFacts
		guardsFound int
		heldCount   int
		guardsHeld  = make(map[pathKey]struct{})
	)

	// An object no other goroutine can reach yet has nothing to race with, so its guards
	// say nothing here. Which objects those are, and up to which instruction, is worked
	// out in fresh.go; both of the declarations that produce one are checked.
	instr, ok := inst.(ssa.Instruction)
	if ok && wc.fresh.isFreshAt(from, instr, fieldIndexOf(accessObj, from)) {
		return
	}
	where := decisionAt{inst: instr, pos: inst.Pos(), context: wc.context}
	object := qualifiedName(from, accessObj)

	// Load the facts for the object accessed.
	pc.importLockGuardFacts(accessObj, &lgf)
	pc.applyTypeAliases(ls, from)

	// Check guards held.
	for _, guardName := range slices.Sorted(maps.Keys(lgf.GuardedBy)) {
		fgr := lgf.GuardedBy[guardName]
		r := fgr.resolveField(pc, ls, from)
		if r.unavailable {
			// The guard has no object in this package, so it can
			// neither be enforced nor counted here. This is not an
			// error; see globalGuard.resolveCommon.
			continue
		}
		guardsFound++
		resolved := r.valid()
		pc.decide(where, guardResolve, resolved, guardName, object)
		if !resolved {
			// See above; this cannot be forced.
			pc.maybeFail(inst.Pos(), "field %s cannot be resolved", guardName)
			continue
		}
		p, ok := ls.isHeld(r, isWrite)
		pc.decide(where, guardHeld, ok, guardName, object)
		s := p.String()
		if ok {
			guardsHeld[p.Key()] = struct{}{}
			heldCount++
			continue
		}
		if _, ok := pc.forced[pc.positionKey(inst.Pos())]; ok {
			pc.suppressed()
			// Mark this as locked, since it has been forced. All
			// forces are treated as an exclusive lock.
			forcedPath, inserted := ls.lockField(r, true /* exclusive */)
			if inserted {
				// The force asserts the lock, it does not take it.
				ls.setAssumed(forcedPath, true)
			}
			guardsHeld[forcedPath.Key()] = struct{}{}
			heldCount++
			continue
		}
		// Note that we may allow this if the disposition is atomic,
		// and we are allowing atomic reads only. This will fall into
		// the atomic disposition check below, which asserts that the
		// access is atomic. Further, heldCount < guardsFound
		// will be true for this case, so we require it to be
		// read-only.
		if lgf.AtomicDisposition != atomicRequired {
			// There is no force key, no atomic access and no lock held.
			pc.maybeFailLocks(ls, inst.Pos(), "invalid field access, %s (%s) must be locked when accessing %s (locks: %s)", guardName, s, accessObj.Name(), ls.String())
		} else {
			pc.suppressed()
		}
	}

	// Check the atomic access for this field.
	switch lgf.AtomicDisposition {
	case atomicRequired:
		// Check that this is used safely as an input.
		ar := readWriteAtomic
		if guardsFound > 0 {
			if heldCount < guardsFound {
				ar = readOnlyAtomic
			} else {
				ar = mixedAtomic
			}
		}
		if refs := inst.Referrers(); refs != nil {
			for _, otherInst := range *refs {
				pc.checkAtomicCall(otherInst, object, ar, wc.context)
			}
		}
		// Check that this is not otherwise written non-atomically,
		// even if we do hold all the locks.
		pc.decide(where, atomicWrite, !isWrite, object)
		if isWrite {
			pc.maybeFailLocks(ls, inst.Pos(), "non-atomic write of field %s, writes must still be atomic with locks held (locks: %s)", accessObj.Name(), ls.String())
		}
	case atomicDisallow:
		// If atomic analysis is not enabled, skip.
		if !enableAtomic {
			break
		}
		// Check that this is *not* used atomically.
		if refs := inst.Referrers(); refs != nil {
			for _, otherInst := range *refs {
				pc.checkAtomicCall(otherInst, object, nonAtomic, wc.context)
			}
		}
	}

	// Check inferred locks.
	if accessObj.Pkg() == pc.pass.Pkg {
		pc.recordObservation(inst, from, accessObj, ls, guardsHeld)
	}
}

// observationKey names one access of one object with one set of locks held.
type observationKey struct {
	inst      almostInst
	accessObj types.Object
	locks     string
}

// recordObservation counts one access towards the inferred lock hints, once per lock
// and once per site and lock state: a hint is a share of the accesses, and a repeat
// would raise it above the whole.
func (pc *passContext) recordObservation(inst almostInst, from ssa.Value, accessObj types.Object, ls *lockState, guardsHeld map[pathKey]struct{}) {
	pc.parityObservation(inst, accessObj, ls)
	if pc.emit == emitWalk {
		return
	}
	key := observationKey{inst: inst, accessObj: accessObj, locks: ls.identitySummary(func(lockInfo) bool { return true })}
	if _, ok := pc.counted[key]; ok {
		return
	}
	counted, observations := &pc.counted, &pc.observations
	if buf := pc.fnHints; buf != nil {
		if _, ok := buf.counted[key]; ok {
			return
		}
		counted, observations = &buf.counted, &buf.observations
	}
	if *counted == nil {
		*counted = make(map[observationKey]struct{})
	}
	(*counted)[key] = struct{}{}

	oo := pc.observationsFor(observations, accessObj)
	oo.total++
	held := make(map[types.Object]struct{})
	for k, info := range ls.lockedMutexes {
		// Is this an object for which we have facts? If there is no
		// ability to name this object, then we don't bother with any
		// inference. We also ignore any self references (e.g.
		// accessing a mutex while you are holding that exact mutex).
		if info.object == nil || accessObj == info.object {
			continue
		}
		// Has this already been held?
		if _, ok := guardsHeld[k]; ok {
			held[info.object] = struct{}{}
			continue
		}
		// Is this a global?
		if _, ok := from.(*ssa.Global); ok {
			held[info.object] = struct{}{}
			continue
		}
		// Is the object a sibling to the accessObj? We need to check
		// all fields and see if they match. We accept only siblings
		// and globals for this recommendation.
		structType, ok := resolveStruct(from.Type())
		if !ok {
			continue
		}
		for i := 0; i < structType.NumFields(); i++ {
			if fieldObj := structType.Field(i); fieldObj == info.object {
				held[info.object] = struct{}{}
			}
		}
	}
	for obj := range held {
		oo.counts[obj]++
	}
}

// checkFieldAccess checks the validity of a field access.
func (pc *passContext) checkFieldAccess(wc *walkContext, inst almostInst, structObj ssa.Value, field int, ls *lockState, isWrite bool) {
	fieldObj, _ := findField(structObj.Type(), field)
	pc.checkGuards(wc, inst, structObj, fieldObj, ls, isWrite)
}

// noReferrers wraps an instruction as an almostInst.
type noReferrers struct {
	ssa.Instruction
}

// Referrers implements almostInst.Referrers.
func (noReferrers) Referrers() *[]ssa.Instruction { return nil }

// checkGlobalAccess checks the validity of a global access.
func (pc *passContext) checkGlobalAccess(wc *walkContext, inst ssa.Instruction, g *ssa.Global, ls *lockState, isWrite bool) {
	pc.checkGuards(wc, noReferrers{inst}, g, g.Object(), ls, isWrite)
}

func (pc *passContext) checkCall(site callSite, wc *walkContext, ls *lockState) {
	pc.recordHeld(site.instr, site.run, wc, ls)
	lff := wc.lff
	// See: https://godoc.org/golang.org/x/tools/go/ssa#CallCommon
	//
	// "invoke" mode: Method is non-nil, and Value is the underlying value.
	if fn := site.Common().Method; fn != nil {
		var nlff lockFunctionFacts
		pc.importLockFunctionFacts(fn, &nlff)
		nlff.Ignore = nlff.Ignore || lff.Ignore // Inherit ignore.
		pc.checkFunctionCall(site, wc, fn, &nlff, ls)
		return
	}

	// "call" mode: when Method is nil (!IsInvoke), a CallCommon represents an ordinary
	//  function call of the value in Value, which may be a *Builtin, a *Function or any
	//  other value of kind 'func'.
	//
	// 	Value may be one of:
	// (a) a *Function, indicating a statically dispatched call
	// to a package-level function, an anonymous function, or
	// a method of a named type.
	//
	// (b) a *MakeClosure, indicating an immediately applied
	// function literal with free variables.
	//
	// (c) a *Builtin, indicating a statically dispatched call
	// to a built-in function.
	//
	// (d) any other value, indicating a dynamically dispatched
	//     function call.
	switch fn := site.Common().Value.(type) {
	case *ssa.Function:
		nlff := lockFunctionFacts{
			Ignore: lff.Ignore, // Inherit ignore.
		}
		if obj := fn.Object(); obj != nil {
			tf := obj.(*types.Func)
			pc.importLockFunctionFacts(tf, &nlff)
			nlff.Ignore = nlff.Ignore || lff.Ignore // See above.
			pc.checkFunctionCall(site, wc, tf, &nlff, ls)
		} else {
			// A literal's lock annotations are not checked here: the caller's lock
			// state is known, so the body is analyzed afresh with that state.
			nlff.Ignore = nlff.Ignore || pc.literalIgnored(fn)
			pc.walkLiteral(site, wc, fn, &nlff, ls, func(fnls *lockState) {
				for i, arg := range site.Common().Args {
					fnls.bind(fn.Params[i], arg)
				}
			})
		}
	case *ssa.MakeClosure:
		// Note that creating and then invoking closures locally is
		// allowed, but analysis of passing closures is done when
		// checking individual instructions.
		pc.checkClosure(site, wc, fn, ls)
	default:
		return
	}
}

// checkGoCall reports a go statement that releases a lock, starts a named function or
// annotated literal that needs a lock on entry, or acquires one through a discarded result.
// The new goroutine holds no lock, and the go statement changes none of the caller's.
func (pc *passContext) checkGoCall(site callSite, wc *walkContext, ls *lockState) {
	common := site.Common()
	pos := site.Pos()
	_, forced := pc.forced[pc.positionKey(pos)]
	at := decisionAt{inst: site.instr, pos: pos, context: wc.context}

	if lockFn, op, recv, ok := lockCallSite(pc.pass, common); ok {
		var lockLff lockFunctionFacts
		pc.importLockFunctionFacts(lockFn, &lockLff)
		if len(lockLff.HeldOnEntry) == 0 {
			if op != opRelease || lockLff.Ignore || wc.lff.Ignore || forced {
				return
			}
			pc.applyTypeAliases(ls, recv)
			rv := makeResolvedValue(recv, nil)
			p, _ := rv.valueAndObject(ls)
			p = ls.aliasPath(p)
			pc.decide(at, goRelease, false, lockFn.Name())
			pc.maybeFail(pos, "goroutine started by go %s does not hold %s, which it releases", lockFn.Name(), p.String())
			return
		}
	}

	fn, ok := common.Value.(*ssa.Function)
	if !ok {
		return
	}
	var lff lockFunctionFacts
	var subject, requirer string
	if tf, ok := fn.Object().(*types.Func); ok {
		pc.importLockFunctionFacts(tf, &lff)
		if lff.Ignore {
			return
		}
		subject, requirer = tf.Name(), tf.Name()
	} else {
		facts, ok := pc.closureFactsFor(fn.Syntax())
		if !ok || pc.literalIgnored(fn) {
			return
		}
		lff = *facts
		subject, requirer = "func literal", "the literal"
	}
	if wc.lff.Ignore || forced {
		return
	}
	args := common.Args
	for _, arg := range args {
		pc.applyTypeAliases(ls, arg)
	}
	resolve := func(fg functionGuardInfo) resolvedValue {
		return fg.Resolver.resolveCall(pc, ls, args, nil)
	}
	for _, g := range resolveGuards(ls, lff.HeldOnEntry, nil, resolve) {
		if g.rv.unavailable || !g.rv.valid() {
			continue
		}
		p, _ := g.rv.valueAndObject(ls)
		p = ls.aliasPath(p)
		pc.decide(at, goHeld, false, g.fieldName, subject)
		pc.maybeFail(pos, "goroutine started by go %s does not hold %s %s (%s); %s requires it on entry", subject, g.fieldName, exclusiveStr(g.fg.Exclusive), p.String(), requirer)
	}
	for _, g := range resolveGuards(ls, lff.HeldOnExit, lff.HeldOnEntry, resolve) {
		if _, ok := g.fg.Resolver.(*returnGuard); !ok {
			continue
		}
		pc.decide(at, acquireDiscarded, false, g.fieldName)
		pc.maybeFail(pos, "lock %s is acquired through a discarded result and cannot be tracked", g.fieldName)
	}
}

// resolvedGuard is one guard of a call or an entry, resolved against the lock state.
type resolvedGuard struct {
	fieldName string
	fg        functionGuardInfo
	rv        resolvedValue

	// exclusive is the mode to record for the lock this guard names, which is not
	// necessarily the mode the guard itself asks for. See resolveGuards.
	exclusive bool
}

// resolveGuards resolves one call's or entry's guards in name order, skipping those in skip.
// A lock's mode is combined over the guards naming it, exclusive winning as in addAlias,
// so map order cannot decide how an aliased lock is held.
func resolveGuards(ls *lockState, guards, skip map[string]functionGuardInfo, resolve func(functionGuardInfo) resolvedValue) []resolvedGuard {
	resolved := make([]resolvedGuard, 0, len(guards))
	for _, fieldName := range slices.Sorted(maps.Keys(guards)) {
		if _, ok := skip[fieldName]; ok {
			continue
		}
		fg := guards[fieldName]
		resolved = append(resolved, resolvedGuard{
			fieldName: fieldName,
			fg:        fg,
			rv:        resolve(fg),
			exclusive: fg.Exclusive,
		})
	}
	keys := make([]pathKey, len(resolved))
	modes := make(map[pathKey]bool, len(resolved))
	for i, g := range resolved {
		if !g.rv.valid() {
			continue
		}
		path, _ := g.rv.valueAndObject(ls)
		path = ls.aliasPath(path)
		keys[i] = path.Key()
		modes[keys[i]] = modes[keys[i]] || g.fg.Exclusive
	}
	for i := range resolved {
		if keys[i] == "" {
			continue
		}
		resolved[i].exclusive = modes[keys[i]]
	}
	return resolved
}

// postFunctionCallUpdate updates all conditions.
func (pc *passContext) postFunctionCallUpdate(site callSite, lff *lockFunctionFacts, ls *lockState, context string) {
	at := decisionAt{inst: site.instr, pos: site.Pos(), context: context}
	resolve := func(fg functionGuardInfo) resolvedValue {
		return fg.Resolver.resolveCall(pc, ls, site.Common().Args, site.Result())
	}

	// lost is the real locks the release annotations dropped, for the engine view.
	var lost []lockPath
	// Release all locks not still held.
	for _, g := range resolveGuards(ls, lff.HeldOnEntry, lff.HeldOnExit, resolve) {
		if g.rv.unavailable {
			continue
		}
		resolved := g.rv.valid()
		pc.decide(at, effectResolve, resolved, g.fieldName)
		if !resolved {
			// See above: this cannot be forced.
			pc.opFailed = true
			pc.maybeFail(site.Pos(), "field %s cannot be resolved", g.fieldName)
			continue
		}
		pre, _ := g.rv.valueAndObject(ls)
		preInfo, preHeld := ls.lockedMutexes[ls.aliasPath(pre).Key()]
		p, ok := ls.unlockField(g.rv, g.fg.Exclusive)
		pc.decide(at, release, ok, g.fieldName)
		if ok && preHeld && !preInfo.assumed {
			lost = append(lost, p)
		}
		if !ok {
			pc.opFailed = true
			if lff.Ignore || pc.silencesRelease(ls, p, site.kind == siteDefer, site.Pos()) {
				pc.suppressed()
			} else if _, forced := pc.forced[pc.positionKey(site.Pos())]; forced {
				pc.suppressed()
			} else {
				pc.maybeFailLocks(ls, site.Pos(), "attempt to release %s (%s), but not held (locks: %s)", g.fieldName, p.String(), ls.String())
			}
		}
	}

	// Update all held locks if acquired.
	for _, g := range resolveGuards(ls, lff.HeldOnExit, lff.HeldOnEntry, resolve) {
		if g.rv.unavailable {
			continue
		}
		tracked := g.rv.valid()
		pc.decide(at, acquireDiscarded, tracked, g.fieldName)
		if !tracked {
			pc.opFailed = true
			pc.maybeFail(site.Pos(), "lock %s is acquired through a discarded result and cannot be tracked", g.fieldName)
			continue
		}
		// Acquire the lock per the annotation.
		pre, _ := g.rv.valueAndObject(ls)
		preInfo, preHeld := ls.lockedMutexes[ls.aliasPath(pre).Key()]
		p, ok := ls.lockField(g.rv, g.exclusive)
		pc.decide(at, acquire, ok, g.fieldName)
		if ok || (preHeld && preInfo.assumed) {
			if i := slices.IndexFunc(lost, func(l lockPath) bool { return l.Key() == p.Key() }); i >= 0 {
				// Released and taken back by the one call: no change.
				lost = slices.Delete(lost, i, i+1)
			} else {
				pc.recordGain(site.instr, p)
			}
		}
		if !ok {
			pc.opFailed = true
			// The lock is really taken here, whatever assumed it earlier.
			ls.setAssumed(p, false)
			if _, ok := pc.forced[pc.positionKey(site.Pos())]; !ok && !lff.Ignore {
				pc.maybeFailLocks(ls, site.Pos(), "attempt to acquire %s (%s), but already held (locks: %s)", g.fieldName, p.String(), ls.String())
			} else {
				pc.suppressed()
			}
		}
	}
	for _, p := range lost {
		pc.recordRelease(site.instr, p)
	}
}

// exclusiveStr returns a string describing exclusive requirements.
func exclusiveStr(exclusive bool) string {
	if exclusive {
		return "exclusively"
	}
	return "non-exclusively"
}

// checkFunctionCall checks preconditions for function calls, and tracks the
// lock state by recording relevant calls to sync functions. Note that calls to
// atomic functions are tracked by checkFieldAccess by looking directly at the
// referrers (because ordering doesn't matter there, so we need not scan in
// instruction order).
func (pc *passContext) checkFunctionCall(site callSite, wc *walkContext, fn *types.Func, lff *lockFunctionFacts, ls *lockState) {
	// Extract the "receiver" properly.
	var args []ssa.Value
	if site.Common().Method != nil {
		// This is an interface dispatch for sync.Locker.
		args = append([]ssa.Value{site.Common().Value}, site.Common().Args...)
	} else {
		// This matches the signature for the relevant
		// sync.Lock/sync.Unlock functions below.
		args = site.Common().Args
	}

	callPos := site.Pos()
	at := decisionAt{inst: site.instr, pos: callPos, context: wc.context}
	posKey := pc.positionKey(callPos)
	_, forced := pc.forced[posKey]
	callValue := site.Result()
	resolve := func(fg functionGuardInfo) resolvedValue {
		return fg.Resolver.resolveCall(pc, ls, args, callValue)
	}
	for _, arg := range args {
		pc.applyTypeAliases(ls, arg)
	}

	// Check that excluded locks are not held on entry.
	for _, g := range resolveGuards(ls, lff.ExcludedOnEntry, nil, resolve) {
		if g.rv.unavailable {
			// The lock has no object in this package, so it cannot
			// be held here; see globalGuard.resolveCommon.
			continue
		}
		p, held := ls.isHeld(g.rv, g.fg.Exclusive)
		pc.decide(at, callExcluded, !held, g.fieldName, fn.Name())
		if held {
			if !forced && !lff.Ignore {
				if g.fg.Exclusive {
					pc.maybeFailLocks(ls, callPos, "must not hold %s exclusively (%s) to call %s, but held (locks: %s)", g.fieldName, p.String(), fn.Name(), ls.String())
				} else {
					pc.maybeFailLocks(ls, callPos, "must not hold %s (%s) to call %s, but held (locks: %s)", g.fieldName, p.String(), fn.Name(), ls.String())
				}
			} else {
				pc.suppressed()
			}
		}
	}

	// Check that what is passed to a parameter declared to arrive unpublished is an
	// object nothing else can reach yet. This is the other half of the check that makes
	// the declaration mean something; the first half is at the constructor.
	pc.checkFreshArgs(wc, site.instr, forced || lff.Ignore)

	// Check all guards required are held.
	for _, g := range resolveGuards(ls, lff.HeldOnEntry, nil, resolve) {
		if g.rv.unavailable {
			// See above; the requirement cannot be checked here.
			continue
		}
		// A lock precondition on an object under construction says nothing: nothing
		// else can reach it to contend for the lock. This is the same silence the
		// guarded fields of that object get, and the census showed it is nearly half
		// of what the suppressions were for.
		if pc.freshArgument(wc, site, g.fg) {
			continue
		}
		p, ok := ls.isHeld(g.rv, g.fg.Exclusive)
		pc.decide(at, callHeld, ok, g.fieldName, fn.Name())
		if !ok {
			if !forced && !lff.Ignore {
				pc.maybeFailLocks(ls, callPos, "must hold %s %s (%s) to call %s, but not held (locks: %s)", g.fieldName, exclusiveStr(g.fg.Exclusive), p.String(), fn.Name(), ls.String())
			} else {
				pc.suppressed()
				if forcedPath, inserted := ls.lockField(g.rv, g.exclusive); inserted {
					// The force asserts the lock, it does not take it.
					pc.opFailed = true
					ls.setAssumed(forcedPath, true)
				}
			}
		}
	}

	// Update all lock state accordingly.
	pc.postFunctionCallUpdate(site, lff, ls, wc.context)

	// Check if it's a method dispatch for something in the sync package.
	// See: https://godoc.org/golang.org/x/tools/go/ssa#Function

	if _, op, recv, ok := lockCallSite(pc.pass, site.Common()); ok {
		rv := makeResolvedValue(recv, nil)
		exclusive := !lockOpShared(fn.Name())
		switch op {
		case opAcquire:
			p, ok := ls.lockField(rv, exclusive)
			pc.decide(at, lockOpLock, ok)
			if ok {
				pc.recordOpGain(site.instr, p)
			} else {
				pc.opFailed = true
				// The lock is really taken here, whatever assumed it earlier.
				ls.setAssumed(p, false)
				if !lff.Ignore && !forced {
					// Double locking a mutex that is already locked.
					pc.maybeFailLocks(ls, callPos, "%s already locked (locks: %s)", p.String(), ls.String())
				} else {
					pc.suppressed()
				}
			}
		case opRelease:
			p, ok := ls.unlockField(rv, exclusive)
			pc.decide(at, lockOpUnlock, ok)
			if !ok {
				pc.opFailed = true
				if !forced && !lff.Ignore && !pc.silencesRelease(ls, p, site.kind == siteDefer, callPos) {
					// Unlocking something that is already unlocked.
					pc.maybeFailLocks(ls, callPos, "%s already unlocked or locked differently (locks: %s)", p.String(), ls.String())
				} else {
					pc.suppressed()
				}
			}
		case opDowngrade:
			p, ok := ls.downgradeField(rv)
			pc.decide(at, lockOpDowngrade, ok)
			if !ok {
				pc.opFailed = true
				if !forced && !lff.Ignore && !pc.silencesRelease(ls, p, site.kind == siteDefer, callPos) {
					// Downgrading something that may not be downgraded.
					pc.maybeFailLocks(ls, callPos, "%s already unlocked or not exclusive (locks: %s)", p.String(), ls.String())
				} else {
					pc.suppressed()
				}
			} else {
				// The lock is really held here, whatever assumed it earlier.
				ls.setAssumed(p, false)
			}
		}
	}
}

// checkClosure walks the closure with its free variables and parameters bound, which
// allows the analysis to resolve it.
func (pc *passContext) checkClosure(site callSite, wc *walkContext, fn *ssa.MakeClosure, ls *lockState) {
	clfn := fn.Fn.(*ssa.Function)
	// Note that this is *not* a call to check function call, which checks
	// against the function preconditions. Instead, this does a fresh
	// analysis of the function from source code with a different state.
	nlff := lockFunctionFacts{
		Ignore: wc.lff.Ignore, // Inherit ignore.
	}
	// An ignore written on the literal applies wherever it is analyzed,
	// including here, where it is invoked in a scope this analysis can
	// follow. The lock requirements are not seeded in that case: the real
	// state of the caller is known and is more precise than the
	// annotation.
	if annotated, ok := pc.closureFactsFor(clfn.Syntax()); ok && annotated.Ignore {
		nlff.Ignore = true
	}
	pc.walkLiteral(site, wc, clfn, &nlff, ls, func(clls *lockState) {
		for i, fv := range clfn.FreeVars {
			clls.store(fv, fn.Bindings[i])
		}
		for i, arg := range site.Common().Args {
			clls.bind(clfn.Params[i], arg)
		}
	})
}

// walkLiteral walks the directly called literal fn inline, on a fork of ls with no pending defers, and
// continues ls with its end state. Where that is not one state, ls is left as it was, and if a relevant
// defer is pending the literal is walked again with it.
func (pc *passContext) walkLiteral(site callSite, outer *walkContext, fn *ssa.Function, lff *lockFunctionFacts, ls *lockState, bind func(*lockState)) {
	if pc.emit == emitReplay {
		if exit := pc.current.foldOf(site.instr); exit != nil {
			ls.foldExit(exit)
		}
		return
	}
	walk := func(inherit bool) *walkContext {
		fnls := ls.fork()
		bind(fnls)
		if !inherit {
			fnls.deferStack = deferStack{}
		}
		return pc.checkFunction(site, outer, fn, lff, fnls, true /* force */)
	}
	recordsAt, pendingAt, decisionsAt, markedAt, givenUpAt := len(pc.records), len(pc.pending), pc.decisionMark(), len(pc.marked), len(pc.givenUp)
	if pc.legacyLits {
		walk(true)
		pc.markUnjoined(fn, recordsAt)
		return
	}
	inherits := len(ls.defers) > 0
	var gaveUp map[*ssa.Function]string
	if inherits && pc.engine != nil {
		gaveUp = maps.Clone(pc.engine.gaveUp)
	}
	wc := walk(false)
	exit, reason := pc.litExit(outer.fn, wc, ls)
	pc.statLitNoFold(reason)
	switch {
	case reason == "gaveup":
		pc.markUnjoined(fn, recordsAt)
		return
	case exit == nil && inherits:
		pc.records, pc.pending = pc.records[:recordsAt], pc.pending[:pendingAt]
		pc.withdrawDecisions(decisionsAt)
		for _, f := range pc.marked[markedAt:] {
			delete(pc.functions, f)
		}
		pc.marked = pc.marked[:markedAt]
		for _, f := range pc.givenUp[givenUpAt:] {
			delete(pc.givenUpSet, f)
		}
		pc.givenUp = pc.givenUp[:givenUpAt]
		if pc.engine != nil {
			pc.engine.gaveUp = gaveUp
		}
		pc.legacyLits = true
		defer func() { pc.legacyLits = false }()
		walk(true)
		pc.markUnjoined(fn, recordsAt)
		return
	}
	pc.joinDefers(wc, recordsAt)
	if exit != nil {
		ls.foldExit(exit)
		pc.current.setFold(site.instr, exit)
	}
}

// markUnjoined notes that fn was walked, leaving records since recordsAt, without joining its defer stacks.
func (pc *passContext) markUnjoined(fn *ssa.Function, recordsAt int) {
	if len(pc.records) == recordsAt {
		return
	}
	if pc.unjoined == nil {
		pc.unjoined = make(map[*ssa.Function]bool)
	}
	pc.unjoined[fn] = true
}

// litExit returns the end state of the walk of a literal called in pre, or the reason there is none.
func (pc *passContext) litExit(outer *ssa.Function, wc *walkContext, pre *lockState) (*lockState, string) {
	switch {
	case wc.gaveUp || pc.overBudget:
		return nil, "gaveup"
	case len(wc.exits) == 0:
		return nil, "noexit"
	}
	exit := wc.exits[0]
	for _, e := range wc.exits[1:] {
		if !exit.isCompatible(e) {
			return nil, "disagree"
		}
	}
	if retaken(pre, exit) {
		return nil, "retake"
	}
	if pc.storesTracked(outer, wc.fn, pre) {
		return nil, "stores"
	}
	return exit, ""
}

// storesTracked reports whether lit, or a literal in it, stores to a captured variable that fn reads lock paths
// through, or stores a value that can hold a pointer anywhere but a free variable or its own local, while pre
// holds a lock that is not assumed. The result is kept per (fn, lit).
func (pc *passContext) storesTracked(fn, lit *ssa.Function, pre *lockState) bool {
	held := false
	for _, info := range pre.lockedMutexes {
		held = held || !info.assumed
	}
	si := pc.slotPass(fn)
	if !held || si == nil {
		return held
	}
	key := [2]*ssa.Function{fn, lit}
	if r, ok := pc.storeScans[key]; ok {
		return r
	}
	var tracked func(v ssa.Value) bool
	tracked = func(v ssa.Value) bool {
		fv, ok := v.(*ssa.FreeVar)
		if !ok || fv.Parent() == fn {
			return si.loaded[v]
		}
		parent := fv.Parent().Parent()
		i := slices.Index(fv.Parent().FreeVars, fv)
		for _, b := range parent.Blocks {
			for _, instr := range b.Instrs {
				if mc, ok := instr.(*ssa.MakeClosure); ok && mc.Fn == ssa.Value(fv.Parent()) && tracked(mc.Bindings[i]) {
					return true
				}
			}
		}
		return false
	}
	found := false
	for work := []*ssa.Function{lit}; len(work) > 0 && !found; {
		f := work[len(work)-1]
		work = append(work[:len(work)-1], f.AnonFuncs...)
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				if st, ok := instr.(*ssa.Store); ok && (tracked(st.Addr) || carries(st.Val.Type()) && !isFreeVar(st.Addr) && !ownLocal(st.Addr)) {
					found = true
				}
			}
		}
	}
	if pc.storeScans == nil {
		pc.storeScans = make(map[[2]*ssa.Function]bool)
	}
	pc.storeScans[key] = found
	return found
}

// carries reports whether a value of type t can hold a pointer.
func carries(t types.Type) bool {
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind() == types.UnsafePointer
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if carries(u.Field(i).Type()) {
				return true
			}
		}
		return false
	case *types.Array:
		return carries(u.Elem())
	}
	return true
}

// ownLocal reports whether v is a local of the storing function, or a field or element of one with no load
// on the way.
func ownLocal(v ssa.Value) bool {
	for {
		switch x := v.(type) {
		case *ssa.Alloc:
			return true
		case *ssa.FieldAddr:
			v = x.X
		case *ssa.IndexAddr:
			v = x.X
		default:
			return false
		}
	}
}

// isFreeVar reports whether v is a free variable of its function.
func isFreeVar(v ssa.Value) bool {
	_, ok := v.(*ssa.FreeVar)
	return ok
}

// retaken reports whether exit lacks a lock held in pre, other than an assumed one, and holds a lock pre does not.
func retaken(pre, exit *lockState) bool {
	released := false
	for key, info := range pre.lockedMutexes {
		if _, ok := exit.lockedMutexes[key]; !ok && !info.assumed {
			released = true
			break
		}
	}
	if !released {
		return false
	}
	for k := range exit.lockedMutexes {
		if _, ok := pre.lockedMutexes[k]; !ok {
			return true
		}
	}
	return false
}

// foldOf returns the exit stored for the call, or nil.
func (r *blockRecord) foldOf(call ssa.CallInstruction) *lockState {
	if r == nil {
		return nil
	}
	return r.folds[call]
}

// setFold stores the exit the caller continues with after the call.
func (r *blockRecord) setFold(call ssa.CallInstruction, exit *lockState) {
	if r == nil {
		return
	}
	if r.folds == nil {
		r.folds = make(map[ssa.CallInstruction]*lockState)
	}
	r.folds[call] = exit
}

// freshAlloc indicates that v has been allocated within the local scope. There
// is no lock checking done on objects that are freshly allocated.
func freshAlloc(v ssa.Value) bool {
	switch x := v.(type) {
	case *ssa.Alloc:
		return true
	case *ssa.FieldAddr:
		return freshAlloc(x.X)
	case *ssa.Field:
		return freshAlloc(x.X)
	case *ssa.IndexAddr:
		return freshAlloc(x.X)
	case *ssa.Index:
		return freshAlloc(x.X)
	case *ssa.Convert:
		return freshAlloc(x.X)
	case *ssa.ChangeType:
		return freshAlloc(x.X)
	default:
		return false
	}
}

// isWrite indicates that this value is used as the addr field in a store.
//
// Note that this may still be used for a write. The return here is optimistic
// but sufficient for basic analysis.
func isWrite(v ssa.Value) bool {
	refs := v.Referrers()
	if refs == nil {
		return false
	}
	for _, ref := range *refs {
		if s, ok := ref.(*ssa.Store); ok && s.Addr == v {
			return true
		}
	}
	return false
}

// siteKind is how a function is reached.
type siteKind uint8

const (
	siteCall siteKind = iota
	siteDefer
	siteGo
	siteEntry
)

// callSite describes how a function is reached; instr is nil for the entry of a walk.
type callSite struct {
	kind  siteKind
	instr ssa.CallInstruction

	// run is the return that runs a deferred call; nil elsewhere.
	run *ssa.RunDefers
}

// newCallSite returns the site of a call, defer or go statement.
func newCallSite(instr ssa.CallInstruction) callSite {
	switch instr.(type) {
	case *ssa.Call:
		return callSite{kind: siteCall, instr: instr}
	case *ssa.Defer:
		return callSite{kind: siteDefer, instr: instr}
	case *ssa.Go:
		return callSite{kind: siteGo, instr: instr}
	}
	panic(fmt.Sprintf("newCallSite: unexpected instruction %T", instr))
}

func (s callSite) Pos() token.Pos {
	if s.instr == nil {
		return token.NoPos
	}
	return s.instr.Pos()
}

func (s callSite) Common() *ssa.CallCommon {
	if s.instr == nil {
		return nil
	}
	return s.instr.Common()
}

// Result returns the call's value, or an untyped nil for any other site, where
// Value is a nil *ssa.Call that would otherwise pass as a non-nil interface.
func (s callSite) Result() ssa.Value {
	if s.kind != siteCall {
		return nil
	}
	return s.instr.Value()
}

// walkContext is what belongs to one walk of one function.
type walkContext struct {
	fn  *ssa.Function
	lff *lockFunctionFacts

	// fresh is what the walk knows about the objects under construction in fn.
	fresh *freshState

	// entryLocks holds, by identity, the mode of each caller's lock an inline walk was entered with.
	entryLocks map[pathKey]bool

	// returnStates holds, by identity summary, the text of each lock state the walk returned in.
	returnStates map[string]string

	// exits holds the lock state at each return, after its deferred calls.
	exits []*lockState

	// context names the walk in the decision log; see inlineContext.
	context string

	// gaveUp is set when a block in a cycle overflowed; the walk then stops.
	gaveUp bool

	// foreign is set for a walk made while running a deferred call of another function; it records nothing.
	foreign bool
}

// checkInstruction checks the legality the single instruction based on the
// current lockState.
func (pc *passContext) checkInstruction(inst ssa.Instruction, wc *walkContext, ls *lockState) (*ssa.Return, *lockState) {
	// Record any observed globals, and check for violations. The global
	// value is not itself an instruction, but we check all referrers to
	// see where they are consumed.
	var stackLocal [16]*ssa.Value
	ops := inst.Operands(stackLocal[:])
	for _, v := range ops {
		if v == nil {
			continue
		}
		g, ok := (*v).(*ssa.Global)
		if !ok {
			continue
		}
		_, isWrite := inst.(*ssa.Store)
		if !wc.lff.Ignore {
			pc.checkGlobalAccess(wc, inst, g, ls, isWrite)
		}
	}

	// Process the instruction.
	switch x := inst.(type) {
	case *ssa.Store:
		// Record that this value is holding this other value. This is
		// because at the beginning of each ssa execution, there is a
		// series of assignments of parameter values to alloc objects.
		// This allows us to trace these back to the original
		// parameters as aliases above.
		//
		// Note that this may overwrite an existing value in the lock
		// state, but this is intentional.
		ls.store(x.Addr, x.Val)
	case *ssa.Field:
		if !freshAlloc(x.X) && !wc.lff.Ignore {
			pc.checkFieldAccess(wc, x, x.X, x.Field, ls, false)
		}
	case *ssa.FieldAddr:
		if !freshAlloc(x.X) && !wc.lff.Ignore {
			pc.checkFieldAccess(wc, x, x.X, x.Field, ls, isWrite(x))
		}
	case *ssa.Call:
		// Resolve the result before the call is checked, so that a
		// lock reached through the result of an accessor is the same
		// lock as one named directly. See accessor.go.
		pc.substituteGlobalAccessor(x, ls)
		pc.checkCall(newCallSite(x), wc, ls)
	case *ssa.Send, *ssa.Select:
		pc.recordHeld(x, nil, wc, ls)
	case *ssa.UnOp:
		if x.Op == token.ARROW {
			pc.recordHeld(x, nil, wc, ls)
		}
	case *ssa.Go:
		pc.checkGoCall(newCallSite(x), wc, ls)
	case *ssa.Defer:
		ls.pushDefer(x, pc.deferRelevant(x))
	case *ssa.RunDefers:
		pc.runDefers(x, wc, ls)
	case *ssa.MakeClosure:
		if refs := x.Referrers(); refs != nil {
			var (
				calls    int
				nonCalls int
			)
			for _, ref := range *refs {
				switch ref.(type) {
				case *ssa.Call, *ssa.Defer:
					// Analysis will be done on the call
					// itself subsequently, including the
					// lock state at the time of the call.
					calls++
				default:
					// We need to analyze separately. Per
					// below, this means that we'll analyze
					// at closure construction time no zero
					// assumptions about when it will be
					// called.
					nonCalls++
				}
			}
			if calls > 0 && nonCalls == 0 {
				return nil, nil
			}
		}
		// Analyze the closure without bindings. This means that we
		// assume no lock facts or have any existing lock state. Only
		// trivial closures are acceptable in this case.
		clfn := x.Fn.(*ssa.Function)
		if pc.isBareLockWrapper(clfn) && onlyReturned(x) {
			return nil, nil
		}
		nlff := lockFunctionFacts{
			Ignore: wc.lff.Ignore || pc.literalIgnored(clfn),
		}
		legacy := pc.legacyLits
		pc.legacyLits = false
		recordsAt := len(pc.records)
		pc.checkFunction(callSite{kind: siteEntry}, wc, clfn, &nlff, nil, false /* force */)
		pc.markUnjoined(clfn, recordsAt)
		pc.legacyLits = legacy
	case *ssa.Return:
		return x, ls // Valid return state.
	}
	return nil, nil
}

// isBareLockWrapper reports whether fn is the generated wrapper of a lock method value
// whose method asks for nothing held on entry. Walked alone it has no state to check.
func (pc *passContext) isBareLockWrapper(fn *ssa.Function) bool {
	obj, ok := fn.Object().(*types.Func)
	if fn.Synthetic == "" || !ok || lockCall(pc.pass, obj) == opNone {
		return false
	}
	var lff lockFunctionFacts
	pc.importLockFunctionFacts(obj, &lff)
	return len(lff.HeldOnEntry) == 0
}

// onlyReturned reports whether v is only returned by a declared function, through phis,
// interface boxing and ChangeType; any other use may run it in this invocation.
func onlyReturned(v ssa.Value) bool {
	if mc, ok := v.(*ssa.MakeClosure); !ok || mc.Parent().Parent() != nil {
		return false
	}
	returned, ok := returnUses(v, make(map[ssa.Value]bool))
	return ok && returned
}

func returnUses(v ssa.Value, seen map[ssa.Value]bool) (returned, ok bool) {
	if seen[v] {
		return false, true
	}
	seen[v] = true
	refs := v.Referrers()
	if refs == nil {
		return false, true
	}
	for _, ref := range *refs {
		switch r := ref.(type) {
		case *ssa.Return:
			returned = true
		case *ssa.Phi, *ssa.MakeInterface, *ssa.ChangeType:
			rr, ok := returnUses(r.(ssa.Value), seen)
			if !ok {
				return false, false
			}
			returned = returned || rr
		default:
			return false, false
		}
	}
	return returned, true
}

// blockRecord is the input a walk entered a block with, kept for the replay.
type blockRecord struct {
	wc     *walkContext
	block  *ssa.BasicBlock
	parent *lockState
	parity blockParity

	// pair is the block and element key the record entered under; outs are the pairs its edges reached.
	pair pairID
	outs []pairID

	// folds holds, per call of a literal the block makes, the exit its caller continues with.
	folds map[ssa.CallInstruction]*lockState
}

// returnPos is where a report at ret is printed. A fall-off-the-end return has no
// position of its own, so it takes the closing brace.
func returnPos(ret *ssa.Return) token.Pos {
	if ret.Pos().IsValid() {
		return ret.Pos()
	}
	switch n := ret.Parent().Syntax().(type) {
	case *ast.FuncDecl:
		return n.Body.Rbrace
	case *ast.FuncLit:
		return n.Body.Rbrace
	case *ast.RangeStmt:
		return n.Body.Rbrace
	}
	return token.NoPos
}

// transferBlock runs the instructions of a block on a fork of parent. It returns
// the resulting state and, when the block returns, that state again.
func (pc *passContext) transferBlock(wc *walkContext, block *ssa.BasicBlock, parent *lockState) (ls, rls *lockState) {
	var rv *ssa.Return
	ls = parent.fork()
	for _, inst := range block.Instrs {
		rv, rls = pc.checkInstruction(inst, wc, ls)
		if rls != nil {
			failed := false
			pos := returnPos(rv)
			at := decisionAt{inst: rv, pos: pos, context: wc.context}
			// Validate held locks.
			resolve := func(fg functionGuardInfo) resolvedValue {
				return fg.Resolver.resolveStatic(pc, ls, wc.fn, rv)
			}
			for _, g := range resolveGuards(rls, wc.lff.HeldOnExit, nil, resolve) {
				if g.rv.unavailable {
					continue
				}
				resolved := g.rv.valid()
				pc.decide(at, returnResolve, resolved, g.fieldName)
				if !resolved {
					// This cannot be forced, since we have no reference.
					pc.maybeFail(pos, "lock %s cannot be resolved", g.fieldName)
					continue
				}
				p, held := rls.isHeld(g.rv, g.fg.Exclusive)
				pc.decide(at, returnHeld, held, g.fieldName)
				if held {
					// The lock leaves with the caller, so it counts as taken
					// here however it was first recorded.
					rls.setAssumed(p, false)
					continue
				}
				if _, ok := pc.forced[pc.positionKey(pos)]; !ok && !wc.lff.Ignore {
					pc.maybeFailLocks(rls, pos, "lock %s (%s) not held %s (locks: %s)", g.fieldName, p.String(), exclusiveStr(g.fg.Exclusive), rls.String())
					failed = true
					continue
				}
				pc.suppressed()
				// Force the lock to be acquired.
				if forcedPath, inserted := rls.lockField(g.rv, g.exclusive); !inserted {
					rls.setAssumed(forcedPath, false)
				}
			}
			// Check for other locks, but only if the above didn't trip.
			if !failed {
				counted := rls.countOnReturn(wc.entryLocks) == len(wc.lff.HeldOnExit)
				pc.decide(at, returnCount, counted)
				if !counted && !wc.lff.Ignore {
					pc.maybeFailLocks(rls, pos, "return with unexpected locks held (locks: %s)", rls.String())
				} else if !counted {
					pc.suppressed()
				}
			}
		}
	}
	return ls, rls
}

// replay runs every recorded block again on its recorded input, which is where the
// reports raised inside blocks come from.
func (pc *passContext) replay() {
	outerEmit := pc.emit
	pc.emit = emitReplay
	pc.recordEntry()
	for _, r := range pc.records {
		for run := 0; run < replayRuns; run++ {
			pc.current = r
			n := len(pc.records)
			ls, _ := pc.transferBlock(r.wc, r.block, r.parent)
			r.parity.replayed(pc, r, ls, len(pc.records) != n)
		}
	}
	pc.current, pc.emit, pc.records, pc.unjoined, pc.storeScans = nil, outerEmit, nil, nil, nil
}

// maxCycleStates bounds the lock-state classes (elements with the marker cleared) that reach one block in a cycle.
const maxCycleStates = 64

// maxFunctionWalks bounds the block walks of one top-level function, inline walks included.
var maxFunctionWalks = 40000

// loopBody returns the natural loop of the back edge from latch to head.
func loopBody(head, latch *ssa.BasicBlock) map[*ssa.BasicBlock]bool {
	body := map[*ssa.BasicBlock]bool{head: true}
	for work := []*ssa.BasicBlock{latch}; len(work) > 0; {
		b := work[len(work)-1]
		work = work[:len(work)-1]
		if body[b] {
			continue
		}
		body[b] = true
		work = append(work, b.Preds...)
	}
	return body
}

// checkFunction checks a function invocation, typically starting with nil lockState.
func (pc *passContext) checkFunction(site callSite, outer *walkContext, fn *ssa.Function, lff *lockFunctionFacts, parent *lockState, force bool) (wc *walkContext) {
	if pc.emit == emitReplay {
		return nil
	}
	outerEmit, outerFailed := pc.emit, pc.opFailed
	pc.emit = emitDirect
	defer func() { pc.emit, pc.opFailed = outerEmit, outerFailed }()
	if outer == nil {
		pc.walks, pc.overBudget, pc.unjoined, pc.storeScans = 0, false, nil, nil
		pc.entryGuards = nil
		pc.marked, pc.givenUp = pc.marked[:0], pc.givenUp[:0]
		clear(pc.givenUpSet)
	}
	defer pc.walkChain(fn)()
	pc.decisions.enter()
	defer pc.decisions.leave()
	defer func() {
		// Mark this function as checked. This is used by the top-level
		// loop to ensure that all anonymous functions are scanned, if
		// they are not explicitly invoked here. Note that this can
		// happen if the anonymous functions are e.g. passed only as
		// parameters or used to initialize some structure.
		if _, ok := pc.functions[fn]; !ok {
			pc.functions[fn] = struct{}{}
			pc.marked = append(pc.marked, fn)
		}
	}()
	if _, ok := pc.functions[fn]; !force && ok {
		// This function has already been analyzed at least once.
		// That's all we permit for each function, although this may
		// cause some anonymous functions to be analyzed in only one
		// context.
		return nil
	}

	// Initialize ls with any preconditions that require locks to be held
	// for the method to be invoked. Note that in the overwhelming majority
	// of cases, parent will be nil. However, in the case of closures and
	// anonymous functions, we may start with a non-nil lock state.
	ls := parent.fork()
	ls.oracle = pc.oracle()
	ls.nameConstants(fn)
	for _, param := range fn.Params {
		pc.applyTypeAliases(ls, param)
	}
	for _, fv := range fn.FreeVars {
		pc.applyTypeAliases(ls, fv)
	}

	// The first is the method object itself so we skip that when
	// looking for receiver/function parameters.
	context := inlineContext(outer, site, fn)
	entry := decisionAt{fn: fn, label: "entry", pos: fn.Pos(), context: context}
	resolve := func(fg functionGuardInfo) resolvedValue {
		if _, ok := fg.Resolver.(*returnGuard); ok {
			panic(fmt.Sprintf("checkFunction: %s holds a return guard on entry", fn.Name()))
		}
		return fg.Resolver.resolveStatic(pc, ls, fn, site.Result())
	}
	for _, g := range resolveGuards(ls, lff.HeldOnEntry, nil, resolve) {
		if g.rv.unavailable {
			continue
		}
		resolved := g.rv.valid()
		pc.decide(entry, entryResolve, resolved, g.fieldName)
		if !resolved {
			// See above: this cannot be forced.
			pc.maybeFail(fn.Pos(), "lock %s cannot be resolved", g.fieldName)
			continue
		}
		p, ok := ls.lockField(g.rv, g.exclusive)
		pc.decide(entry, entryDuplicate, ok, g.fieldName)
		if outer == nil {
			if pc.entryGuards == nil {
				pc.entryGuards = make(map[pathKey][]string)
			}
			pc.entryGuards[p.Key()] = append(pc.entryGuards[p.Key()], g.fieldName)
		}
		if !ok {
			if !lff.Ignore {
				// This can only happen if the same value is declared
				// multiple times, and should be caught by the earlier
				// fact scanning. Keep it here as a sanity check.
				pc.maybeFailLocks(ls, fn.Pos(), "lock %s (%s) acquired multiple times or differently (locks: %s)", g.fieldName, p.String(), ls.String())
			} else {
				pc.suppressed()
			}
		}
	}

	// The objects under construction in this function, which the guard checks below
	// consult. computeFresh sees the enclosing walk's state, nil outside any walk.
	var outerFresh *freshState
	if outer != nil {
		outerFresh = outer.fresh
	}
	wc = &walkContext{fn: fn, lff: lff, fresh: pc.computeFresh(fn, outerFresh), context: context, foreign: pc.foreign}

	// An inline body is entered with the caller's locks held; only what it takes
	// itself is its own to release, and a lock merely assumed is no cover.
	if parent != nil {
		wc.entryLocks = make(map[pathKey]bool, ls.count())
		for key, info := range ls.lockedMutexes {
			if !info.assumed {
				wc.entryLocks[key] = info.exclusive
			}
		}
	}
	// fn.Recover is not a root: it starts from the entry state, where an acquire or
	// release contract always fails. lockorder skips it too.
	roots := make([]*ssa.BasicBlock, 0, 1)
	if len(fn.Blocks) > 0 {
		roots = append(roots, fn.Blocks[0])
	}
	recordsAt, pendingAt, decisionsAt, markedAt := len(pc.records), len(pc.pending), pc.decisionMark(), len(pc.marked)
	for _, root := range roots {
		wc.returnStates = make(map[string]string)
		pc.solve(wc, root, ls)
		if wc.gaveUp || pc.overBudget {
			break
		}
		// Two states that cannot both hold are one finding about the function,
		// however many paths reach them. Sorting by text first keeps the order the messages had.
		compatible := len(wc.returnStates) <= 1
		pc.decide(decisionAt{fn: fn, label: "exit", index: root.Index, pos: fn.Pos(), context: context}, incompatibleReturn, compatible)
		if !compatible {
			states := slices.SortedFunc(maps.Keys(wc.returnStates), func(a, b string) int {
				return cmp.Or(strings.Compare(wc.returnStates[a], wc.returnStates[b]), strings.Compare(a, b))
			})
			if _, ok := pc.forced[pc.positionKey(fn.Pos())]; !ok && !lff.Ignore {
				pc.maybeFail(fn.Pos(), "incompatible return states (first: %s, second: %s)", wc.returnStates[states[0]], wc.returnStates[states[1]])
			} else {
				pc.suppressed()
			}
		}
	}

	if pc.overBudget && outer != nil {
		return wc
	}
	if wc.gaveUp || pc.overBudget {
		pc.recordGaveUp(fn)
		pc.records, pc.pending = pc.records[:recordsAt], pc.pending[:pendingAt]
		pc.withdrawDecisions(decisionsAt)
		if outer != nil {
			// Take back what this walk marked done; fn and its closures are marked again through givenUp.
			for _, f := range pc.marked[markedAt:] {
				delete(pc.functions, f)
			}
			pc.marked = pc.marked[:markedAt]
		}
		pc.statOverflow()
		pc.decide(decisionAt{fn: fn, label: "overflow", pos: fn.Pos(), context: context}, loopOverflow, false)
		if _, ok := pc.forced[pc.positionKey(fn.Pos())]; !ok && !lff.Ignore {
			if pc.overBudget {
				pc.maybeFail(fn.Pos(), "checklocks gave up on this function: more than %d block walks", maxFunctionWalks)
			} else {
				pc.maybeFail(fn.Pos(), "checklocks gave up on this function: more than %d lock states reach one block in a loop", maxCycleStates)
			}
		} else {
			pc.suppressed()
		}
		// Loop B must not analyse the closures of a function given up on, out of the context it reached them in.
		for work := []*ssa.Function{fn}; len(work) > 0; {
			f := work[len(work)-1]
			work = append(work[:len(work)-1], f.AnonFuncs...)
			if outer == nil {
				pc.functions[f] = struct{}{}
			} else if _, ok := pc.givenUpSet[f]; !ok {
				if pc.givenUpSet == nil {
					pc.givenUpSet = make(map[*ssa.Function]struct{})
				}
				pc.givenUpSet[f] = struct{}{}
				pc.givenUp = append(pc.givenUp, f)
			}
		}
	}

	if outer == nil {
		if !wc.gaveUp && !pc.overBudget {
			pc.joinDefers(wc, recordsAt)
		}
		pc.replay()
		for _, f := range pc.givenUp {
			pc.functions[f] = struct{}{}
		}
	}

	// Update all lock state accordingly. This will be called only if we
	// are doing inline analysis for e.g. an anonymous function.
	if parent != nil {
		pc.postFunctionCallUpdate(site, lff, parent, outer.context)
	}
	return wc
}

// checkInferred checks for any inferred lock annotations.
func (pc *passContext) checkInferred() {
	// Resolved positions order these as a reader meets them; a raw token.Pos depends on how the
	// file set was filled. Package and name separate objects that share a position.
	pkgPath := func(o types.Object) string {
		if o.Pkg() == nil {
			return ""
		}
		return o.Pkg().Path()
	}
	byPosition := func(a, b types.Object) int {
		return cmp.Or(comparePos(pc.pass.Fset, a.Pos(), b.Pos()),
			strings.Compare(pkgPath(a), pkgPath(b)), strings.Compare(a.Name(), b.Name()))
	}
	for _, obj := range slices.SortedFunc(maps.Keys(pc.observations), byPosition) {
		oo := pc.observations[obj]
		var lgf lockGuardFacts
		pc.importLockGuardFacts(obj, &lgf)
		for _, other := range slices.SortedFunc(maps.Keys(oo.counts), byPosition) {
			count := oo.counts[other]
			// Is this already a guard?
			if _, ok := lgf.GuardedBy[other.Name()]; ok {
				continue
			}
			// Check to see if this field is used with a given lock
			// held above the threshold. If yes, provide a helpful
			// hint that this may something you wish to annotate.
			const threshold = 0.9
			if usage := float64(count) / float64(oo.total); usage >= threshold {
				pc.maybeFail(obj.Pos(), "may require checklocks annotation for %s, used with lock held %2.0f%% of the time", other.Name(), usage*100)
			}
		}
	}
}
