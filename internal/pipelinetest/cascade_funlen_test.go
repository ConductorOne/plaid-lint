// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeFunlenSyntaxOnly is the regression gate
// for the funlen SyntaxOnly classification. funlen's Run reads only
// pass.Files / pass.Fset, builds ast.NewCommentMap, ranges over
// file.Decls for *ast.FuncDecl, counts statements via ast.Inspect,
// and reports via pass.Reportf. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf.
//
// See wire_analyzers_batch4.go for the analyzer wire.
func TestCascadeFunlenSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "funlen", 0xfa, "cascade-funlen-v1")
}
