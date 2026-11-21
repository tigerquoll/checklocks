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

package lockblockinggroup

import (
	"time"

	"github.com/tigerquoll/checklocks/test/lockblockinggroup/dep"
)

// Queue is named as the dependency's type is, so that the two callees print alike.
type Queue struct{}

func (Queue) Drain(c chan int) { time.Sleep(time.Millisecond) }

// Two callees that print alike are two groups: the key is the function called, not its name.
func twoPackagesOfOneName(g *Grouped, c chan int) {
	g.Lock()
	Queue{}.Drain(c)     // +lockblockingfail=calling (Queue).Drain, which may block (calling Sleep at lockblockinggroup/samename.go:26) while holding Grouped
	dep.Queue{}.Drain(c) // +lockblockingfail=calling (Queue).Drain, which may block (a channel receive at dep/dep.go:23) while holding Grouped
	g.Unlock()
}
