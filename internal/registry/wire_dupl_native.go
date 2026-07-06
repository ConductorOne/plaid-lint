// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/token"
	"sync"

	duplAPI "github.com/golangci/dupl/lib"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// duplDefaultThreshold mirrors upstream golangci-lint v2's default
// for [config.DuplSettings.Threshold]: 150 tokens. Clones below that
// size are considered noise. The mibk/dupl binary's default is 100;
// the higher value matches what plaid's subprocess wrapper used
// (`-t 150`) and what golangci-lint reimplements as its default.
const duplDefaultThreshold = 150

// wireAnalyzerFnsDuplNative attaches the in-process `dupl` Analyzer.
// The check builds a per-package suffix tree over the files in
// pass.Files and reports clone groups whose token count exceeds
// [config.DuplSettings.Threshold]. golangci-lint v2's port in
// pkg/golinters/dupl/dupl.go is the reference; the underlying
// library is github.com/golangci/dupl/lib (a golangci-maintained
// fork of mibk/dupl that exposes the suffix-tree walker behind a
// stable Run(files, threshold) API).
//
// Cross-package coverage delta: the subprocess Runner walked
// every .go file in the module root and built one global suffix
// tree, so it detected clones that spanned package boundaries.
// The native port runs per-pass and only sees one package at a
// time — cross-package clones are not detected. This matches
// golangci-lint v2's behavior exactly (a known and accepted
// limitation upstream). The native port ships per-pass after
// concluding the engine-wide finalizer-hook investment is
// unjustified for one linter whose standalone cascade savings
// are ~10s.
//
// Message format: `N-M lines are duplicate of file:start-end` —
// matches the subprocess wrapper's emission so c1's exclusion
// rules continue to apply unchanged.
//
// Aggregation: the underlying library Run() is invoked once per
// pass.Files set. A pass-scoped mu guards the per-Analyzer issue
// slice for the unlikely future case where the engine parallelizes
// passes belonging to the same analyzer; today the analyzer
// instance is shared across packages and pass.Report is the
// channel back to the engine, so the mu is defensive.
func wireAnalyzerFnsDuplNative(c *catalog) {
	wireNativeFn(c, "dupl", func(cfg any) []*analysis.Analyzer {
		threshold := duplDefaultThreshold
		if s, ok := cfg.(*config.DuplSettings); ok && s != nil && s.Threshold > 0 {
			threshold = s.Threshold
		}
		// runDupl reads pass.Files (for filenames only) +
		// pass.Fset, opens the .go files from disk via duplAPI.Run,
		// and reports clones via pass.Report. No pass.TypesInfo,
		// pass.Pkg, or pass.ResultOf. Per-pass scope matches the
		// shipped semantics (no cross-package clone detection).
		// Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(duplAnalyzer(threshold), 1)}
	})
}

func duplAnalyzer(threshold int) *analysis.Analyzer {
	var mu sync.Mutex
	return &analysis.Analyzer{
		Name: "dupl",
		Doc:  "Detects duplicate fragments of code.",
		Run: func(pass *analysis.Pass) (any, error) {
			return runDupl(pass, threshold, &mu)
		},
	}
}

func runDupl(pass *analysis.Pass, threshold int, mu *sync.Mutex) (any, error) {
	files := duplGoFileNames(pass)
	if len(files) == 0 {
		return nil, nil
	}

	// duplAPI.Run reads each file from disk, builds the suffix tree,
	// and returns one Issue per clone fragment (so a clone group of
	// 3 fragments yields 3 issues, each pointing From=this Frag,
	// To=next Frag in the group). The library is not pass-safe to
	// call concurrently because its internal goroutines fan out
	// over the supplied file list with shared channels; the mu
	// serializes overlapping pass invocations against the same
	// Analyzer (the analyzer instance is shared across passes).
	mu.Lock()
	issues, err := duplAPI.Run(files, threshold)
	mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("dupl: run: %w", err)
	}

	for _, i := range issues {
		dupl := fmt.Sprintf("%s:%d-%d", i.To.Filename(), i.To.LineStart(), i.To.LineEnd())
		msg := fmt.Sprintf("%d-%d lines are duplicate of %s",
			i.From.LineStart(), i.From.LineEnd(), dupl)
		pass.Report(analysis.Diagnostic{
			Pos:     duplLinePos(pass, i.From.Filename(), i.From.LineStart()),
			Message: msg,
		})
	}
	return nil, nil
}

// duplGoFileNames returns the on-disk paths of every parsed .go file
// in pass.Files. Files without a backing on-disk path (cgo
// intermediates, in-memory overlays) are skipped because the dupl
// library opens each file via os.ReadFile.
func duplGoFileNames(pass *analysis.Pass) []string {
	out := make([]string, 0, len(pass.Files))
	for _, f := range pass.Files {
		name := pass.Fset.PositionFor(f.Pos(), false).Filename
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// duplLinePos resolves a (filename, line) pair to a token.Pos using
// the pass's FileSet. If the file is not registered with the FileSet
// (which can happen when the library reports a path that isn't in
// pass.Files — e.g. a transitive include) we fall back to the first
// file's package position so the diagnostic still lands somewhere
// the engine can attach exclusions to.
func duplLinePos(pass *analysis.Pass, filename string, line int) token.Pos {
	for _, f := range pass.Files {
		fp := pass.Fset.File(f.Pos())
		if fp == nil {
			continue
		}
		if fp.Name() != filename {
			continue
		}
		if line < 1 || line > fp.LineCount() {
			return f.Pos()
		}
		return fp.LineStart(line)
	}
	if len(pass.Files) > 0 {
		return pass.Files[0].Pos()
	}
	return token.NoPos
}
