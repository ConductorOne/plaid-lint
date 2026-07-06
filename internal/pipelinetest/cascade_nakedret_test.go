// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeNakedretSyntaxOnly is the regression
// gate for the nakedret SyntaxOnly classification. nakedret's Run
// (nakedret.go L40-56) requires inspect.Analyzer (pure AST), reads
// pass.ResultOf[inspect.Analyzer], pass.Fset, and pass.Report. No
// pass.TypesInfo, pass.Pkg, or non-pure-AST ResultOf access.
func TestCascadeNakedretSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "nakedret", 0xd4, "cascade-nakedret-v1")
}
