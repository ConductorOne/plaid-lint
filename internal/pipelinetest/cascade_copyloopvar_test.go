// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeCopyloopvarSyntaxOnly is the regression
// gate for the copyloopvar SyntaxOnly classification. copyloopvar's
// Run reads pass.ResultOf[inspect.Analyzer] (a purely AST inspector,
// not type-providing) and inspects *ast.RangeStmt / *ast.ForStmt
// nodes. All identifier comparisons are name-based on *ast.Ident.Name
// — no .Obj, no types.Object. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf of a type-providing prerequisite.
//
// See wire_analyzers_batch3.go for the analyzer wire.
func TestCascadeCopyloopvarSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "copyloopvar", 0xc1, "cascade-copyloopvar-v1")
}
