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

package lockorder

// A go literal's own lock order.
func zzGoLiteralInverts(a *App, p *Partition) {
	go func() {
		a.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		a.Unlock()
	}()
}

// A callback literal's own lock order.
func zzCallbackInverts(a *App, p *Partition, run func(func())) {
	run(func() {
		a.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		a.Unlock()
	})
}

// A deferred literal's own lock order.
func zzDeferredLiteralInverts(a *App, p *Partition) {
	defer func() {
		a.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		a.Unlock()
	}()
}

// A literal called inline while the caller holds a lock.
func zzInlineLiteralInherits(a *App, p *Partition) {
	a.Lock()
	func() {
		p.Lock()
		p.Unlock()
	}()
	a.Unlock()
}

// A literal called inline while the caller holds an aliased lock.
func zzAliasBodyLiteral(o *seamOuter, p *Partition) {
	o.inner.mu.Lock()
	func() {
		p.Lock()
		p.Unlock()
	}()
	o.inner.mu.Unlock()
}

// A literal in a function that holds an aliased lock on entry.
//
// +checklocks:o.inner.mu
func zzAliasEntryLiteral(o *seamOuter, p *Partition) {
	func() {
		p.Lock()
		p.Unlock()
	}()
}

// A literal called inline while the caller holds a lock taken through a wrapper.
func zzPlainBodyLiteral(a *App, p *Partition) {
	seamLockApp(a)
	func() {
		p.Lock()
		p.Unlock()
	}()
	seamUnlockApp(a)
}

// A deferred call that runs at two returns with different locks held.
func zzTwoReturnsOrder(a *App, o *seamOuter, p *Partition, c bool) { // +checklocksfail=incompatible return states
	defer p.Unlock()
	defer p.Lock() // +lockorderfail=while holding App:|while holding SeamInner:
	if c {
		a.Lock()
		return // +checklocksfail=return with unexpected locks held
	}
	o.inner.mu.Lock()
} // +checklocksfail=return with unexpected locks held

// An aliased lock taken inside a literal.
func zzAliasTakenInLiteral(o *seamOuter, p *Partition) {
	func() {
		o.inner.mu.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		o.inner.mu.Unlock()
	}()
}

// A plain lock taken inside a literal.
func zzPlainTakenInLiteral(a *App, p *Partition) {
	func() {
		a.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		a.Unlock()
	}()
}

// A lock taken through a wrapper inside a literal.
func zzWrapperTakenInLiteral(a *App, p *Partition) {
	func() {
		seamLockApp(a)
		p.Lock()
		p.Unlock()
		seamUnlockApp(a)
	}()
}
