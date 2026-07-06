// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers/tracecheck"
)

// wireAnalyzerFnsTracecheck attaches the AnalyzerFn for tracecheck —
// c1's custom analyzer that flags trace-span names which don't match
// the enclosing function name in snake_case.
//
// Upstream ships as a Go plugin (.so loaded via plugin.Open) consumed
// by golangci-lint at runtime. plaid-lint vendors the analyzer
// source under internal/analyzers/tracecheck/ and wires it as a
// native AnalyzerFn here — same pattern as the other custom-rule
// analyzers (depguard, exhaustive, forbidigo, gosec) in
// wire_analyzers_polya.go. Avoids the Go-plugin binary-compatibility
// fragility and lets tracecheck participate in plaid's analyzer
// graph / cache infrastructure like any other wired analyzer.
//
// No config schema needed — tracecheck has no user-configurable
// options (it embeds its naming convention in the analyzer itself),
// so the AnalyzerFn ignores its cfg parameter and returns the
// package-global Analyzer verbatim.
func wireAnalyzerFnsTracecheck(c *catalog) {
	wireNativeFn(c, "tracecheck", wireTracecheck)
}

// wireTracecheck returns the tracecheck.Analyzer. cfg is unused —
// tracecheck doesn't take settings from .golangci.yml's
// linters-settings block.
func wireTracecheck(_ any) []*analysis.Analyzer {
	return []*analysis.Analyzer{tracecheck.Analyzer}
}
