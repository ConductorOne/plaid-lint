// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeDepguardSyntaxOnly is the regression gate
// for the depguard SyntaxOnly classification. depguard's Run
// (depguard.go L68-90) ranges pass.Files, reads
// pass.Fset.Position().Filename, and walks file.Imports for raw
// BasicLit.Value matching against compiled linterSettings. No
// pass.TypesInfo, pass.Pkg, or pass.ResultOf access.
func TestCascadeDepguardSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "depguard", 0xd1, "cascade-depguard-v1")
}
