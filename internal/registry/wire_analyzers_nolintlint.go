// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// nolintlint is reimplemented in-tree because the upstream lint logic
// lives at `github.com/golangci/golangci-lint/v2/pkg/golinters/nolintlint/internal`
// and the `internal` segment makes it unimportable from outside the
// golangci-lint module (landmine 26). The in-tree analyzer parses
// `//nolint` directives in source comments and applies the three
// load-bearing checks c1's `.golangci.yml` exercises:
//
//   - RequireSpecific:    a bare `//nolint` (no linter list) is flagged.
//   - RequireExplanation: a `//nolint:foo` without `// trailing text`
//     is flagged unless `foo` is listed in AllowNoExplanation.
//   - Malformed parse:    a `//nolint` whose syntax doesn't match the
//     upstream directive grammar (`//nolint:<comma-list>[ // <explanation>]`)
//     is flagged.
//
// AllowUnused (flag `//nolint:foo` where foo doesn't apply to any
// diagnostic on that line) is deliberately deferred. A faithful
// implementation needs to cross-reference the active diagnostic set
// for the same line, which the engine does not currently surface to
// in-pass analyzers. Documented as landmine 38 in the playbook.
//
// The analyzer name is "nolintlint" (matches the catalog row); the
// diagnostic Message format mirrors upstream's wrapper so c1's
// existing nolint hygiene work translates cleanly.
func wireAnalyzerFnsNolintlint(c *catalog) {
	wireNativeFn(c, "nolintlint", func(cfg any) []*analysis.Analyzer {
		s, _ := cfg.(*config.NoLintLintSettings)
		settings := nolintlintSettings{}
		if s != nil {
			settings.requireExplanation = s.RequireExplanation
			settings.requireSpecific = s.RequireSpecific
			settings.allowNoExplanation = make(map[string]bool, len(s.AllowNoExplanation))
			for _, n := range s.AllowNoExplanation {
				settings.allowNoExplanation[strings.ToLower(strings.TrimSpace(n))] = true
			}
		}
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(newNolintlintAnalyzer(settings), 1)}
	})
}

// nolintlintSettings is the resolved, normalized settings carried in
// the analyzer's Run closure. Mirrors the load-bearing subset of
// `config.NoLintLintSettings` after lower-cased + trimmed name lookup
// keys are computed up front.
type nolintlintSettings struct {
	requireExplanation bool
	requireSpecific    bool
	allowNoExplanation map[string]bool // lower-cased name → exempt
}

// newNolintlintAnalyzer returns a fresh *analysis.Analyzer whose Run
// closes over a copy of settings. Each Build gets its own analyzer
// instance, so two Builds with different NoLintLintSettings do not
// race on a shared flag set (the standard fresh-per-call pattern).
func newNolintlintAnalyzer(settings nolintlintSettings) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "nolintlint",
		Doc:  "Reports ill-formed or insufficient //nolint directives.",
		Run: func(pass *analysis.Pass) (any, error) {
			for _, file := range pass.Files {
				for _, cg := range file.Comments {
					for _, c := range cg.List {
						checkNolintComment(pass, c, settings)
					}
				}
			}
			return nil, nil
		},
	}
}

// nolintDirective is the parsed form of a single //nolint comment.
// The grammar mirrors upstream's:
//
//	//nolint[:linter1,linter2][ // explanation]
//	//nolint:all     (special — equivalent to bare //nolint)
//
// `Specific` is true when at least one linter name follows the colon.
// `Explanation` is the trailing comment text (post the inner `//`),
// trimmed; empty means no explanation.
type nolintDirective struct {
	Pos         token.Pos
	Specific    bool
	Linters     []string // lower-cased, trimmed; empty if Specific is false
	Explanation string   // trimmed; "" if absent
	Malformed   bool     // grammar didn't parse — pos still valid
}

