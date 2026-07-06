// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeDecorderSyntaxOnly is the regression gate
// for the decorder SyntaxOnly classification. decorder's Run reads
// only pass.Files (Decl-walk over each file's top-level declarations
// for token kind / order / init-first rules) and calls pass.Reportf.
// No pass.TypesInfo, pass.Pkg, or pass.ResultOf.
//
// See wire_analyzers_batch5.go for the analyzer wire.
func TestCascadeDecorderSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "decorder", 0xde, "cascade-decorder-v1")
}
