// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/token"

	"golang.org/x/tools/go/analysis"

	unconvertlib "github.com/golangci/unconvert"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsUnconvertNative attaches the in-process `unconvert`
// Analyzer. The check is types-aware: unconvert.Run consults
// pass.TypesInfo to identify redundant `T(x)` conversions where the
// argument is already of type T. Upstream library is
// `github.com/golangci/unconvert`, the Go Authors' fork that adds a
// `Run(*analysis.Pass) []token.Position` entry point.
//
// Settings: FastMath and Safe are package-globals on the upstream
// library — set via SetFastMath/SetSafe before the Analyzer runs.
// The analysis framework runs every Run() once per package; setting
// the globals at AnalyzerFn build time is correct as long as no
// other unconvert Analyzer pointer with different settings co-exists
// in a single registry Build (Build is one-shot per process).
//
// Message format: `unnecessary conversion` — matches the subproc
// wrapper's emission verbatim.
func wireAnalyzerFnsUnconvertNative(c *catalog) {
	wireNativeFn(c, "unconvert", func(cfg any) []*analysis.Analyzer {
		fastMath := false
		safe := false
		if s, ok := cfg.(*config.UnconvertSettings); ok && s != nil {
			fastMath = s.FastMath
			safe = s.Safe
		}
		// Upstream documents these setters as "should not be called
		// during Run" — at Analyzer-build time we're before any pass
		// dispatch, so this is safe.
		unconvertlib.SetFastMath(fastMath)
		unconvertlib.SetSafe(safe)
		return []*analysis.Analyzer{unconvertAnalyzer()}
	})
}

func unconvertAnalyzer() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "unconvert",
		Doc:  "Reports unnecessary type conversions.",
		Run:  runUnconvert,
	}
}

func runUnconvert(pass *analysis.Pass) (any, error) {
	positions := unconvertlib.Run(pass)
	for _, p := range positions {
		pass.Report(analysis.Diagnostic{
			Pos:     unconvertPos(pass, p),
			Message: "unnecessary conversion",
		})
	}
	return nil, nil
}

func unconvertPos(pass *analysis.Pass, pos token.Position) token.Pos {
	if !pos.IsValid() {
		return token.NoPos
	}
	var found *token.File
	pass.Fset.Iterate(func(f *token.File) bool {
		if f.Name() == pos.Filename {
			found = f
			return false
		}
		return true
	})
	if found == nil {
		return token.NoPos
	}
	if pos.Line < 1 || pos.Line > found.LineCount() {
		return token.NoPos
	}
	off := token.Pos(pos.Column - 1)
	if pos.Column < 1 {
		off = 0
	}
	return found.LineStart(pos.Line) + off
}
