// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGocycloSyntaxOnly is the regression gate
// for the gocyclo SyntaxOnly classification. runGocyclo reads only
// pass.Files / pass.Fset, hands each *ast.File to
// gocyclolib.AnalyzeASTFile (purely AST cyclomatic-complexity walk),
// and reports via pass.Report. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf.
//
// See wire_gocyclo_native.go for the analyzer body.
func TestCascadeGocycloSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "gocyclo", 0x9c, "cascade-gocyclo-v1")
}
