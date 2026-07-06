// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeBidichkSyntaxOnly is the regression gate
// for the bidichk SyntaxOnly classification. bidichk's Run
// (github.com/breml/bidichk@v0.3.3/pkg/bidichk/bidichk.go) iterates
// pass.Files, reads each file via pass.ReadFile, and byte-scans for
// dangerous bidirectional unicode codepoints via bytes.IndexRune.
// Diagnostics emit via pass.Reportf. No pass.TypesInfo, pass.Pkg, or
// pass.ResultOf.
func TestCascadeBidichkSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "bidichk", 0xb1, "cascade-bidichk-v1")
}
