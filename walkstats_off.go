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

import (
	"go/token"

	"golang.org/x/tools/go/ssa"
)

type walkStats struct{}

type oracleRef struct{}

func (oracleRef) read(ssa.Value) {}

func (*passContext) oracle() oracleRef                                  { return oracleRef{} }
func (*passContext) walkChain(*ssa.Function) func()                     { return func() {} }
func (*passContext) cutCheck(*ssa.Function, *lockState, *lockState)     {}
func (*passContext) statWalk(*walkContext, *ssa.BasicBlock, *lockState) {}
func (*passContext) statWidened(*walkContext, *ssa.BasicBlock, int)     {}
func (*passContext) statTopSilenced(token.Pos, lockPath)                {}
func (*passContext) statOverflow()                                      {}
func (*passContext) writeWalkStats()                                    {}
