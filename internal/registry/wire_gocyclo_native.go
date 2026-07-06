// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/token"

	"golang.org/x/tools/go/analysis"

	gocyclolib "github.com/fzipp/gocyclo"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// gocycloDefaultMinComplexity mirrors upstream golangci-lint's
// default for [config.GoCycloSettings.MinComplexity]: functions with
// cyclomatic complexity >= 30 trigger a finding.
const gocycloDefaultMinComplexity = 30

// wireAnalyzerFnsGocycloNative attaches the in-process `gocyclo`
// Analyzer. The check computes cyclomatic complexity for every
// function and method declaration in each *ast.File of the pass and
// reports any whose complexity meets or exceeds the configured
// threshold. Upstream library is `github.com/fzipp/gocyclo` — we
// call AnalyzeASTFile per pass.Files entry to reuse the parsed AST.
//
// Message format: `cyclomatic complexity N of func \`<name>\` is
// high (> M)` — matches the subproc wrapper's emission so existing
// c1 exclusion rules over the diagnostic stem continue to apply.
func wireAnalyzerFnsGocycloNative(c *catalog) {
	wireNativeFn(c, "gocyclo", func(cfg any) []*analysis.Analyzer {
		minComplexity := gocycloDefaultMinComplexity
		if s, ok := cfg.(*config.GoCycloSettings); ok && s != nil && s.MinComplexity > 0 {
			minComplexity = s.MinComplexity
		}
		// runGocyclo reads only pass.Files / pass.Fset
		// and calls gocyclolib.AnalyzeASTFile(file, pass.Fset, nil)
		// per file — a purely syntactic analyzer over ast.File. Reports
		// via pass.Report. No pass.TypesInfo, pass.Pkg, or
		// pass.ResultOf. Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(gocycloAnalyzer(minComplexity), 1)}
	})
}

func gocycloAnalyzer(minComplexity int) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "gocyclo",
		Doc:  "Computes and checks the cyclomatic complexity of functions.",
		Run: func(pass *analysis.Pass) (any, error) {
			return runGocyclo(pass, minComplexity)
		},
	}
}

func runGocyclo(pass *analysis.Pass, minComplexity int) (any, error) {
	for _, file := range pass.Files {
		stats := gocyclolib.AnalyzeASTFile(file, pass.Fset, nil)
		tf := pass.Fset.File(file.Pos())
		if tf == nil {
			continue
		}
		for _, s := range stats {
			if s.Complexity < minComplexity {
				continue
			}
			pass.Report(analysis.Diagnostic{
				Pos: gocycloPos(tf, s.Pos),
				Message: fmt.Sprintf(
					"cyclomatic complexity %d of func `%s` is high (> %d)",
					s.Complexity, s.FuncName, minComplexity-1,
				),
			})
		}
	}
	return nil, nil
}

func gocycloPos(tf *token.File, pos token.Position) token.Pos {
	if pos.Line < 1 || pos.Line > tf.LineCount() {
		return tf.Pos(0)
	}
	off := token.Pos(pos.Column - 1)
	if pos.Column < 1 {
		off = 0
	}
	return tf.LineStart(pos.Line) + off
}
