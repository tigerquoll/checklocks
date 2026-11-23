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
	"go/types"

	"golang.org/x/tools/go/ssa"
)

func parityCheck(*resolvedValue, *lockState, lockPath, types.Object) {}

func parityCheckEntry(pathKey, lockInfo) {}

const replayRuns = 1

const markerBoundOn = true

type blockParity struct{}

func (blockParity) entered(*passContext, *ssa.Function, *lockState, string) {}
func (blockParity) walked(*passContext, *ssa.Function, *lockState)          {}
func (blockParity) replayed(*passContext, *blockRecord, *lockState, bool)   {}
func (*passContext) parityFinding(token.Pos, string, ...any)                {}
func (*passContext) parityObservation(almostInst, types.Object, *lockState) {}

type decisionState struct{}

func (*decisionState) enter() {}
func (*decisionState) leave() {}

func (*passContext) decide(decisionAt, decisionKind, bool, ...any) {}
func (*passContext) suppressed()                                   {}
func (*passContext) decisionMark() int                             { return 0 }
func (*passContext) withdrawDecisions(int)                         {}
func (*passContext) writeDecisions()                               {}

func inlineContext(*walkContext, callSite, *ssa.Function) string { return "" }

func qualifiedName(ssa.Value, types.Object) string { return "" }

// recoverPanics contains a panic to the function being analyzed.
var recoverPanics = true

// succOrder is the order the walk visits a block's successors in.
func succOrder(_ *ssa.Function, block *ssa.BasicBlock) []*ssa.BasicBlock { return block.Succs }

// workRank orders the worklist: reverse-postorder number first, oldest first.
func workRank(_ *ssa.Function, rpo, seq int) [2]int { return [2]int{rpo, seq} }
