// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGoheaderSyntaxOnly is the regression gate
// for the goheader SyntaxOnly classification. goheader's Run reads
// only pass.Files / pass.Fset; fans out files to a worker pool, each
// worker resolves the filename via pass.Fset.PositionFor +
// pass.Fset.File, parses the file's leading comment block against
// the configured template, and reports via pass.Report. No
// pass.TypesInfo, pass.Pkg, or pass.ResultOf.
//
// See wire_analyzers_batch6.go for the analyzer wire.
func TestCascadeGoheaderSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "goheader", 0xa1, "cascade-goheader-v1")
}
