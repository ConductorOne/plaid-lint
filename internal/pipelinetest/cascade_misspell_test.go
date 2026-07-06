// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeMisspellSyntaxOnly is the regression gate
// for the misspell SyntaxOnly classification. The wire's Run iterates
// pass.Files, calls pass.ReadFile to fetch each file's bytes, and
// runs the misspellpass.Replacer (a string-only dictionary matcher
// built at wire-time) against the contents. Diagnostics are reported
// via pass.Report against pass.Fset positions. No pass.TypesInfo,
// pass.Pkg, or pass.ResultOf reads.
//
// See wire_analyzers_wrapbatch.go (misspell stanza).
func TestCascadeMisspellSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "misspell", 0x52, "cascade-misspell-v1")
}
