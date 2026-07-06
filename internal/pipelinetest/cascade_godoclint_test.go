// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGodoclintSyntaxOnly is the regression gate
// for the godoclint SyntaxOnly classification. godoclint's Run
// (analysis/analyzer.go) reads pass.Files / pass.Fset /
// pass.ReadFile / pass.ResultOf[<own internal inspector>]; the
// internal inspector walks file.Comments and AST nodes only. No
// pass.TypesInfo, pass.Pkg, types.Object, or types.Type reads in
// pkg/check/*/*.go.
//
// See wire_analyzers_wrapbatch.go for the analyzer wire.
func TestCascadeGodoclintSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "godoclint", 0xd1, "cascade-godoclint-v1")
}
