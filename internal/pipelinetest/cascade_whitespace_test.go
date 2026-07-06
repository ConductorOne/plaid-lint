// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeWhitespaceSyntaxOnly is the regression gate
// for the whitespace SyntaxOnly classification. Whitespace's Run
// reads only pass.Files / pass.Fset / pass.Report (see
// internal/registry/wire_whitespace_native.go runWhitespace +
// wsVisitor). The non-exported-dep-edit fixture asserts:
//
//  1. CORRECTNESS — importer diagnostic stream is invariant.
//  2. WIN — 5/5 importers hit L1; the edited app/ correctly misses.
//
// Shape and assertions are the runCascadeSyntaxOnlyDepInternalsEdit
// helper's contract.
func TestCascadeWhitespaceSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "whitespace", 0x77, "cascade-whitespace-v1")
}
