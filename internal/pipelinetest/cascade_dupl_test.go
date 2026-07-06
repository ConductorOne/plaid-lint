// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeDuplSyntaxOnly is the regression gate for
// the dupl SyntaxOnly classification. runDupl extracts on-disk paths
// from pass.Files (via pass.Fset.PositionFor) and delegates the
// suffix-tree search to duplAPI.Run([]string, threshold) — a function
// that takes filename strings and never sees pass.TypesInfo, pass.Pkg,
// or pass.ResultOf. The per-pass scope means each package
// builds its own suffix tree independently of importers.
//
// See wire_dupl_native.go for the analyzer body.
func TestCascadeDuplSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "dupl", 0xdd, "cascade-dupl-v1")
}
