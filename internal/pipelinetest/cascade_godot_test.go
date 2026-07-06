// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGodotSyntaxOnly is the regression gate for
// the godot SyntaxOnly classification. The wire's Run loops over
// pass.Files and delegates to godotpass.Run(file, fset, settings)
// — a function whose signature precludes any pass.TypesInfo /
// pass.Pkg / Requires access by construction. Diagnostics are
// emitted via pass.Report against pass.Fset positions.
//
// See wire_analyzers_wrapbatch.go (godot stanza) and
// github.com/tetafro/godot@v1.5.6's func Run signature.
func TestCascadeGodotSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "godot", 0x60, "cascade-godot-v1")
}
