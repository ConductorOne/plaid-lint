// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeUsestdlibvarsSyntaxOnly is the regression
// gate for the usestdlibvars SyntaxOnly classification. Run
// (pkg/analyzer/analyzer.go L40-) requires inspect.Analyzer (pure
// AST) and reads pass.ResultOf[inspect.Analyzer] (L63) plus
// pass.Report. Walks calls and literals matching against fixed
// stdlib-constant tables (HTTPMethod, HTTPStatusCode, etc.). No
// pass.TypesInfo, pass.Pkg, or non-pure-AST ResultOf access.
func TestCascadeUsestdlibvarsSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "usestdlibvars", 0xd5, "cascade-usestdlibvars-v1")
}