// parseNolintComment scans a single comment's text. Returns nil if the
// comment isn't a //nolint directive. The recognizer is intentionally
// forgiving: trailing whitespace, `// nolint` (space after //), and
// mixed-case `//NoLint` are all accepted. The upstream grammar treats
// these the same, and rejecting them here would cause more diagnostics
// than upstream does.
func parseNolintComment(c *ast.Comment) *nolintDirective {
	text := c.Text
	if text == "" {
		return nil
	}
	// Strip the leading // or /* */. Block comments are valid per
	// upstream but rarely used; preserve the inner content.
	switch {
	case strings.HasPrefix(text, "//"):
		text = text[2:]
	case strings.HasPrefix(text, "/*") && strings.HasSuffix(text, "*/"):
		text = strings.TrimSuffix(text[2:], "*/")
	default:
		return nil
	}
	// Trim a single leading space; upstream accepts both `//nolint`
	// and `// nolint` but treats them the same.
	text = strings.TrimLeft(text, " \t")
	// Must begin with the literal "nolint" token (case-insensitive).
	if len(text) < len("nolint") {
		return nil
	}
	if !strings.EqualFold(text[:len("nolint")], "nolint") {
		return nil
	}
	rest := text[len("nolint"):]
	// rest is one of:
	//   ""                 → bare //nolint
	//   ":foo,bar"         → //nolint:foo,bar
	//   ":foo // why"      → //nolint:foo // why
	//   " // why"          → //nolint // why (bare with explanation)
	//   anything else      → malformed
	d := &nolintDirective{Pos: c.Pos()}

	// Optional explanation split: anything after a `//` after the
	// linter list is the explanation. Note: we look for `//` not `// ` so
	// `//nolint:foo//why` is accepted (rare but upstream accepts it).
	var head, expl string
	if i := strings.Index(rest, "//"); i >= 0 {
		head = rest[:i]
		expl = strings.TrimSpace(rest[i+2:])
	} else {
		head = rest
	}
	d.Explanation = expl

	head = strings.TrimRight(head, " \t")
	switch {
	case head == "":
		// Bare //nolint or //nolint // why
		d.Specific = false
	case strings.HasPrefix(head, ":"):
		// //nolint:foo,bar
		list := strings.TrimSpace(head[1:])
		if list == "" {
			// `//nolint:` is malformed (colon with empty list).
			d.Malformed = true
			return d
		}
		// `all` is upstream's "wildcard" sentinel — treated as
		// non-specific (equivalent to bare //nolint).
		if strings.EqualFold(list, "all") {
			d.Specific = false
			return d
		}
		for _, name := range strings.Split(list, ",") {
			n := strings.ToLower(strings.TrimSpace(name))
			if n == "" {
				// e.g. `//nolint:foo,,bar` — malformed.
				d.Malformed = true
				return d
			}
			d.Linters = append(d.Linters, n)
		}
		d.Specific = len(d.Linters) > 0
	default:
		// `nolint<something other than : or space>` → not a directive.
		// e.g. `//nolinted` or `//nolint-foo`. Return nil so the caller
		// doesn't false-positive.
		return nil
	}
	return d
}

// checkNolintComment runs the three enforcement passes against a single
// directive. Anything that isn't a //nolint comment is silently skipped.
func checkNolintComment(pass *analysis.Pass, c *ast.Comment, s nolintlintSettings) {
	d := parseNolintComment(c)
	if d == nil {
		return
	}
	if d.Malformed {
		pass.Report(analysis.Diagnostic{
			Pos:     d.Pos,
			Message: "nolintlint: directive is malformed; expected `//nolint:<linter>[,<linter>]* [// explanation]`",
		})
		return
	}
	if s.requireSpecific && !d.Specific {
		pass.Report(analysis.Diagnostic{
			Pos:     d.Pos,
			Message: "nolintlint: directive must name at least one specific linter (e.g. `//nolint:errcheck`)",
		})
		// Continue to the explanation check — both can fire on the same
		// directive (`//nolint` with no linters AND no explanation).
	}
	if s.requireExplanation && d.Explanation == "" {
		if !directiveExempt(d, s.allowNoExplanation) {
			pass.Report(analysis.Diagnostic{
				Pos:     d.Pos,
				Message: fmt.Sprintf("nolintlint: directive %q must have an explanation (e.g. `// explain why`)", nolintDirectiveString(d)),
			})
		}
	}
}

// directiveExempt reports whether every linter named in the directive
// appears in the allow-no-explanation list. A bare //nolint without
// any specific linters is never exempt (the AllowNoExplanation list is
// keyed by linter name; without one, there is no key to consult).
func directiveExempt(d *nolintDirective, allow map[string]bool) bool {
	if !d.Specific || len(allow) == 0 || len(d.Linters) == 0 {
		return false
	}
	for _, n := range d.Linters {
		if !allow[n] {
			return false
		}
	}
	return true
}

// nolintDirectiveString renders the directive back to its source form
// for use in diagnostic messages. The reverse of parseNolintComment.
func nolintDirectiveString(d *nolintDirective) string {
	if !d.Specific {
		return "//nolint"
	}
	return "//nolint:" + strings.Join(d.Linters, ",")
}
