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
			pc.maybeFail(inst.Pos(), "invalid field access, %s (%s) must be locked when accessing %s (locks: %s)", guardName, s, accessObj.Name(), ls.String())
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
			pc.maybeFail(inst.Pos(), "non-atomic write of field %s, writes must still be atomic with locks held (locks: %s)", accessObj.Name(), ls.String())
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
	if pc.counted == nil {
		pc.counted = make(map[observationKey]struct{})
	}
	pc.counted[key] = struct{}{}

	oo := pc.observationsFor(accessObj)
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
			fnls := ls.fork()
			for i, arg := range site.Common().Args {
				fnls.bind(fn.Params[i], arg)
			}
			pc.checkFunction(site, wc, fn, &nlff, fnls, true /* force */)
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

	// Release all locks not still held.
	for _, g := range resolveGuards(ls, lff.HeldOnEntry, lff.HeldOnExit, resolve) {
		if g.rv.unavailable {
			continue
		}
		resolved := g.rv.valid()
		pc.decide(at, effectResolve, resolved, g.fieldName)
		if !resolved {
			// See above: this cannot be forced.
			pc.maybeFail(site.Pos(), "field %s cannot be resolved", g.fieldName)
			continue
		}
		p, ok := ls.unlockField(g.rv, g.fg.Exclusive)
		pc.decide(at, release, ok, g.fieldName)
		if !ok {
			if lff.Ignore {
				pc.suppressed()
			} else if _, forced := pc.forced[pc.positionKey(site.Pos())]; forced {
				pc.suppressed()
			} else {
				pc.maybeFail(site.Pos(), "attempt to release %s (%s), but not held (locks: %s)", g.fieldName, p.String(), ls.String())
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
			pc.maybeFail(site.Pos(), "lock %s is acquired through a discarded result and cannot be tracked", g.fieldName)
			continue
		}
		// Acquire the lock per the annotation.
		p, ok := ls.lockField(g.rv, g.exclusive)
		pc.decide(at, acquire, ok, g.fieldName)
		if !ok {
			// The lock is really taken here, whatever assumed it earlier.
			ls.setAssumed(p, false)
			if _, ok := pc.forced[pc.positionKey(site.Pos())]; !ok && !lff.Ignore {
				pc.maybeFail(site.Pos(), "attempt to acquire %s (%s), but already held (locks: %s)", g.fieldName, p.String(), ls.String())
			} else {
				pc.suppressed()
			}
		}
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
					pc.maybeFail(callPos, "must not hold %s exclusively (%s) to call %s, but held (locks: %s)", g.fieldName, p.String(), fn.Name(), ls.String())
				} else {
					pc.maybeFail(callPos, "must not hold %s (%s) to call %s, but held (locks: %s)", g.fieldName, p.String(), fn.Name(), ls.String())
				}
			} else {
				pc.suppressed()
			}
		}
	}

	// Check that what is passed to a parameter declared to arrive unpublished is an
	// object nothing else can reach yet. This is the other half of the check that makes
	// the declaration mean something; the first half is at the constructor.
	pc.checkFreshArgs(wc, site.instr)

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
				pc.maybeFail(callPos, "must hold %s %s (%s) to call %s, but not held (locks: %s)", g.fieldName, exclusiveStr(g.fg.Exclusive), p.String(), fn.Name(), ls.String())
			} else {
				pc.suppressed()
				if forcedPath, inserted := ls.lockField(g.rv, g.exclusive); inserted {
					// The force asserts the lock, it does not take it.
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
			if !ok {
				// The lock is really taken here, whatever assumed it earlier.
				ls.setAssumed(p, false)
				if !lff.Ignore && !forced {
					// Double locking a mutex that is already locked.
					pc.maybeFail(callPos, "%s already locked (locks: %s)", p.String(), ls.String())
				} else {
					pc.suppressed()
				}
			}
		case opRelease:
			p, ok := ls.unlockField(rv, exclusive)
			pc.decide(at, lockOpUnlock, ok)
			if !ok {
				if !forced && !lff.Ignore {
					// Unlocking something that is already unlocked.
					pc.maybeFail(callPos, "%s already unlocked or locked differently (locks: %s)", p.String(), ls.String())
				} else {
					pc.suppressed()
				}
			}
		case opDowngrade:
			p, ok := ls.downgradeField(rv)
			pc.decide(at, lockOpDowngrade, ok)
			if !ok {
				if !forced && !lff.Ignore {
					// Downgrading something that may not be downgraded.
					pc.maybeFail(callPos, "%s already unlocked or not exclusive (locks: %s)", p.String(), ls.String())
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

// checkClosure forks the lock state and binds the closure's free variables and
// parameters. This allows the analysis to resolve the closure.
func (pc *passContext) checkClosure(site callSite, wc *walkContext, fn *ssa.MakeClosure, ls *lockState) {
	clls := ls.fork()
	clfn := fn.Fn.(*ssa.Function)
	for i, fv := range clfn.FreeVars {
		clls.store(fv, fn.Bindings[i])
	}
	for i, arg := range site.Common().Args {
		clls.bind(clfn.Params[i], arg)
	}

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
	pc.checkFunction(site, wc, clfn, &nlff, clls, true /* force */)
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

	// context names the walk in the decision log; see inlineContext.
	context string
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
	case *ssa.Go:
		pc.checkGoCall(newCallSite(x), wc, ls)
	case *ssa.Defer:
		ls.pushDefer(x)
	case *ssa.RunDefers:
		for d := ls.popDefer(); d != nil; d = ls.popDefer() {
			pc.checkCall(newCallSite(d), wc, ls)
		}
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
		nlff := lockFunctionFacts{
			Ignore: wc.lff.Ignore || pc.literalIgnored(clfn),
		}
		pc.checkFunction(callSite{kind: siteEntry}, wc, clfn, &nlff, nil, false /* force */)
	case *ssa.Return:
		return x, ls // Valid return state.
	}
	return nil, nil
}

// blockRecord is the input a walk entered a block with, kept for the replay.
type blockRecord struct {
	wc     *walkContext
	block  *ssa.BasicBlock
	parent *lockState
	parity blockParity
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
			at := decisionAt{inst: rv, pos: rv.Pos(), context: wc.context}
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
					pc.maybeFail(rv.Pos(), "lock %s cannot be resolved", g.fieldName)
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
				if _, ok := pc.forced[pc.positionKey(rv.Pos())]; !ok && !wc.lff.Ignore {
					pc.maybeFail(rv.Pos(), "lock %s (%s) not held %s (locks: %s)", g.fieldName, p.String(), exclusiveStr(g.fg.Exclusive), rls.String())
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
					pc.maybeFail(rv.Pos(), "return with unexpected locks held (locks: %s)", rls.String())
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
	for _, r := range pc.records {
		for run := 0; run < replayRuns; run++ {
			pc.current = r
			n := len(pc.records)
			ls, _ := pc.transferBlock(r.wc, r.block, r.parent)
			r.parity.replayed(r, ls, len(pc.records) != n)
		}
	}
	pc.current, pc.emit, pc.records = nil, outerEmit, nil
}

// checkBasicBlock traverses the control flow graph starting at a set of given
// block and checks each instruction for allowed operations.
func (pc *passContext) checkBasicBlock(wc *walkContext, block *ssa.BasicBlock, parent *lockState, seen map[*ssa.BasicBlock]*lockState, rg map[*ssa.BasicBlock]struct{}) *lockState {
	// Check for cached results from entering this block from a *different*
	// execution path. Note that this is not the same path, which is
	// checked with the recursion guard below.
	if oldLS, ok := seen[block]; ok && oldLS.isCompatible(parent) {
		return nil
	}

	// Prevent recursion. If the lock state is constantly changing and we
	// are a recursive path, then there will never be a return block.
	if rg == nil {
		rg = make(map[*ssa.BasicBlock]struct{})
	}
	if _, ok := rg[block]; ok {
		return nil
	}
	rg[block] = struct{}{}
	defer func() { delete(rg, block) }()

	// If the lock state is not compatible, then we need to do the
	// recursive analysis to ensure that it is still sane. For example, the
	// following is guaranteed to generate incompatible locking states:
	//
	//	if foo {
	//		mu.Lock()
	//	}
	//	other stuff ...
	//	if foo {
	//		mu.Unlock()
	//	}

	// Analyze this block.
	seen[block] = parent
	rec := &blockRecord{wc: wc, block: block, parent: parent}
	pc.records = append(pc.records, rec)
	outerRec, outerEmit := pc.current, pc.emit
	pc.current, pc.emit = rec, emitWalk
	ls, rls := pc.transferBlock(wc, block, parent)
	pc.current, pc.emit = outerRec, outerEmit
	rec.parity.walked(ls)

	// Analyze all successors.
	for _, succ := range block.Succs {
		// Collect possible return values, and make sure that the lock
		// state aligns with any return value that we may have found
		// above. Note that checkBasicBlock will recursively analyze
		// the lock state to ensure that Releases and Acquires are
		// respected.
		if pls := pc.checkBasicBlock(wc, succ, ls, seen, rg); pls != nil {
			// The states of one walk are gathered for checkFunction to compare:
			// which two of them are neighbours here depends on the block graph.
			text, summary := pls.acquiredString(wc.entryLocks)
			wc.returnStates[summary] = text
			rls = pls
		}
	}
	return rls
}

// checkFunction checks a function invocation, typically starting with nil lockState.
func (pc *passContext) checkFunction(site callSite, outer *walkContext, fn *ssa.Function, lff *lockFunctionFacts, parent *lockState, force bool) {
	if pc.emit == emitReplay {
		return
	}
	outerEmit := pc.emit
	pc.emit = emitDirect
	defer func() { pc.emit = outerEmit }()
	pc.decisions.enter()
	defer pc.decisions.leave()
	defer func() {
		// Mark this function as checked. This is used by the top-level
		// loop to ensure that all anonymous functions are scanned, if
		// they are not explicitly invoked here. Note that this can
		// happen if the anonymous functions are e.g. passed only as
		// parameters or used to initialize some structure.
		pc.functions[fn] = struct{}{}
	}()
	if _, ok := pc.functions[fn]; !force && ok {
		// This function has already been analyzed at least once.
		// That's all we permit for each function, although this may
		// cause some anonymous functions to be analyzed in only one
		// context.
		return
	}

	// Initialize ls with any preconditions that require locks to be held
	// for the method to be invoked. Note that in the overwhelming majority
	// of cases, parent will be nil. However, in the case of closures and
	// anonymous functions, we may start with a non-nil lock state.
	ls := parent.fork()
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
		if !ok {
			if !lff.Ignore {
				// This can only happen if the same value is declared
				// multiple times, and should be caught by the earlier
				// fact scanning. Keep it here as a sanity check.
				pc.maybeFail(fn.Pos(), "lock %s (%s) acquired multiple times or differently (locks: %s)", g.fieldName, p.String(), ls.String())
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
	wc := &walkContext{fn: fn, lff: lff, fresh: pc.computeFresh(fn, outerFresh), context: context}

	seen := make(map[*ssa.BasicBlock]*lockState)

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
	roots := make([]*ssa.BasicBlock, 0, 2)
	if len(fn.Blocks) > 0 {
		roots = append(roots, fn.Blocks[0])
	}
	if fn.Recover != nil {
		roots = append(roots, fn.Recover)
	}
	for _, root := range roots {
		// A state the body returns in is no alternative to one the recover block returns in.
		wc.returnStates = make(map[string]string)
		pc.checkBasicBlock(wc, root, ls, seen, nil)
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

	if outer == nil {
		pc.replay()
	}

	// Update all lock state accordingly. This will be called only if we
	// are doing inline analysis for e.g. an anonymous function.
	if parent != nil {
		pc.postFunctionCallUpdate(site, lff, parent, outer.context)
	}
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
