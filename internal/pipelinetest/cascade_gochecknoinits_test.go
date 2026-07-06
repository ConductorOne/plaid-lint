// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGochecknoinitsSyntaxOnly is the regression
// gate for the gochecknoinits SyntaxOnly classification.
// runGochecknoinits reads only pass.Files (range f.Decls for
// *ast.FuncDecl named "init" with no receiver) and calls pass.Reportf.
// No pass.TypesInfo, pass.Pkg, or pass.ResultOf access.
//
// See wire_gochecknoinits_native.go for the analyzer body.
func TestCascadeGochecknoinitsSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "gochecknoinits", 0x91, "cascade-gochecknoinits-v1")
}
