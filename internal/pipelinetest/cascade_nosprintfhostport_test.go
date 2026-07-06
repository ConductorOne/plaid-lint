// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeNosprintfhostportSyntaxOnly is the regression
// gate for the nosprintfhostport SyntaxOnly classification. Run
// (pkg/analyzer/analyzer.go L20-) requires inspect.Analyzer (pure
// AST) and reads pass.ResultOf[inspect.Analyzer] (L24) plus
// pass.Reportf (L33). Pure AST walk + format-string parsing. No
// pass.TypesInfo, pass.Pkg, or non-pure-AST ResultOf access.
//
// Wire uses the upstream package-global Analyzer pointer (single-
// pointer-per-process); the RegisterSyntaxOnly idempotency note
// matches predeclared and decorder.
func TestCascadeNosprintfhostportSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "nosprintfhostport", 0xd6, "cascade-nosprintfhostport-v1")
}
