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

// Package dep stands for another package of the same code base. It declares a type and a
// method whose names are the corpus's own, so that two callees print alike.
package dep

// Queue is named as the corpus's own type is.
type Queue struct{}

// Drain waits for the channel.
func (Queue) Drain(c chan int) { <-c }
