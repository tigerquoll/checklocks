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

// Binary lockorder is a `vettool` for `go vet`.
//
// It exists so the analyzer can be exercised on its own while the multi analyzer binary is
// being built; once that lands this folds into it.
package main

import (
	"github.com/tigerquoll/checklocks/lockorder"
	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() { singlechecker.Main(lockorder.Analyzer) }
