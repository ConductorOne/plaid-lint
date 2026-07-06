// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeLllSyntaxOnly is the regression gate for
// the lll SyntaxOnly classification. runLll reads pass.Files and
// pass.Fset, opens each .go file from disk via os.Open, and reports
// lines whose rune count exceeds the configured maximum. The disk
// read is the same source the workspace already parsed — still
// package-local. No pass.TypesInfo, pass.Pkg, pass.ResultOf access.
//
// See wire_lll_native.go for the analyzer body.
func TestCascadeLllSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "lll", 0x11, "cascade-lll-v1")
}
