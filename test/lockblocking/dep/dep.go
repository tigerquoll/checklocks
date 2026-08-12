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

// Package dep stands for another package of the same code base, reached from the
// lockblocking corpus. It exists to pin what crosses a package boundary: a wait this
// analysis can NAME does, and it does so in every mode the analyser can be driven in.
package dep

// Resolve stands for a wait that cannot be seen: the work happens behind an indirect call,
// so the declaration is what says it waits.
//
// +blocking
func Resolve(name string) string {
	return name
}

// Lookup reaches the declared wait without declaring anything itself.
//
// This is the shape that matters. A caller of this is one call and one package away from the
// declaration, which is where the boundary used to be: the declaration was consulted for the
// callee itself and nothing carried it one step further.
func Lookup(name string) string {
	return Resolve(name)
}

// Sleeper reaches a wait on the built-in list, which is named in the same sense as the
// annotation: it is a statement about the callee rather than something inferred about it.
func Sleeper(c chan int) {
	select {
	case <-c:
	default:
	}
}

// Drain waits on a channel, which this analysis infers rather than names.
//
// It is deliberately not called under a lock in the corpus. Whether an inferred wait crosses
// a package boundary depends on how the analyser was driven: over packages, the module of
// the caller is known and it does; over a file list, there is no module to compare against
// and it does not. A corpus case would have to expect two different things.
func Drain(c chan int) {
	<-c
}
