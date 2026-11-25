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

//go:build !checklocks_parity

package checklocks

import "golang.org/x/tools/go/ssa"

type lockOrderShadow struct{}

func (*lockOrderContext) shadowCall(ssa.CallInstruction, siteLocks, bool) {}
func (*lockOrderContext) shadowCompare()                                  {}
func (*lockOrderContext) shadowFlush(bool)                                {}

func (*lockOrderContext) shadowRelease(*ssa.Function, ssa.CallInstruction, ssa.Value, string, siteLocks, bool) {
}
func (*lockOrderContext) shadowAcquire(siteLocks, string)                                 {}
func (*lockOrderContext) shadowGains(*ssa.Function, ssa.CallInstruction, siteLocks, bool) {}
func (*lockOrderContext) shadowReleased(*ssa.Function, ssa.Instruction, siteLocks, bool, string) {
}
