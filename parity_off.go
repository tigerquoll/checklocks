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

type blockParity struct{}

func (blockParity) walked(*lockState)                                       {}
func (blockParity) replayed(*blockRecord, *lockState, bool)                 {}
func (*passContext) parityFinding(token.Pos, string, ...any)                {}
func (*passContext) parityObservation(almostInst, types.Object, *lockState) {}

type decisionState struct{}

func (*decisionState) enter() {}
func (*decisionState) leave() {}

func (*passContext) decide(decisionAt, decisionKind, bool, ...string) {}
func (*passContext) writeDecisions()                                  {}

func inlineContext(*walkContext, callSite, *ssa.Function) string { return "" }
