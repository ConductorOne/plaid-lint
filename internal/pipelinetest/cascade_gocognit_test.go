// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGocognitSyntaxOnly is the regression gate
// for the gocognit SyntaxOnly classification. gocognit's Run reads
// only pass.ResultOf[inspect.Analyzer] (purely AST inspector) and
// computes cognitive complexity from AST shape, visiting
// *ast.FuncDecl, *ast.BranchStmt, *ast.BinaryExpr, *ast.IfStmt, etc.
// The one *ast.Ident.Obj read is the parser's intra-file resolver bag
// (go/ast.Object) used for direct-recursion detection within a single
// file; not types.Object and does not cross package boundaries.
// Reports via pass.Reportf. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf of a type-providing prerequisite.
//
// See wire_analyzers_batch3.go for the analyzer wire.
func TestCascadeGocognitSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "gocognit", 0x90, "cascade-gocognit-v1")
}
