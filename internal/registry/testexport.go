// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import "golang.org/x/tools/go/analysis"

// AnalyzerFnForTest returns the registered AnalyzerFn for the named
// linter in the package-global catalog, or nil if no such entry
// exists or the entry has no factory wired. Used by cross-package
// tests (e.g. internal/pipelinetest) that need to build a fresh
// analyzer instance via the same factory production uses, without
// taking a circular dep on registry's full Build() machinery.
//
// The factory call (`fn(cfg)`) also triggers any descriptor
// registration the wire layer performs (e.g. RegisterSyntaxOnly), so
// callers that consume the analyzer through BundledRegistry will see
// the descriptor present after AnalyzerFnForTest returns.
//
// Naming: the "ForTest" suffix marks this as a test-only seam — it
// is not part of the registry's public contract.
func AnalyzerFnForTest(name string) func(cfg any) []*analysis.Analyzer {
	e, ok := defaultCatalog.resolve(name)
	if !ok {
		return nil
	}
	return e.AnalyzerFn
}
