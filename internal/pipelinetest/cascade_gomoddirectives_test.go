// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeGomoddirectivesSyntaxOnly is the regression
// gate for the gomoddirectives SyntaxOnly classification.
// gomoddirectives is special-shape: the wire wraps Run in sync.Once
// because go.mod is module-global, not per-package. AnalyzePass
// (gomoddirectives.go L69-92) reads pass.Module.Path (not
// pass.TypesInfo / pass.Pkg / pass.ResultOf), parses go.mod via
// gomod.GetModuleInfo, and reports against go.mod positions. A
// dep-internals edit cannot change go.mod content; diagnostic
// stream is invariant.
//
// The parameterized cascade helper still applies because the
// analyzer-instance + cache shape is per-package — each importer
// package gets its own L1 entry whose stored diagnostic set is
// driven by the sync.Once gate.
func TestCascadeGomoddirectivesSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "gomoddirectives", 0xd7, "cascade-gomoddirectives-v1")
}
