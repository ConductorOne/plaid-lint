// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/token"

	"golang.org/x/tools/go/analysis"

	nestiflib "github.com/nakabonne/nestif"

	"github.com/conductorone/plaid-lint/internal/config"
)

// nestifDefaultMinComplexity mirrors upstream golangci-lint's
// default for [config.NestifSettings.MinComplexity]: nested-if blocks
// with complexity >= 5 trigger a finding.
const nestifDefaultMinComplexity = 5

// wireAnalyzerFnsNestifNative attaches the in-process `nestif`
// Analyzer. The check walks each *ast.File's body and reports
// root-level `if` statements whose nested complexity score meets or
// exceeds the configured threshold. Upstream library is
// `github.com/nakabonne/nestif` — we instantiate a Checker per
// Analyzer build (its MinComplexity field is the only knob) and call
// Check per pass.Files entry.
//
// Message format: “if <cond>` has complex nested blocks
// (complexity: N)“ — the library emits this stem verbatim, matching
// the subproc wrapper.
func wireAnalyzerFnsNestifNative(c *catalog) {
	wireNativeFn(c, "nestif", func(cfg any) []*analysis.Analyzer {
		minComplexity := nestifDefaultMinComplexity
		if s, ok := cfg.(*config.NestifSettings); ok && s != nil && s.MinComplexity > 0 {
			minComplexity = s.MinComplexity
		}
		return []*analysis.Analyzer{nestifAnalyzer(minComplexity)}
	})
}

func nestifAnalyzer(minComplexity int) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "nestif",
		Doc:  "Reports deeply nested if statements.",
		Run: func(pass *analysis.Pass) (any, error) {
			return runNestif(pass, minComplexity)
		},
	}
}

func runNestif(pass *analysis.Pass, minComplexity int) (any, error) {
	checker := &nestiflib.Checker{MinComplexity: minComplexity}
	for _, file := range pass.Files {
		issues := checker.Check(file, pass.Fset)
		tf := pass.Fset.File(file.Pos())
		if tf == nil {
			continue
		}
		for _, iss := range issues {
			pass.Report(analysis.Diagnostic{
				Pos:     nestifPos(tf, iss.Pos),
				Message: iss.Message,
			})
		}
	}
	return nil, nil
}

func nestifPos(tf *token.File, pos token.Position) token.Pos {
	if pos.Line < 1 || pos.Line > tf.LineCount() {
		return tf.Pos(0)
	}
	off := token.Pos(pos.Column - 1)
	if pos.Column < 1 {
		off = 0
	}
	return tf.LineStart(pos.Line) + off
}
