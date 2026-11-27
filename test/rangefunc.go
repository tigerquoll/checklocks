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

import "slices"

func rangeSeq(yield func(int) bool) { yield(1) }

func rangeGeneric[T any](yield func(T) bool) {}

type rangeSource struct{}

func (*rangeSource) All(yield func(int) bool) { yield(1) }

// A lock held around the loop covers the body.
func testRangeFuncBodyHeld(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		tc.guardedField++
	}
	tc.mu.Unlock()
}

// Each iterator shape reaches the body with the loop's locks.
func testRangeFuncShapes(tc *oneGuardStruct, seq func(func(int) bool), src *rangeSource, xs []int) {
	tc.mu.Lock()
	for range seq {
		tc.guardedField++
	}
	for range slices.Values(xs) {
		tc.guardedField++
	}
	for range src.All {
		tc.guardedField++
	}
	for range rangeGeneric[int] {
		tc.guardedField++
	}
	for range func(yield func(int) bool) { yield(1) } {
		tc.guardedField++
	}
	tc.mu.Unlock()
}

// A lock taken and released inside the body is its own.
func testRangeFuncBodyOwnLock(tc *oneGuardStruct) {
	for range rangeSeq {
		tc.mu.Lock()
		tc.guardedField++
		tc.mu.Unlock()
	}
}

// The body releases the loop's lock and takes it back.
func testRangeFuncBodyRelock(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		tc.mu.Unlock() // +lockgapfail=released here and taken again
		tc.mu.Lock()
	}
	tc.mu.Unlock()
}

// An iteration that ends with the loop's lock released is reported; the caller keeps its lock.
func testRangeFuncReleaseOnContinue(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		tc.mu.Unlock()
	} // +checklocksfail=range loop body ends an iteration without
	tc.mu.Unlock()
}

// Every lock released is reported.
func testRangeFuncReleaseTwo(tc *twoLocksStruct) {
	tc.mu.Lock()
	tc.secondMu.Lock()
	for range rangeSeq {
		tc.mu.Unlock()
		tc.secondMu.Unlock()
	} // +checklocksfail=range loop body ends an iteration without|range loop body ends an iteration without
	tc.secondMu.Unlock()
	tc.mu.Unlock()
}

// A break that holds a lock taken in the body reports at the break, and the exit is no return state.
func testRangeFuncBreakHolding(tc *oneGuardStruct, c bool) {
	for range rangeSeq {
		if c {
			tc.mu.Lock()
			break // +checklocksfail=return with unexpected locks held
		}
	}
}

// The body may run no times, so what it takes is not held after the loop.
func testRangeFuncZeroIterations(tc *oneGuardStruct) {
	for range rangeSeq {
		tc.mu.Lock()
		break // +checklocksfail=return with unexpected locks held
	}
	tc.guardedField++ // +checklocksfail
	tc.mu.Unlock()    // +checklocksfail=already unlocked
}

// A return that releases the loop's lock is not an iteration end.
func testRangeFuncUnlockReturn(tc *oneGuardStruct, c bool) int { // +checklocksfail=incompatible return states
	tc.mu.Lock()
	for range rangeSeq {
		if c {
			tc.mu.Unlock()
			return 1 // +checklocksfail=return with unexpected locks held
		}
	}
	tc.mu.Unlock()
	return 0
}

// A lock taken and deferred in the body is not followed past the return.
func testRangeFuncDeferReturn(tc *oneGuardStruct) int {
	for range rangeSeq {
		tc.mu.Lock()
		defer tc.mu.Unlock()
		return tc.guardedField // +checklocksfail=return with unexpected locks held
	}
	return 0
}

// A loop in a loop and in a called literal reach the lock; a literal in a body is one hop too far.
func testRangeFuncNested(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		for range rangeSeq {
			tc.guardedField++
		}
	}
	func() {
		for range rangeSeq {
			tc.guardedField++
		}
	}()
	for range rangeSeq {
		func() {
			tc.guardedField++ // +checklocksfail
		}()
	}
	tc.mu.Unlock()
}

func rangeRun(f func()) { f() }

// A loop in a closure passed on is checked with no locks, as the closure is.
func testRangeFuncPassedClosure(tc *oneGuardStruct) {
	tc.mu.Lock()
	rangeRun(func() {
		for range rangeSeq {
			tc.guardedField++ // +checklocksfail
		}
	})
	tc.mu.Unlock()
}

// An annotated parent's lock covers the body.
// +checklocks:tc.mu
func testRangeFuncAnnotated(tc *oneGuardStruct) {
	for range rangeSeq {
		tc.guardedField++
	}
}

// An ignored parent leaves its body unchecked, releases included.
// +checklocksignore
func testRangeFuncIgnored(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		tc.guardedField++
		tc.mu.Unlock()
	}
	tc.mu.Unlock()
}

// The loop is reached with the lock held and without it; only the second state reports.
func testRangeFuncTwoStates(tc *oneGuardStruct, c bool) { // +checklocksfail=incompatible return states
	tc.mu.Lock()
	if c {
		tc.mu.Unlock()
	}
	for range rangeSeq {
		tc.guardedField++ // +checklocksfail
	}
} // +checklocksfail=return with unexpected locks held

// The loop is reached in each state of a plain loop.
func testRangeFuncInLoop(tc *oneGuardStruct, n int) {
	tc.mu.Lock()
	for i := 0; i < n; i++ {
		for range rangeSeq {
			tc.guardedField++
		}
	}
	tc.mu.Unlock()
}

// A defer pending at the loop is not the body's.
func testRangeFuncDeferPending(tc *oneGuardStruct) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	for range rangeSeq {
		tc.guardedField++
	}
}

// A loop, a called literal and a loop on its parameter.
func testRangeFuncLiteralParam(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		func(s *oneGuardStruct) {
			for range rangeSeq {
				s.guardedField++
			}
		}(tc)
	}
	tc.mu.Unlock()
}

// A loop in a loop over a local struct.
func testRangeFuncNestedLocal() {
	var tc oneGuardStruct
	tc.mu.Lock()
	for range rangeSeq {
		for range rangeSeq {
			tc.guardedField++
		}
	}
	tc.mu.Unlock()
}

// Two loops, a called literal and a loop on its parameter.
func testRangeFuncNestedLiteralParam(tc *oneGuardStruct) {
	tc.mu.Lock()
	for range rangeSeq {
		for range rangeSeq {
			func(s *oneGuardStruct) {
				for range rangeSeq {
					s.guardedField++
				}
			}(tc)
		}
	}
	tc.mu.Unlock()
}

// A continue path that releases the loop's lock is reported once, at the body's end.
func testRangeFuncContinueRelease(tc *oneGuardStruct, c bool) {
	tc.mu.Lock()
	for range rangeSeq {
		if c {
			tc.mu.Unlock()
			continue
		}
		tc.guardedField++
	} // +checklocksfail=range loop body ends an iteration without
	tc.mu.Unlock()
}

// A continue path that takes another lock is reported as a return with locks held, not as incompatible states.
func testRangeFuncContinueAcquire(tc *twoLocksStruct, c bool) {
	tc.mu.Lock()
	for range rangeSeq {
		if c {
			tc.secondMu.Lock()
			continue
		}
	} // +checklocksfail=return with unexpected locks held
	tc.mu.Unlock()
}

// Known false negative: the lock released before a break still counts as held after the loop.
func testRangeFuncReleaseBreak(tc *oneGuardStruct, c bool) {
	tc.mu.Lock()
	for v := range rangeSeq {
		if c {
			tc.mu.Unlock()
			break
		}
		tc.guardedField += v
	}
	tc.mu.Unlock()
}
