// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
)

// wireAnalyzerFnsGochecknoinitsNative attaches the in-process
// `gochecknoinits` Analyzer. The check flags every top-level `init`
// function declaration that has no receiver (free function, not a
// method). golangci-lint's reimplementation in
// pkg/golinters/gochecknoinits/gochecknoinits.go is the reference.
//
// Settings: none — gochecknoinits has no per-run knobs in
// [config.LintersSettings]. The cfg arg is ignored.
//
// Message format matches the subproc wrapper's emission (`init
// function`) so existing exclusion rules over the diagnostic stem
// continue to apply.
func wireAnalyzerFnsGochecknoinitsNative(c *catalog) {
	wireNativeFn(c, "gochecknoinits", func(_ any) []*analysis.Analyzer {
		// runGochecknoinits reads only pass.Files and
		// calls pass.Reportf — no pass.TypesInfo, pass.Pkg, or
		// pass.ResultOf. Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(gochecknoinitsAnalyzer(), 1)}
	})
}

func gochecknoinitsAnalyzer() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "gochecknoinits",
		Doc:  "Checks that no init functions are present in Go code.",
		Run:  runGochecknoinits,
	}
}

func runGochecknoinits(pass *analysis.Pass) (any, error) {
	for _, f := range pass.Files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name == nil || fn.Name.Name != "init" {
				continue
			}
			// Methods (functions with a receiver) named init are
			// legal user code, not the package-init function the
			// linter targets.
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				continue
			}
			pass.Reportf(fn.Pos(), "init function")
		}
	}
	return nil, nil
}
