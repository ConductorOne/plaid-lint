// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGodoxSyntaxOnly is the regression gate
// for the godox SyntaxOnly classification. runGodox reads only
// pass.Files / pass.Fset and hands each *ast.File to
// godoxlib.Run(file, fset, keywords...) — a per-file comment scanner
// looking for TODO/BUG/FIXME-style keywords. Reports via pass.Report.
// No pass.TypesInfo, pass.Pkg, or pass.ResultOf.
//
// See wire_godox_native.go for the analyzer body.
func TestCascadeGodoxSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "godox", 0x60, "cascade-godox-v1")
}
