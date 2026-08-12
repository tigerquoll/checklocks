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

// Package lockstringer is the corpus for the lockstringer analyzer.
//
// It is analyzed with the checklocks analyzer disabled. The unguarded reads
// below are violations in checklocks' own terms as well, and an expectation
// can only be stated once per line, so each analyzer has its own corpus and
// each is run over it alone.
package lockstringer

import (
	"fmt"
	"sync"
	"time"
)

// guardedRead is the F19 shape: a Stringer reading guarded state with no lock.
type guardedRead struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
	name  string
}

func (g *guardedRead) String() string {
	return fmt.Sprintf("%s=%d", g.name, g.count) // +lockstringerfail=guarded read races
}

// selfLocking is the recursive acquisition shape: the Stringer takes the
// receiver's own lock, so formatting it while holding that lock deadlocks.
type selfLocking struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

func (s *selfLocking) String() string {
	s.mu.Lock() // +lockstringerfail=self-deadlock
	defer s.mu.Unlock()
	return fmt.Sprintf("%d", s.count)
}

// viaAccessor is the Application.String shape: the Stringer itself takes no
// lock, but calls a self-locking accessor of the same type.
type viaAccessor struct {
	mu sync.RWMutex
	// +checklocks:mu
	submitted time.Time
	id        string
}

// getSubmitted is a self-locking accessor.
func (v *viaAccessor) getSubmitted() time.Time {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.submitted
}

func (v *viaAccessor) String() string {
	return fmt.Sprintf("%s@%v", v.id, v.getSubmitted()) // +lockstringerfail=getSubmitted
}

// clean only reads fields fixed at construction, which is the recommended
// shape, and must not be reported.
type clean struct {
	mu sync.Mutex
	// +checklocks:mu
	mutable int
	id      string
	created time.Time
}

func (c *clean) String() string {
	return fmt.Sprintf("%s@%v", c.id, c.created)
}

// nonStringer has the same guarded read as guardedRead, but in an ordinary
// method. That is the checklocks analyzer's business, not this one's, so it
// must not be reported here.
type nonStringer struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

func (n *nonStringer) Describe() string {
	return fmt.Sprintf("%d", n.count)
}

// otherLazy covers the remaining lazily evaluated methods.
type otherLazy struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

func (o *otherLazy) Error() string {
	return fmt.Sprintf("%d", o.count) // +lockstringerfail=guarded read races
}

func (o *otherLazy) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%d", o.count)), nil // +lockstringerfail=guarded read races
}

// unguardedType has a lock but no guarded field, so it has taken no position
// on what the lock protects and is out of scope.
type unguardedType struct {
	mu    sync.Mutex
	count int
}

func (u *unguardedType) String() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return fmt.Sprintf("%d", u.count)
}

// noLockType is guarded by a global lock and has no lock of its own. The
// analysis is scoped to types that carry their own lock, so this is not
// reported even though the read is unguarded; checklocks reports it.
var globalMu sync.Mutex

type noLockType struct {
	// +checklocks:globalMu
	count int
}

func (n *noLockType) String() string {
	return fmt.Sprintf("%d", n.count)
}

// ignoredFunc carries a function level ignore.
type ignoredFunc struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

// +lockstringerignore
func (i *ignoredFunc) String() string {
	return fmt.Sprintf("%d", i.count)
}

// ignoredLine carries a line level ignore.
type ignoredLine struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

func (i *ignoredLine) String() string {
	return fmt.Sprintf("%d", i.count) // +lockstringerignore
}

// notLazy is named String but is not a fmt.Stringer, so it is out of scope.
type notLazy struct {
	mu sync.Mutex
	// +checklocks:mu
	count int
}

func (n *notLazy) String(verbose bool) string {
	return fmt.Sprintf("%v %d", verbose, n.count)
}
