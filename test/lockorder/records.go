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

// A caller's lock released on one path only, then the paths join.
//
// +checklocks:a.mu
func (a *App) zzReleaseOnOnePath(p *Partition, c bool) { // +checklocksfail=incompatible return states
	if c {
		a.mu.Unlock() // +lockgapfail=releases a lock its caller holds
	}
	p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
	p.Unlock()
} // +checklocksfail=not held exclusively

// An aliased lock taken inside an annotated literal that is passed away.
func zzAliasInAnnotatedLiteral(run func(func(*Context, *seamOuter, *Partition))) {
	// +checklocks:c.mu
	run(func(c *Context, o *seamOuter, p *Partition) {
		o.inner.mu.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		o.inner.mu.Unlock()
	})
}

// An aliased lock taken inside a go literal.
func zzAliasInGoLiteral(o *seamOuter, p *Partition) {
	go func() {
		o.inner.mu.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		o.inner.mu.Unlock()
	}()
}

// An aliased lock taken inside a deferred literal.
func zzAliasInDeferredLiteral(o *seamOuter, p *Partition) {
	defer func() {
		o.inner.mu.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		o.inner.mu.Unlock()
	}()
}

// An aliased lock taken inside a returned literal.
func zzAliasInReturnedLiteral(o *seamOuter, p *Partition) func() {
	return func() {
		o.inner.mu.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		o.inner.mu.Unlock()
	}
}

// A plain lock taken inside a literal nested in a go literal.
func zzPlainInNestedLiteral(a *App, p *Partition) {
	go func() {
		func() {
			a.Lock()
			p.Lock() // +lockorderfail=acquiring Partition
			p.Unlock()
			a.Unlock()
		}()
	}()
}

// An annotated literal passed away that takes a lock of the class it holds on entry.
func zzAnnotatedLiteralSameClass(run func(func(*App, *App))) {
	// +checklocks:a.mu
	run(func(a, b *App) {
		b.Lock()
		b.Unlock()
	})
}

// A deferred literal run at two returns that hold different locks.
func zzTwoReturnsLiteral(a *App, o *seamOuter, p *Partition, c bool) { // +checklocksfail=incompatible return states
	defer func() {
		p.Lock()
		p.Unlock()
	}()
	if c {
		a.Lock()
		return // +checklocksfail=return with unexpected locks held
	}
	o.inner.mu.Lock()
} // +checklocksfail=return with unexpected locks held

// A literal that defers a no-op call, run inside a caller that defers a literal.
func zzProbeNoIgnore(a *App, t *Context, p *Partition, c bool) {
	defer func() {
		p.Lock()
		p.Unlock()
	}()
	a.Lock()
	func() { // +checklocksfail=incompatible return states
		defer djNoop()
		if c {
			t.Lock()
		}
	}() // +checklocksfail=return with unexpected locks held
	a.Unlock()
}

// A literal that returns early with a lock held, run inside a caller that defers a literal.
func zzProbeLegacyWalk(a *App, p *Partition, c bool) {
	defer func() {
		p.Lock()
		p.Unlock()
	}()
	func() { // +checklocksfail=incompatible return states
		defer djNoop()
		if c {
			a.Lock()
			return // +checklocksfail=return with unexpected locks held
		}
	}()
	if c {
		a.Unlock()
	}
}

// Pins for the flip: a function the engine gave up in takes App then Partition.
func zzGiveUpOrder(f *litSeven, a *App, p *Partition, c func() bool) { // +checklocksfail=checklocks gave up on this function
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
	a.Lock()
	p.Lock() // +lockorderfail=acquiring Partition
	p.Unlock()
	a.Unlock()
}

// Pins for the flip: the exported summary of a given-up function, seen from a caller holding Partition.
func zzCallsGiveUpOrder(f *litSeven, a *App, p *Partition, c func() bool) {
	p.Lock()
	zzGiveUpOrder(f, a, p, c) // +lockorderfail=acquiring Partition
	p.Unlock()
}

// Pins for the flip: zzGiveUpOrder under +checklocksignore, so no give-up row is printed.
//
// +checklocksignore
func zzGiveUpOrderIgnored(f *litSeven, a *App, p *Partition, c func() bool) {
	for c() {
		if c() {
			f.a.Lock()
		}
		if c() {
			f.b.Lock()
		}
		if c() {
			f.c.Lock()
		}
		if c() {
			f.d.Lock()
		}
		if c() {
			f.e.Lock()
		}
		if c() {
			f.f.Lock()
		}
		if c() {
			f.g.Lock()
		}
	}
	a.Lock()
	p.Lock() // +lockorderfail=acquiring Partition
	p.Unlock()
	a.Unlock()
}
