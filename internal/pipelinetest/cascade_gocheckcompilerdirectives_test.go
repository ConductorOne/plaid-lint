// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGocheckcompilerdirectivesSyntaxOnly is the
// regression gate for the gocheckcompilerdirectives SyntaxOnly
// classification. Its Run
// (4d63.com/gocheckcompilerdirectives@v1.4.0/checkcompilerdirectives/checkcompilerdirectives.go)
// iterates pass.Files, walks each file.Comments group, string-checks
// comment text for `//go:` directives against a fixed table of known
// directives, and reports via pass.ReportRangef. No pass.TypesInfo,
// pass.Pkg, or pass.ResultOf reads.
func TestCascadeGocheckcompilerdirectivesSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "gocheckcompilerdirectives", 0xc1, "cascade-gocheckcompilerdirectives-v1")
}
