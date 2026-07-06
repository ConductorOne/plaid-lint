// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsWhitespaceNative attaches the in-process whitespace
// Analyzer. It is a behavior-preserving fork of `ultraware/whitespace`
// v0.2.0 that adds a per-file line-offset cache so the per-token
// `posLine` lookup stops thrashing the workspace-shared
// `go/token.(*File).unpack` mutex.
//
// Cold profiling attributed 8.86% of cum CPU to whitespace.Run, of
// which `posLine` (157.45s, 7.52%) + `firstAndLast` (184.62s, 8.81%)
// dominate via repeated `fset.Position(pos).Line` calls. The upstream
// implementation calls `posLine` once per AST node it visits,
// re-entering `(*File).unpack`'s mutex each time. On cold runs every
// analyzer concurrently walks the same files, so the mutex pinged at
// 5.49% flat. We memoize each file's line-offset table once at the top
// of `runFile` (via `token.File.Lines()`) and binary-search it in our
// own goroutine without taking any lock.
//
// Diagnostic output is byte-equivalent to upstream: same `Pos`, same
// message stems, same SuggestedFix shape. W6 cold↔warm digest contract
// is preserved.
func wireAnalyzerFnsWhitespaceNative(c *catalog) {
	wireNativeFn(c, "whitespace", func(cfg any) []*analysis.Analyzer {
		settings := wsSettings{}
		if s, ok := cfg.(*config.WhitespaceSettings); ok && s != nil {
			settings.multiIf = s.MultiIf
			settings.multiFunc = s.MultiFunc
		}
		// whitespace's Run reads only pass.Files / pass.Fset /
		// pass.Report — no pass.TypesInfo, pass.Pkg, or pass.ResultOf.
		// Classified TypeUseSyntaxOnly so importer L1 entries survive
		// dep-internals edits.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(newWhitespaceAnalyzer(settings), 1)}
	})
}

type wsSettings struct {
	multiIf   bool
	multiFunc bool
}

func newWhitespaceAnalyzer(settings wsSettings) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "whitespace",
		Doc:  "Whitespace is a linter that checks for unnecessary newlines at the start and end of functions, if, for, etc.",
		Run: func(pass *analysis.Pass) (any, error) {
			runWhitespace(pass, settings)
			return nil, nil
		},
		RunDespiteErrors: true,
	}
}

func runWhitespace(pass *analysis.Pass, settings wsSettings) {
	for _, file := range pass.Files {
		tf := pass.Fset.File(file.Pos())
		if tf == nil {
			continue
		}
		if !strings.HasSuffix(tf.Name(), ".go") {
			continue
		}
		// Pre-compute the line-offset slice once per file. Lines()
		// returns the file's internal []int of byte offsets at the start
		// of each line; binary-searching it locally avoids re-entering
		// (*File).unpack on every posLine call (the hot path).
		lines := tf.Lines()
		base := tf.Base()
		fc := &wsFileCache{base: base, lines: lines}
		for _, msg := range runWhitespaceFile(file, tf, fc, settings) {
			pass.Report(msg)
		}
	}
}

// wsFileCache holds a single file's line-offset slice plus its base
// position, so posLine reduces to a binary search inside our own slice
// without touching go/token's per-file mutex.
type wsFileCache struct {
	base  int
	lines []int
}

// line returns the 1-based line number for pos. Matches
// `(*token.File).Position(pos).Line` semantics — `Position.Line` is
// derived from a sort.Search over `(*File).lines` keyed by the byte
// offset `int(pos) - file.base`. We replicate that search against our
// memoized snapshot.
//
// Returns 0 when pos falls outside the file's byte range (mirrors the
// upstream behavior of a NoPos lookup; posLine callers in whitespace
// never compare 0-vs-1 explicitly, only line-vs-line, so a 0 propagates
// safely).
func (fc *wsFileCache) line(pos token.Pos) int {
	if !pos.IsValid() {
		return 0
	}
	off := int(pos) - fc.base
	if off < 0 || len(fc.lines) == 0 {
		return 0
	}
	// sort.Search returns the smallest index for which lines[i] > off;
	// the line number is that index (1-based since the search counts
	// the implicit line-1 zero-offset entry).
	i := sort.SearchInts(fc.lines, off+1)
	return i
}

type wsVisitor struct {
	comments    []*ast.CommentGroup
	fc          *wsFileCache
	messages    []analysis.Diagnostic
	wantNewline map[*ast.BlockStmt]bool
	settings    wsSettings
}

