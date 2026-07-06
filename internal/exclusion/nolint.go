// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"sync"

	"github.com/conductorone/plaid-lint/internal/output"
)

// nolintFilter implements the upstream `nolint_filter` processor:
// diagnostics whose `(file, line)` falls inside a `//nolint[:list]`
// inline range, or inside the AST-node range expanded from a leading
// `//nolint` comment on the line above, are dropped.
//
// Mirrors golangci-lint v2.9 master commit 72798d3
// (pkg/result/processors/nolint_filter.go).
//
// Scope: bare `//nolint` and `//nolint:all` drop every linter on the
// matched range. `//nolint:linter1,linter2` drops only those linters
// (matched against the diagnostic's `Linter` field, lowercased; family
// aliases like `staticcheck` → ST/SA/QF/S1 are honored).
//
// The filter parses each touched file once on demand and caches the
// resulting ranges. It does NOT validate that the named linters exist
// (the `nolintlint` analyzer reports those separately).
type nolintFilter struct {
	cache   map[string][]ignoredRange
	cacheMu sync.Mutex
}

// ignoredRange is one nolint window — inclusive line span plus the
// list of linters it covers. Empty linters means "all".
type ignoredRange struct {
	fromLine int
	toLine   int
	col      int
	linters  []string // lowercased; empty means "all"
}

// newNolintFilter returns a filter with an empty cache.
func newNolintFilter() *nolintFilter {
	return &nolintFilter{cache: map[string][]ignoredRange{}}
}

// suppresses reports whether d's (file, line, linter) is covered by a
// `//nolint` directive in the file. Errors during file parsing degrade
// to "not suppressed" so we don't silently drop diagnostics on parse
// failure (which the user will see surfaced separately as a typecheck
// diagnostic).
func (n *nolintFilter) suppresses(d output.Diagnostic) bool {
	if n == nil {
		return false
	}
	if d.Pos.Filename == "" || d.Pos.Line == 0 {
		return false
	}
	ranges := n.rangesFor(d.Pos.Filename)
	if len(ranges) == 0 {
		return false
	}
	for _, r := range ranges {
		if d.Pos.Line < r.fromLine || d.Pos.Line > r.toLine {
			continue
		}
		if rangeCoversLinter(r, d.Linter) {
			return true
		}
	}
	return false
}

// rangesFor returns (and caches) the ignored-range slice for path.
func (n *nolintFilter) rangesFor(path string) []ignoredRange {
	n.cacheMu.Lock()
	defer n.cacheMu.Unlock()
	if rs, ok := n.cache[path]; ok {
		return rs
	}
	rs := buildIgnoredRangesForFile(path)
	n.cache[path] = rs
	return rs
}

// buildIgnoredRangesForFile parses path, extracts inline `//nolint`
// ranges from every comment, and expands ranges whose comment is the
// line directly above an AST node — matching upstream's
// `rangeExpander.Visit` (function-level `//nolint` comments suppress
// the entire function body).
//
// Returns nil on parse failure; the caller treats nil as "no
// suppression" rather than surfacing the parse error.
func buildIgnoredRangesForFile(path string) []ignoredRange {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}
	inline := extractInlineNolintRanges(fset, f.Comments)
	if len(inline) == 0 {
		return nil
	}
	expanded := expandNolintRanges(fset, f, inline)
	out := make([]ignoredRange, 0, len(inline)+len(expanded))
	out = append(out, inline...)
	out = append(out, expanded...)
	return out
}

