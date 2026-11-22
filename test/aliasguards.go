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

package test

import (
	"sync"
	"sync/atomic"

	"github.com/tigerquoll/checklocks/test/crosspkg"
)

type alInner struct {
	mu *sync.RWMutex
}

// +checklocksalias:in.mu=mu
type alO struct {
	mu sync.RWMutex
	in alInner

	// +checklocks:mu
	x int
}

// Two names for one lock are reported at the definition, and callers see one guard.

// +checklocks:o.mu
// +checklocks:o.in.mu
func alReqPair(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already required
	o.x = 1
}

func alCallReqPair(o *alO) {
	o.mu.Lock()
	alReqPair(o)
	o.mu.Unlock()
}

// +checklocksacquire:o.mu
// +checklocksacquire:o.in.mu
func alAcqPair(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already acquired
	o.mu.Lock()
}

// +checklocksrelease:o.mu
// +checklocksrelease:o.in.mu
func alRelPair(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already required
	o.mu.Unlock()
}

func alCallAcqRel(o *alO) {
	alAcqPair(o)
	o.x = 2
	alRelPair(o)
}

type alEmb struct {
	mu sync.Mutex
}

type alE struct {
	alEmb

	// +checklocks:mu
	v int
}

// +checklocks:e.mu
// +checklocks:e.alEmb.mu
func alReqEmb(e *alE) { // +checklocksfail=annotation e.alEmb.mu specified more than once, already required
	e.v = 1
}

// +checklocksacquire:e.mu
// +checklocksacquire:e.alEmb.mu
func alAcqEmb(e *alE) { // +checklocksfail=annotation e.alEmb.mu specified more than once, already acquired
	e.mu.Lock()
}

func alCallEmb(e *alE) {
	e.mu.Lock()
	alReqEmb(e)
	e.mu.Unlock()
	alAcqEmb(e)
	e.v = 2
	e.mu.Unlock()
}

// The dropped name leaves the stronger mode behind, in either order.

// +checklocksread:o.in.mu
// +checklocks:o.mu
func alReadThenWrite(o *alO) { // +checklocksfail=annotation o.mu specified more than once, already required
	o.x = 1
}

// +checklocks:o.mu
// +checklocksread:o.in.mu
func alWriteThenRead(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already required
	o.x = 1
}

func alCallModesRead(o *alO) {
	o.mu.RLock()
	alReadThenWrite(o) // +checklocksfail=must hold o.in.mu exclusively
	alWriteThenRead(o) // +checklocksfail=must hold o.mu exclusively
	o.mu.RUnlock()
}

func alCallModesWrite(o *alO) {
	o.mu.Lock()
	alReadThenWrite(o)
	alWriteThenRead(o)
	o.mu.Unlock()
}

// +checklocks:o.mu
// +checklocksacquire:o.in.mu
func alReqThenAcq(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already required
	o.x = 1
}

// +checklocks:o.mu
// +checklocksrelease:o.in.mu
func alReqThenRel(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already required
	o.x = 1
}

// +checklocksacquire:o.in.mu
// +checklocks:o.mu
func alAcqThenReq(o *alO) { // +checklocksfail=annotation o.mu specified more than once, already acquired
	o.mu.Lock()
}

func alCallRoles(o *alO) {
	o.mu.Lock()
	alReqThenAcq(o)
	alReqThenRel(o)
	o.mu.Unlock()
	alAcqThenReq(o)
	o.mu.Unlock()
}

// The any-mode exclusion wins, in either order.

// +checklocksexclude:o.mu
// +checklocksexcludewrite:o.in.mu
func alExAnyFirst(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already forbidden
}

// +checklocksexcludewrite:o.mu
// +checklocksexclude:o.in.mu
func alExWriteFirst(o *alO) { // +checklocksfail=annotation o.in.mu specified more than once, already forbidden
}

func alCallExcludesRead(o *alO) {
	o.mu.RLock()
	alExAnyFirst(o)   // +checklocksfail=must not hold o.mu
	alExWriteFirst(o) // +checklocksfail=must not hold o.mu
	o.mu.RUnlock()
}

func alCallExcludesWrite(o *alO) {
	o.mu.Lock()
	alExAnyFirst(o) // +checklocksfail=must not hold o.mu
	o.mu.Unlock()
}

// +checklocksexcludewrite:o.mu
// +checklocksread:o.in.mu
// +checklocksread:o.mu
func alThreeNames(o *alO) { // +checklocksfail=annotation o.mu specified more than once, already required
	_ = o.x
}

func alCallThree(o *alO) {
	o.mu.RLock()
	alThreeNames(o)
	o.mu.RUnlock()
}

// A read guard and a write exclusion on one lock are a valid pair.

// +checklocksexcludewrite:o.mu
// +checklocksread:o.in.mu
func alExThenRead(o *alO) {
	_ = o.x
}

// +checklocksread:o.in.mu
// +checklocksexcludewrite:o.mu
func alReadThenEx(o *alO) {
	_ = o.x
}

func alCallExRead(o *alO) {
	o.mu.RLock()
	alExThenRead(o)
	alReadThenEx(o)
	o.mu.RUnlock()
}

// One lock reached through two parameters stays two guards.

type alT struct {
	mu sync.Mutex
}

