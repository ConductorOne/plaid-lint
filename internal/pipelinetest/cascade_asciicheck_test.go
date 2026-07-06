// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import "testing"

// TestCascadeAsciicheckSyntaxOnly is the regression gate
// for the asciicheck SyntaxOnly classification. asciicheck's Run
// (github.com/tdakkota/asciicheck@v0.4.1/asciicheck.go) reads only
// pass.ResultOf[inspect.Analyzer] — a *inspector.Inspector built by
// `inspector.New(pass.Files)`, package-local syntax only — and walks
// AST nodes for non-ASCII identifier characters. No pass.TypesInfo,
// pass.Pkg, or pass.ResultOf of a type-providing prerequisite. The
// inspect prerequisite runs in the prereq-bypass path (its Result
// is non-cacheable), unaffected by the SyntaxOnly classification on
// asciicheck itself.
func TestCascadeAsciicheckSyntaxOnly(t *testing.T) {
	runCascadeSyntaxOnlyDepInternalsEdit(t, "asciicheck", 0xa5, "cascade-asciicheck-v1")
}
