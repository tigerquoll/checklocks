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

package lockblocking

func rangeSeq(yield func(int) bool) { yield(1) }

// A send in a range-over-func body waits while the loop's lock is held.
func sendInRangeBody(a *App, out chan int) {
	a.Lock()
	for v := range rangeSeq {
		out <- v // +lockblockingfail=a channel send
	}
	a.Unlock()
}
