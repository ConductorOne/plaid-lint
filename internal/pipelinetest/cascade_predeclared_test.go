// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadePredeclaredSyntaxOnly is the regression
// gate for the predeclared SyntaxOnly classification. predeclared's
// Run (predeclared.go L37-43) ranges pass.Files and calls
// processFile(pass.Report, cfg, pass.Fset, file); processFile uses
// pure ast.Inspect + identifier .Name comparisons against the
// isPredeclaredIdent table. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf access.
func TestCascadePredeclaredSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "predeclared", 0xd2, "cascade-predeclared-v1")
}