func runWhitespaceFile(file *ast.File, _ *token.File, fc *wsFileCache, settings wsSettings) []analysis.Diagnostic {
	var messages []analysis.Diagnostic
	for _, f := range file.Decls {
		decl, ok := f.(*ast.FuncDecl)
		if !ok || decl.Body == nil { // decl.Body can be nil for cgo
			continue
		}
		vis := &wsVisitor{
			comments:    file.Comments,
			fc:          fc,
			wantNewline: make(map[*ast.BlockStmt]bool),
			settings:    settings,
		}
		ast.Walk(vis, decl)
		messages = append(messages, vis.messages...)
	}
	return messages
}

func (v *wsVisitor) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return v
	}

	if stmt, ok := node.(*ast.IfStmt); ok && v.settings.multiIf {
		wsCheckMultiLine(v, stmt.Body, stmt.Cond)
	}
	if stmt, ok := node.(*ast.FuncLit); ok && v.settings.multiFunc {
		wsCheckMultiLine(v, stmt.Body, stmt.Type)
	}
	if stmt, ok := node.(*ast.FuncDecl); ok && v.settings.multiFunc {
		wsCheckMultiLine(v, stmt.Body, stmt.Type)
	}

	if stmt, ok := node.(*ast.BlockStmt); ok {
		wantNewline := v.wantNewline[stmt]
		comments := v.comments
		if wantNewline {
			comments = nil // Comments also count as a newline if we want a newline
		}
		opening, first, last := wsFirstAndLast(comments, v.fc, stmt)
		startMsg := wsCheckStart(v.fc, opening, first)
		if wantNewline && startMsg == nil && len(stmt.List) >= 1 {
			v.messages = append(v.messages, analysis.Diagnostic{
				Pos:     opening,
				Message: "multi-line statement should be followed by a newline",
				SuggestedFixes: []analysis.SuggestedFix{{
					TextEdits: []analysis.TextEdit{{
						Pos:     stmt.List[0].Pos(),
						End:     stmt.List[0].Pos(),
						NewText: []byte("\n"),
					}},
				}},
			})
		} else if !wantNewline && startMsg != nil {
			v.messages = append(v.messages, *startMsg)
		}
		if msg := wsCheckEnd(v.fc, stmt.Rbrace, last); msg != nil {
			v.messages = append(v.messages, *msg)
		}
	}
	return v
}

func wsCheckMultiLine(v *wsVisitor, body *ast.BlockStmt, stmtStart ast.Node) {
	start, end := v.fc.line(stmtStart.Pos()), v.fc.line(stmtStart.End())
	if end > start { // Check only multi line conditions
		v.wantNewline[body] = true
	}
}

func wsFirstAndLast(comments []*ast.CommentGroup, fc *wsFileCache, stmt *ast.BlockStmt) (token.Pos, ast.Node, ast.Node) {
	openingPos := stmt.Lbrace + 1
	if len(stmt.List) == 0 {
		return openingPos, nil, nil
	}
	first, last := ast.Node(stmt.List[0]), ast.Node(stmt.List[len(stmt.List)-1])
	for _, c := range comments {
		// Comment on same line as opening but after it: opening moves
		// to the comment's End (single-line) or first := comment
		// (multi-line). Matches upstream.
		if fc.line(c.Pos()) == fc.line(openingPos) && c.Pos() > openingPos {
			if fc.line(c.End()) != fc.line(openingPos) {
				first = c
			} else {
				openingPos = c.End()
			}
		}
		if fc.line(c.Pos()) == fc.line(stmt.Pos()) || fc.line(c.End()) == fc.line(stmt.End()) {
			continue
		}
		if c.Pos() < stmt.Pos() || c.End() > stmt.End() {
			continue
		}
		if c.Pos() < first.Pos() {
			first = c
		}
		if c.End() > last.End() {
			last = c
		}
	}
	return openingPos, first, last
}

func wsCheckStart(fc *wsFileCache, start token.Pos, first ast.Node) *analysis.Diagnostic {
	if first == nil {
		return nil
	}
	if fc.line(start)+1 < fc.line(first.Pos()) {
		return &analysis.Diagnostic{
			Pos:     start,
			Message: "unnecessary leading newline",
			SuggestedFixes: []analysis.SuggestedFix{{
				TextEdits: []analysis.TextEdit{{
					Pos:     start,
					End:     first.Pos(),
					NewText: []byte("\n"),
				}},
			}},
		}
	}
	return nil
}

func wsCheckEnd(fc *wsFileCache, end token.Pos, last ast.Node) *analysis.Diagnostic {
	if last == nil {
		return nil
	}
	if fc.line(end)-1 > fc.line(last.End()) {
		return &analysis.Diagnostic{
			Pos:     end,
			Message: "unnecessary trailing newline",
			SuggestedFixes: []analysis.SuggestedFix{{
				TextEdits: []analysis.TextEdit{{
					Pos:     last.End(),
					End:     end,
					NewText: []byte("\n"),
				}},
			}},
		}
	}
	return nil
}