// extractInlineNolintRanges turns every `//nolint` comment in the file
// into one inline `ignoredRange` covering the comment group's own
// lines. Trailing `//nolint:foo` on a code line is a single-line range.
func extractInlineNolintRanges(fset *token.FileSet, groups []*ast.CommentGroup) []ignoredRange {
	var out []ignoredRange
	for _, g := range groups {
		for _, c := range g.List {
			r, ok := parseNolintRange(c, g, fset)
			if !ok {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// parseNolintRange recognizes one comment as a `//nolint` directive.
// Returns the inline range covering the comment group.
//
// Grammar matches upstream's `extractInlineRangeFromComment` and
// plaid's existing `parseNolintComment` (registry/wire_analyzers_nolintlint.go):
//   - `//nolint`                 → all linters
//   - `//nolint:all`             → all linters (`all` is the sentinel)
//   - `//nolint:foo`             → only foo
//   - `//nolint:foo,bar`         → only foo + bar
//   - `//nolint:foo // why`      → only foo (explanation after //)
//
// The leading `//` may be `// ` (space-then-nolint); `/* nolint */` is
// accepted. Linter names are lowercased and trimmed.
func parseNolintRange(c *ast.Comment, g *ast.CommentGroup, fset *token.FileSet) (ignoredRange, bool) {
	text := c.Text
	switch {
	case strings.HasPrefix(text, "//"):
		text = strings.TrimLeft(text[2:], " \t")
	case strings.HasPrefix(text, "/*") && strings.HasSuffix(text, "*/"):
		text = strings.TrimSpace(text[2 : len(text)-2])
	default:
		return ignoredRange{}, false
	}
	if len(text) < len("nolint") {
		return ignoredRange{}, false
	}
	if !strings.EqualFold(text[:len("nolint")], "nolint") {
		return ignoredRange{}, false
	}
	rest := text[len("nolint"):]
	// Upstream's regex is `^nolint( |:|$)` — the next char must be
	// space, colon, or end-of-string; rejecting `nolintlint`, etc.
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' && rest[0] != ':' {
		return ignoredRange{}, false
	}
	// Strip any trailing `// explanation`; upstream cuts at the next
	// `//` so `//nolint:foo // why` keeps only `:foo`.
	if i := strings.Index(rest, "//"); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.TrimSpace(rest)

	startPos := fset.Position(g.Pos())
	endPos := fset.Position(g.End())
	r := ignoredRange{
		fromLine: startPos.Line,
		toLine:   endPos.Line,
		col:      startPos.Column,
	}

	switch {
	case rest == "":
		// Bare //nolint — all linters.
		return r, true
	case strings.HasPrefix(rest, ":"):
		list := strings.TrimSpace(rest[1:])
		if list == "" {
			// `//nolint:` is malformed — no suppression.
			return ignoredRange{}, false
		}
		if strings.EqualFold(list, "all") {
			// //nolint:all — all linters.
			return r, true
		}
		for _, name := range strings.Split(list, ",") {
			n := strings.ToLower(strings.TrimSpace(name))
			if n == "" {
				return ignoredRange{}, false
			}
			r.linters = append(r.linters, n)
		}
		return r, true
	}
	return ignoredRange{}, false
}

// expandNolintRanges mirrors upstream's `rangeExpander.Visit`. A
// `//nolint` comment that sits on the line directly above a top-level
// AST node, and starts at the same column as that node, has its `To`
// extended to the node's end line. This is what makes
// `//nolint:nonamedreturns` immediately above a function declaration
// suppress diagnostics inside the function body.
func expandNolintRanges(fset *token.FileSet, f *ast.File, inline []ignoredRange) []ignoredRange {
	if len(inline) == 0 {
		return nil
	}
	var expanded []ignoredRange
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		start := fset.Position(n.Pos())
		end := fset.Position(n.End())
		for _, r := range inline {
			if r.toLine == start.Line-1 && r.col == start.Column {
				ex := r
				if ex.toLine < end.Line {
					ex.toLine = end.Line
				}
				expanded = append(expanded, ex)
			}
		}
		return true
	})
	return expanded
}

// rangeCoversLinter reports whether the range suppresses diagnostics
// from linter `name`. An empty `r.linters` is the "all" wildcard.
//
// Family-alias resolution mirrors `matchLinterName` for the
// staticcheck family — e.g. `//nolint:staticcheck` suppresses ST/SA/QF
// diagnostics emitted by plaid's fan-out.
func rangeCoversLinter(r ignoredRange, name string) bool {
	if len(r.linters) == 0 {
		return true
	}
	lower := strings.ToLower(name)
	for _, l := range r.linters {
		if l == lower {
			return true
		}
		if fam, ok := familyByPrefix(name); ok && l == strings.ToLower(fam) {
			return true
		}
	}
	return false
}
