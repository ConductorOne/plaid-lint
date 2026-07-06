// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeDupwordSyntaxOnly is the regression gate
// for the dupword SyntaxOnly classification. dupword's Run reads
// pass.Files / pass.Fset / pass.ResultOf[inspect.Analyzer] (purely
// AST inspector) and scans file.Comments + string literals for
// consecutive duplicate words. Reports via pass.Report. No
// pass.TypesInfo, pass.Pkg, or pass.ResultOf of a type-providing
// prerequisite.
//
// See wire_analyzers_batch4.go for the analyzer wire.
func TestCascadeDupwordSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "dupword", 0xdb, "cascade-dupword-v1")
}
