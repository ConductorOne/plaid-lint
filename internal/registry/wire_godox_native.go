// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/token"

	"golang.org/x/tools/go/analysis"

	godoxlib "github.com/matoous/godox"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// godoxDefaultKeywords mirrors golangci-lint's default for
// [config.GodoxSettings.Keywords] when the user supplies none.
var godoxDefaultKeywords = []string{"TODO", "BUG", "FIXME"}

// wireAnalyzerFnsGodoxNative attaches the in-process `godox`
// Analyzer. The check scans every comment in the package's *ast.File
// set for a configurable list of keywords (`TODO`, `BUG`, `FIXME`).
// Upstream library is `github.com/matoous/godox`, which exposes a
// per-file `Run(file, fset, keywords...) ([]Message, error)` entry
// point — we loop over `pass.Files` and translate each Message's
// token.Position back to a token.Pos.
//
// Message format: the library returns "Line N: KEYWORD(... )" stems
// matching golangci-lint's own port. Existing c1 exclusion rules
// match against the bare diagnostic text, so the format passes
// through unchanged.
func wireAnalyzerFnsGodoxNative(c *catalog) {
	wireNativeFn(c, "godox", func(cfg any) []*analysis.Analyzer {
		keywords := godoxDefaultKeywords
		if s, ok := cfg.(*config.GodoxSettings); ok && s != nil && len(s.Keywords) > 0 {
			keywords = s.Keywords
		}
		// runGodox reads only pass.Files / pass.Fset and
		// hands each *ast.File to godoxlib.Run(file, fset, keywords...)
		// (a per-file comment scanner). Reports via pass.Report. No
		// pass.TypesInfo, pass.Pkg, or pass.ResultOf. Classified
		// TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(godoxAnalyzer(keywords), 1)}
	})
}

func godoxAnalyzer(keywords []string) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "godox",
		Doc:  "Detects FIXME, TODO and other comment keywords.",
		Run: func(pass *analysis.Pass) (any, error) {
			return runGodox(pass, keywords)
		},
	}
}

func runGodox(pass *analysis.Pass, keywords []string) (any, error) {
	for _, file := range pass.Files {
		msgs, err := godoxlib.Run(file, pass.Fset, keywords...)
		if err != nil {
			// godox.Run only errors on internal IO failures it does
			// not currently emit; treat as soft failure to avoid
			// aborting the whole pass.
			continue
		}
		tf := pass.Fset.File(file.Pos())
		if tf == nil {
			continue
		}
		for _, m := range msgs {
			pass.Report(analysis.Diagnostic{
				Pos:     godoxPos(tf, m.Pos),
				Message: m.Message,
			})
		}
	}
	return nil, nil
}

// godoxPos resolves the library's token.Position (line/column) back
// to a token.Pos in the pass's FileSet. Falls back to the file's
// base when the line is out of range (overlay renumber).
func godoxPos(tf *token.File, pos token.Position) token.Pos {
	if pos.Line < 1 || pos.Line > tf.LineCount() {
		return tf.Pos(0)
	}
	off := token.Pos(pos.Column - 1)
	if pos.Column < 1 {
		off = 0
	}
	return tf.LineStart(pos.Line) + off
}
