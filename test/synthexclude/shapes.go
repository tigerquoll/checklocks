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

// Shapes of receiver access that decide whether a method is derived as taking its own lock.
// A holding caller is reported where the shape is attributed and silent where it is not.

package synthexclude

import "sync"

// A mutex whose Unlock returns a value, so go vet's copylocks does not take it for a lock
// and the shapes that copy it stay vet-clean.
type shMutex struct{ held bool }

func (m *shMutex) Lock()        {}
func (m *shMutex) Unlock() bool { return m.held }

// Value receiver, value lock: the lock is reached through the copy of the receiver.
type shC struct{ mu shMutex }

func (s shC) M()     { s.mu.Lock(); s.mu.Unlock() }
func (s *shC) N()    { s.M() }
func callShC(s *shC) { s.mu.Lock(); s.N(); s.mu.Unlock() } // +checklocksfail=must not hold

// Value receiver, pointer lock.
type shD struct{ mu *sync.Mutex }

func (s shD) M()     { s.mu.Lock(); s.mu.Unlock() }
func (s *shD) N()    { s.M() }
func callShD(s *shD) { s.mu.Lock(); s.N(); s.mu.Unlock() } // +checklocksfail=must not hold

// The lock is taken through a sync.Locker holding its address.
type shO struct{ mu sync.Mutex }

func (s *shO) M()    { var l sync.Locker = &s.mu; l.Lock(); l.Unlock() }
func callShO(s *shO) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// The receiver is stored twice, so it is spilled to a local with two stores.
type shJ struct{ mu sync.Mutex }

func (s *shJ) M()    { func() { *(&s) = s }(); s.mu.Lock(); s.mu.Unlock() }
func callShJ(s *shJ) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// A pointer receiver calls a value-receiver method that locks.
type shPV struct{ mu *sync.Mutex }

func (s shPV) lock()   { s.mu.Lock(); s.mu.Unlock() }
func (s *shPV) M()     { s.lock() }
func callShPV(s *shPV) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// A value receiver calls a pointer-receiver method that locks.
type shVP struct{ mu *sync.Mutex }

func (s *shVP) lock()  { s.mu.Lock(); s.mu.Unlock() }
func (s shVP) M()      { s.lock() }
func (s *shVP) N()     { s.M() }
func callShVP(s *shVP) { s.mu.Lock(); s.N(); s.mu.Unlock() } // +checklocksfail=must not hold

// The lock is taken on a copy of the receiver.
type shCY struct{ mu shMutex }

func (s *shCY) M()     { x := *s; x.mu.Lock(); x.mu.Unlock() }
func callShCY(s *shCY) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// An own method is called on a copy of the receiver.
type shCYO struct{ mu shMutex }

func (s *shCYO) lock()   { s.mu.Lock(); s.mu.Unlock() }
func (s *shCYO) M()      { x := *s; x.lock() }
func callShCYO(s *shCYO) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// An own method is called from an invoked closure.
type shPC struct{ mu sync.Mutex }

func (s *shPC) lock()  { s.mu.Lock(); s.mu.Unlock() }
func (s *shPC) M()     { func() { s.lock() }() }
func callShPC(s *shPC) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// The lock is taken two invoked closures deep.
type shD2 struct{ mu sync.Mutex }

func (s *shD2) M()     { func() { func() { s.mu.Lock(); s.mu.Unlock() }() }() }
func callShD2(s *shD2) { s.mu.Lock(); s.M(); s.mu.Unlock() } // +checklocksfail=must not hold

// The lock field is an interface.
type shLK struct{ l sync.Locker }

func (s *shLK) M()     { s.l.Lock(); s.l.Unlock() }
func callShLK(s *shLK) { s.l.Lock(); s.M(); s.l.Unlock() } // +checklocksfail=must not hold

// The receiver is passed to the closure as an argument, which is not a capture.
type shI struct{ mu sync.Mutex }

func (s *shI) M()    { func(x *shI) { x.mu.Lock(); x.mu.Unlock() }(s) }
func callShI(s *shI) { s.mu.Lock(); s.M(); s.mu.Unlock() }

// The lock field has a type parameter's type.
type shK[L sync.Locker] struct{ l L }

func (g *shK[L]) M()              { g.l.Lock(); g.l.Unlock() }
func callShK(g *shK[*sync.Mutex]) { g.l.Lock(); g.M(); g.l.Unlock() }

// The lock pointer is captured by an invoked closure.
type shCP struct{ mu sync.Mutex }

func (s *shCP) M()     { mu := &s.mu; func() { mu.Lock(); mu.Unlock() }() }
func callShCP(s *shCP) { s.mu.Lock(); s.M(); s.mu.Unlock() }

// The lock pointer is spilled by a capture and locked in the method.
type shSP struct{ mu sync.Mutex }

func (s *shSP) M()     { mu := &s.mu; f := func() { _ = mu }; f(); mu.Lock(); mu.Unlock() }
func callShSP(s *shSP) { s.mu.Lock(); s.M(); s.mu.Unlock() }

// The lock is nested, and the outer struct has a lock at the inner lock's field index.
type shGN struct {
	mu sync.Mutex
	in struct{ mu sync.Mutex }
}

func (s *shGN) M()     { s.in.mu.Lock(); s.in.mu.Unlock() }
func callShGN(s *shGN) { s.mu.Lock(); s.M(); s.mu.Unlock() }
