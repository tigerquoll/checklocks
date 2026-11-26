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

import "sync"

// Pins the G1 gap: a deferred literal run during a panic, after an inline literal that defers and conditionally locks.
func zzForeignLit(a *App, p *Partition, t *Context, c bool) {
	defer func() {
		a.Lock()
		p.Lock()
		p.Unlock()
		a.Unlock()
	}()
	func() { // +checklocksfail=incompatible return states
		defer djNoop()
		if c {
			t.Lock()
		}
	}() // +checklocksfail=return with unexpected locks held
	panic("x")
}

// Pins the G1 gap with an endless loop in place of the panic.
func zzForeignLitLoop(a *App, p *Partition, t *Context, c bool) {
	defer func() {
		a.Lock()
		p.Lock()
		p.Unlock()
		a.Unlock()
	}()
	func() { // +checklocksfail=incompatible return states
		defer djNoop()
		if c {
			t.Lock()
		}
	}() // +checklocksfail=return with unexpected locks held
	for {
	}
}

// Pins a deferred call checked at each return that runs it; both states report it.
func zzUnionReturns(a *App, p *Partition, c bool) { // +checklocksfail=incompatible return states
	a.Lock()
	defer takesThePartition(p) // +lockorderfail=acquiring Partition
	if c {
		return // +checklocksfail=return with unexpected locks held
	}
	a.Unlock()
}

// Pins the union at a deferred literal: the literal's lock is checked against every return's held locks.
func zzUnionReturnsLiteral(a *App, p *Partition, c bool) { // +checklocksfail=incompatible return states
	a.Lock()
	defer func() {
		p.Lock() // +lockorderfail=acquiring Partition (via (*Partition).Lock) while holding App
		p.Unlock()
	}()
	if c {
		return // +checklocksfail=return with unexpected locks held
	}
	a.Unlock()
}

func zzYield(yield func(int) bool) { yield(1) }

// Pins a lock held across a range-over-func body: neither state reports the nesting inside the body.
func zzRangeFuncHeld(a *App, p *Partition) {
	a.Lock()
	for range zzYield {
		p.Lock()
		p.Unlock()
	}
	a.Unlock()
}

// Pins a deferred call in the body of a range-over-func loop.
func zzRangeFuncDefer(a *App, p *Partition) {
	a.Lock()
	for range zzYield {
		defer takesThePartition(p)
	}
	a.Unlock()
}

// Pins the union callerOwned reads at a deferred release: a body lock of the released class is held at one of the two returns.
//
// +checklocks:a.mu
// +checklocksignore
func (a *App) zzDeferUnionReleased(b *App, wg *sync.WaitGroup, c bool) {
	if !c {
		defer wg.Wait() // +lockblockingfail=calling (*WaitGroup).Wait
	}
	defer a.mu.Unlock()
	if c {
		b.mu.Lock() // +lockorderfail=acquiring App (via (*RWMutex).Lock) while holding App
		return
	}
}

func zzCallsDeferUnionReleased(a, b *App, wg *sync.WaitGroup, c bool) {
	a.mu.Lock()
	a.zzDeferUnionReleased(b, wg, c) // +lockorderfail=acquiring App (via (*App).zzDeferUnionReleased) while holding App +lockblockingfail=calling (*App).zzDeferUnionReleased
	a.mu.Unlock()
}

// Pins one skipped row per pass when a literal and the function around it both gave up.
func zzGiveUpBoth(f *litSeven, c func() bool) { // +checklocksfail=checklocks gave up on this function +lockorderfail=lockorder skipped this function +lockblockingfail=lockblocking skipped this function
	func() {
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
	}()
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
}

// Pins that a function's lockorder ignore covers the literal it calls inline.
//
// +lockorderignore
func zzProbeIgnoredLit(a *App, p *Partition) {
	a.Lock()
	func() {
		p.Lock()
		p.Unlock()
	}()
	a.Unlock()
}

// Pins that a literal started with go is not covered by its function's lockorder ignore.
//
// +lockorderignore
func zzProbeIgnoredGoLit(a *App, p *Partition) {
	go func() {
		a.Lock()
		p.Lock() // +lockorderfail=acquiring Partition
		p.Unlock()
		a.Unlock()
	}()
}

// Pins that a literal's give-up row follows the ignore of the function around it.
//
// +checklocksignore +lockorderignore +lockblockingignore
func zzGiveUpLiteralIgnored(f *litSeven, c func() bool) {
	func() {
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
	}()
}

// Pins that a literal the function defers is covered by its lockorder ignore.
//
// +lockorderignore
func zzProbeIgnoredDeferLit(a *App, p *Partition) {
	a.Lock()
	defer a.Unlock()
	defer func() {
		p.Lock()
		p.Unlock()
	}()
}

// Pins the skipped rows for a go literal that gave up inside a function under both ignores.
//
// +lockorderignore +lockblockingignore
func zzGiveUpGoLiteralIgnored(f *litSeven, c func() bool) {
	go func() { // +checklocksfail=checklocks gave up on this function +lockorderfail=lockorder skipped this function +lockblockingfail=lockblocking skipped this function
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
	}()
}

// Pins that a range-over-func loop body follows its function's lockorder ignore: no row.
//
// +lockorderignore
func zzIgnoredRangeBody(a *App, p *Partition) {
	for range zzYield {
		a.Lock()
		p.Lock()
		p.Unlock()
		a.Unlock()
	}
}

// Pins that a go literal gets the skipped rows when its function gave up under both ignores.
//
// +lockorderignore +lockblockingignore
func zzGiveUpFunctionGoLiteral(f *litSeven, a *App, p *Partition, c func() bool) { // +checklocksfail=checklocks gave up on this function
	go func() { // +lockorderfail=lockorder skipped this function +lockblockingfail=lockblocking skipped this function
		a.Lock()
		p.Lock()
		p.Unlock()
		a.Unlock()
	}()
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
}
