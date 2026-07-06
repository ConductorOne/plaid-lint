// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeFuncorderSyntaxOnly is the regression gate
// for the funcorder SyntaxOnly classification. funcorder's Run reads
// only pass.ResultOf[inspect.Analyzer] (purely AST inspector) and
// walks *ast.File / *ast.FuncDecl / *ast.TypeSpec; the internal/
// subpackage walks AST shapes and reports via pass.Report. No
// pass.TypesInfo, pass.Pkg, or pass.ResultOf of a type-providing
// prerequisite.
//
// See wire_analyzers_batch4.go for the analyzer wire.
func TestCascadeFuncorderSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "funcorder", 0xf0, "cascade-funcorder-v1")
}
