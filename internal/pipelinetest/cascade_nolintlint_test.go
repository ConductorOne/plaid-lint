// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeNolintlintSyntaxOnly is the regression
// gate for the nolintlint SyntaxOnly classification. The in-tree
// analyzer's Run (wire_analyzers_nolintlint.go L75-84) ranges
// pass.Files → file.Comments → cg.List → checkNolintComment;
// checkNolintComment reads only c.Text and pass.Report. No
// pass.TypesInfo, pass.Pkg, or pass.ResultOf access.
func TestCascadeNolintlintSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "nolintlint", 0xd3, "cascade-nolintlint-v1")
}