// +checklocksrelease:a.mu
// +checklocksrelease:b.mu
func alUnlockBoth(a, b *alT) {
	a.mu.Unlock()
	b.mu.Unlock()
}

func alSameArgument(x *alT) {
	x.mu.Lock()
	alUnlockBoth(x, x) // +checklocksfail=but not held
}

func alDistinctArguments(x, y *alT) {
	x.mu.Lock()
	y.mu.Lock()
	alUnlockBoth(x, y)
}

// A literal keeps both names.
func alClosure(run func(func(*alO))) {
	// +checklocks:o.mu
	// +checklocks:o.in.mu
	f := func(o *alO) { // +checklocksfail=acquired multiple times or differently
		o.x = 1
		// A literal's alias duplicate guards are not merged, so one exit lock counts as two.
	} // +checklocksfail=return with unexpected locks held
	run(f)
}

// The lock a method takes on its receiver is already named by an annotation.

// +checklocks:o.in.mu
func (o *alO) alRelock() {
	o.mu.Unlock() // +lockgapfail=taken again
	o.mu.Lock()
}

// +checklocksexclude:o.in.mu
func (o *alO) alTakes() {
	o.mu.Lock()
	o.mu.Unlock()
}

func alCallRelock(o *alO) {
	o.mu.Lock()
	o.alRelock()
	o.mu.Unlock()
}

func alCallTakes(o *alO) {
	o.mu.Lock()
	o.alTakes() // +checklocksfail=must not hold o.in.mu
	o.mu.Unlock()
}

// A field with two guards for one lock counts as guarded by a held lock.

// +checklocksalias:in.mu=mu
type alAtomic struct {
	mu sync.RWMutex
	in alInner

	// +checklocks:mu
	// +checklocks:in.mu
	// +checkatomic
	n int32
}

func alAtomicRead(o *alAtomic) int32 {
	o.mu.Lock()
	v := o.n
	o.mu.Unlock()
	return v
}

func alAtomicStore(o *alAtomic) {
	o.mu.Lock()
	atomic.StoreInt32(&o.n, 1)
	o.mu.Unlock()
}

func alAtomicUnheld(o *alAtomic) int32 {
	return o.n // +checklocksfail=illegal use of atomic-only field
}

func alCallCross(o *crosspkg.AliasPair) {
	o.Mu.Lock()
	crosspkg.AliasedReq(o)
	o.Mu.Unlock()
}

// A declared exclusion is widened, never narrowed, by the lock the body takes.

// +checklocksexcludewrite:o.in.mu
func (o *alO) alWidenTakes() {
	o.mu.Lock()
	o.mu.Unlock()
}

// +checklocksexclude:o.in.mu
func (o *alO) alKeepTakesRead() {
	o.mu.RLock()
	o.mu.RUnlock()
}

// +checklocksread:o.in.mu
// +checklocksexcludewrite:o.mu
func (o *alO) alHeldBeatsExclude() {
	o.mu.RUnlock() // +lockgapfail=released here and taken again
	o.mu.Lock()
	o.mu.Unlock() // +lockgapfail=released here and taken again
	o.mu.RLock()
}

func alCallWiden(o *alO) {
	o.mu.RLock()
	o.alWidenTakes()    // +checklocksfail=must not hold o.in.mu (
	o.alKeepTakesRead() // +checklocksfail=must not hold o.in.mu (
	o.alHeldBeatsExclude()
	o.mu.RUnlock()
}

// An exclusion before an acquire is redundant, not contradictory.

// +checklocksexclude:o.in.mu
// +checklocksacquire:o.mu
func alExThenAcq(o *alO) {
	o.mu.Lock()
}

// +checklocksacquire:o.mu
// +checklocksexclude:o.in.mu
func alAcqThenEx(o *alO) {
	o.mu.Lock()
}

// +checklocksexclude:o.mu
// +checklocksacquire:o.mu
func alExThenAcqSame(o *alO) {
	o.mu.Lock()
}

func alCallExAcq(o *alO) {
	o.mu.Lock()
	alExThenAcq(o)     // +checklocksfail=must not hold o.in.mu (|attempt to acquire
	alAcqThenEx(o)     // +checklocksfail=must not hold o.in.mu (|attempt to acquire
	alExThenAcqSame(o) // +checklocksfail=must not hold o.mu (|attempt to acquire
	o.mu.Unlock()
}

// An exclusion with a lock held on entry is a contradiction.

// +checklocksexclude:o.in.mu
// +checklocksrelease:o.mu
func alExThenRel(o *alO) { // +checklocksfail=annotation o.mu cannot be both required and forbidden
	o.mu.Unlock() // +checklocksfail=already unlocked or locked differently
}

// +checklocksexclude:o.mu
// +checklocks:o.mu
func alExThenGuardSame(o *alO) { // +checklocksfail=annotation o.mu cannot be both required and forbidden
	o.x = 1 // +checklocksfail=must be locked when accessing x
}

// Modes merge only between annotations of one role.

// +checklocksread:o.in.mu
// +checklocksacquire:o.mu
func alReadThenAcq(o *alO) { // +checklocksfail=annotation o.mu specified more than once, already required
	_ = o.x
	return
}

// +checklocksread:o.in.mu
// +checklocksrelease:o.mu
func alReadThenRel(o *alO) { // +checklocksfail=annotation o.mu specified more than once, already required
	_ = o.x
	return
}
