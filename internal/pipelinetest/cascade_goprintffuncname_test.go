// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGoprintffuncnameSyntaxOnly is the
// regression gate for the goprintffuncname SyntaxOnly classification.
// goprintffuncname's Run reads only
// pass.ResultOf[inspect.Analyzer] (purely AST inspector) and walks
// *ast.FuncDecl, doing pure AST-shape checks of parameter types and
// name-based identifier matching. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf of a type-providing prerequisite.
//
// See wire_analyzers_batch1.go for the analyzer wire.
func TestCascadeGoprintffuncnameSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "goprintffuncname", 0xb1, "cascade-goprintffuncname-v1")
}
