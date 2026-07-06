// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// dogsledDefaultMaxBlankIdentifiers mirrors upstream golangci-lint's
// default for [config.DogsledSettings.MaxBlankIdentifiers]: a
// declaration with more than two blank identifiers triggers a
// finding. Matches both `alexkohler/dogsled`'s upstream default and
// golangci-lint's `New(settings)` path.
const dogsledDefaultMaxBlankIdentifiers = 2

// wireAnalyzerFnsDogsledNative attaches the in-process `dogsled`
// Analyzer. The check flags `:=` and `=` assignments whose LHS holds
// more than N blank identifiers (default 2). golangci-lint's
// reimplementation in pkg/golinters/dogsled/dogsled.go is the
// reference; the linter has no external library dep upstream.
//
// Message format: `declaration has N blank identifiers` — matches
// golangci's port. The subprocess wrapper appended the assignment
// source text after a colon; the native port drops that detail
// because reconstructing it requires re-printing the AST node, and
// no exclusion rule in the c1 corpus matches against the trailing
// expression text.
func wireAnalyzerFnsDogsledNative(c *catalog) {
	wireNativeFn(c, "dogsled", func(cfg any) []*analysis.Analyzer {
		maxBlank := dogsledDefaultMaxBlankIdentifiers
		if s, ok := cfg.(*config.DogsledSettings); ok && s != nil && s.MaxBlankIdentifiers > 0 {
			maxBlank = s.MaxBlankIdentifiers
		}
		// runDogsled reads only pass.Files (ast.Inspect
		// for *ast.AssignStmt, counts blank ident LHS) and calls
		// pass.Reportf. No pass.TypesInfo / pass.Pkg / pass.ResultOf.
		// Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(dogsledAnalyzer(maxBlank), 1)}
	})
}

func dogsledAnalyzer(maxBlank int) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "dogsled",
		Doc:  "Checks assignments with too many blank identifiers (e.g. x, _, _, _, := f()).",
		Run: func(pass *analysis.Pass) (any, error) {
			return runDogsled(pass, maxBlank)
		},
	}
}

func runDogsled(pass *analysis.Pass, maxBlank int) (any, error) {
	for _, f := range pass.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			blank := 0
			for _, lhs := range assign.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if id.Name == "_" {
					blank++
				}
			}
			if blank > maxBlank {
				pass.Reportf(assign.Pos(), "declaration has %d blank identifiers", blank)
			}
			return true
		})
	}
	return nil, nil
}
