// Copyright 2020 The gVisor Authors.
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

// Binary checklocks is a `vettool` for `go vet`.
//
// It runs every analyzer in this module. Each may be disabled by name, for
// example -lockstringer=false, and each carries its own flags under its own
// name, for example -checklocks.inferred=false.
package main

import (
	"github.com/tigerquoll/checklocks"
	"golang.org/x/tools/go/analysis/multichecker"
)

func main() {
	multichecker.Main(
		checklocks.Analyzer,
		checklocks.LockStringerAnalyzer,
		checklocks.LockOrderAnalyzer,
	)
}
