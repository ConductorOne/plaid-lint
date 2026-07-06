// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// Default values mirror the plaid-lint subprocess wrapper's
// defaults (LineLength=120, TabWidth=1), which intentionally differ
// from upstream `walle/lll`'s LineLength=80.
const (
	lllDefaultLineLength = 120
	lllDefaultTabWidth   = 1
)

// goCommentDirectivePrefix is the `//go:` line prefix lll skips
// per upstream behavior — directives can't be wrapped without
// breaking the toolchain. Matches golangci-lint's port.
const lllGoCommentDirectivePrefix = "//go:"

// wireAnalyzerFnsLllNative attaches the in-process `lll` Analyzer.
// The check reads each source file fresh from disk (the AST does
// not preserve original whitespace columns) and reports every line
// whose rune count exceeds [config.LllSettings.LineLength] after
// tab-expansion to [config.LllSettings.TabWidth] spaces.
// golangci-lint's reimplementation in pkg/golinters/lll/lll.go is
// the reference; the linter has no external library dep upstream.
//
// Message format: `line is N characters` — matches the upstream
// `walle/lll` binary's emission (and therefore the subproc
// wrapper's output). golangci's port uses a richer "The line is X
// characters long, which exceeds the maximum of Y characters."
// wording; we use the leaner form because that's what the existing
// canonical subproc output looks like.
//
// Import-block lines are exempt (the closing `)` and the
// individual import paths are not wrappable), per upstream's
// multi-import-skip heuristic.
func wireAnalyzerFnsLllNative(c *catalog) {
	wireNativeFn(c, "lll", func(cfg any) []*analysis.Analyzer {
		lineLen := lllDefaultLineLength
		tabWidth := lllDefaultTabWidth
		if s, ok := cfg.(*config.LllSettings); ok && s != nil {
			if s.LineLength > 0 {
				lineLen = s.LineLength
			}
			if s.TabWidth > 0 {
				tabWidth = s.TabWidth
			}
		}
		// runLll reads pass.Files + pass.Fset, opens each
		// file's source from disk (same workspace bytes), and reports
		// over-length lines. No pass.TypesInfo, pass.Pkg, or
		// pass.ResultOf. Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(lllAnalyzer(lineLen, tabWidth), 1)}
	})
}

func lllAnalyzer(lineLen, tabWidth int) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "lll",
		Doc:  "Reports long lines.",
		Run: func(pass *analysis.Pass) (any, error) {
			return runLll(pass, lineLen, tabWidth)
		},
	}
}

func runLll(pass *analysis.Pass, lineLen, tabWidth int) (any, error) {
	tabSpaces := strings.Repeat(" ", tabWidth)
	for _, file := range pass.Files {
		if err := lllScanFile(pass, file, lineLen, tabSpaces); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func lllScanFile(pass *analysis.Pass, file *ast.File, lineLen int, tabSpaces string) error {
	pos := pass.Fset.PositionFor(file.Pos(), false)
	if pos.Filename == "" {
		return nil
	}
	if !strings.HasSuffix(pos.Filename, ".go") {
		return nil
	}

	f, err := os.Open(pos.Filename)
	if err != nil {
		// Generated / virtual files (cgo intermediates, in-memory
		// overlays) may not be on disk; silently skip rather than
		// fail the whole pass.
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("lll: open %s: %w", pos.Filename, err)
	}
	defer f.Close()

	ft := pass.Fset.File(file.Pos())
	if ft == nil {
		return nil
	}

	lineNumber := 0
	multiImport := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), bufio.MaxScanTokenSize)
	for scanner.Scan() {
		lineNumber++

		line := scanner.Text()
		line = strings.ReplaceAll(line, "\t", tabSpaces)

		if strings.HasPrefix(line, lllGoCommentDirectivePrefix) {
			continue
		}

		if strings.HasPrefix(line, "import") {
			multiImport = strings.HasSuffix(line, "(")
			continue
		}

		if multiImport {
			if line == ")" {
				multiImport = false
			}
			continue
		}

		runeLen := utf8.RuneCountInString(line)
		if runeLen > lineLen {
			pass.Report(analysis.Diagnostic{
				Pos:     lllLineStart(ft, lineNumber),
				Message: fmt.Sprintf("line is %d characters", runeLen),
			})
		}
	}

	if err := scanner.Err(); err != nil {
		// A line longer than bufio.MaxScanTokenSize still violates
		// the maxLineLen budget when that budget is smaller; emit
		// it without losing the rest of the file's findings. This
		// is the same accommodation golangci-lint's port makes for
		// auto-generated files (go-bindata).
		if errors.Is(err, bufio.ErrTooLong) && lineLen < bufio.MaxScanTokenSize {
			pass.Report(analysis.Diagnostic{
				Pos:     lllLineStart(ft, lineNumber),
				Message: fmt.Sprintf("line is more than %d characters", bufio.MaxScanTokenSize),
			})
			return nil
		}
		return fmt.Errorf("lll: scan %s: %w", pos.Filename, err)
	}
	return nil
}

// lllLineStart returns the token.Pos for the first byte of the
// 1-indexed line, falling back to the file's base Pos if the line
// is out of range (a possibility when overlays renumber).
func lllLineStart(ft *token.File, line int) token.Pos {
	if line < 1 || line > ft.LineCount() {
		return ft.Pos(0)
	}
	return ft.LineStart(line)
}
