// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeDogsledSyntaxOnly is the regression gate
// for the dogsled SyntaxOnly classification. runDogsled walks
// pass.Files via ast.Inspect for *ast.AssignStmt, counts blank-
// identifier LHS, and calls pass.Reportf. No pass.TypesInfo,
// pass.Pkg, or pass.ResultOf.
//
// See wire_dogsled_native.go for the analyzer body.
func TestCascadeDogsledSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "dogsled", 0xd0, "cascade-dogsled-v1")
}
