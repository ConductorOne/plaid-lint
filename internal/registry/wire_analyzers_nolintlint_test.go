// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestNolintlint_ShapeNative asserts the catalog row was flipped and
// the AnalyzerFn is wired in.
func TestNolintlint_ShapeNative(t *testing.T) {
	e, ok := defaultCatalog.resolve("nolintlint")
	if !ok {
		t.Fatal("catalog missing nolintlint")
	}
	if e.Shape != ShapeNative {
		t.Errorf("Shape = %v, want ShapeNative", e.Shape)
	}
	if e.AnalyzerFn == nil {
		t.Error("AnalyzerFn is nil")
	}
}

// TestNolintlint_ParseDirective covers the directive grammar
// recognizer.
func TestNolintlint_ParseDirective(t *testing.T) {
	cases := []struct {
		name        string
		comment     string
		want        *nolintDirective
		wantNil     bool
		wantMalform bool
	}{
		{"bare", "//nolint", &nolintDirective{Specific: false}, false, false},
		{"bare-space", "// nolint", &nolintDirective{Specific: false}, false, false},
		{"bare-mixed-case", "//NOLINT", &nolintDirective{Specific: false}, false, false},
		{"bare-with-explanation", "//nolint // because reasons", &nolintDirective{Specific: false, Explanation: "because reasons"}, false, false},
		{"specific-one", "//nolint:errcheck", &nolintDirective{Specific: true, Linters: []string{"errcheck"}}, false, false},
		{"specific-many", "//nolint:errcheck,govet,staticcheck", &nolintDirective{Specific: true, Linters: []string{"errcheck", "govet", "staticcheck"}}, false, false},
		{"specific-spaces", "//nolint:errcheck, govet ,staticcheck", &nolintDirective{Specific: true, Linters: []string{"errcheck", "govet", "staticcheck"}}, false, false},
		{"specific-mixed-case", "//nolint:ErrCheck", &nolintDirective{Specific: true, Linters: []string{"errcheck"}}, false, false},
		{"specific-with-explanation", "//nolint:errcheck // see issue 123", &nolintDirective{Specific: true, Linters: []string{"errcheck"}, Explanation: "see issue 123"}, false, false},
		{"wildcard-all", "//nolint:all", &nolintDirective{Specific: false}, false, false},
		{"malformed-empty-list", "//nolint:", nil, false, true},
		{"malformed-trailing-comma", "//nolint:foo,", nil, false, true},
		{"malformed-double-comma", "//nolint:foo,,bar", nil, false, true},
		{"not-a-directive-suffix", "//nolinted", nil, true, false},
		{"not-a-directive-hyphen", "//nolint-foo", nil, true, false},
		{"not-a-directive-shebang", "//!/usr/bin/env", nil, true, false},
		{"not-a-directive-empty", "", nil, true, false},
		{"not-a-directive-other", "// regular comment", nil, true, false},
		{"block-comment", "/*nolint:errcheck*/", &nolintDirective{Specific: true, Linters: []string{"errcheck"}}, false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			d := parseNolintComment(&ast.Comment{Text: tc.comment})
			if tc.wantNil {
				if d != nil {
					t.Errorf("got %+v, want nil", d)
				}
				return
			}
			if d == nil {
				t.Fatal("got nil, want directive")
			}
			if tc.wantMalform {
				if !d.Malformed {
					t.Errorf("Malformed = false, want true")
				}
				return
			}
			if d.Malformed {
				t.Errorf("Malformed = true, want false")
			}
			if d.Specific != tc.want.Specific {
				t.Errorf("Specific = %v, want %v", d.Specific, tc.want.Specific)
			}
			if !equalStrings(d.Linters, tc.want.Linters) {
				t.Errorf("Linters = %v, want %v", d.Linters, tc.want.Linters)
			}
			if d.Explanation != tc.want.Explanation {
				t.Errorf("Explanation = %q, want %q", d.Explanation, tc.want.Explanation)
			}
		})
	}
}

// TestNolintlint_RequireSpecific verifies bare directives are flagged
// while specific ones pass. The directive parser intentionally treats
// `//nolint:all` as bare (matching upstream), so it's flagged here too.
func TestNolintlint_RequireSpecific(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{requireSpecific: true}, `package x
var _ = 1 //nolint:errcheck
var _ = 2 //nolint
var _ = 3 //nolint:all
`)
	wantMessages := []string{
		"directive must name at least one specific linter",
		"directive must name at least one specific linter",
	}
	assertDiagnosticMessages(t, diags, wantMessages)
}

// TestNolintlint_RequireExplanation verifies missing-explanation
// directives are flagged.
func TestNolintlint_RequireExplanation(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{requireExplanation: true}, `package x
var _ = 1 //nolint:errcheck // why
var _ = 2 //nolint:errcheck
var _ = 3 //nolint
`)
	wantMessages := []string{
		"must have an explanation",
		"must have an explanation",
	}
	assertDiagnosticMessages(t, diags, wantMessages)
}

// TestNolintlint_AllowNoExplanation exempts linters listed in
// AllowNoExplanation from the explanation requirement.
func TestNolintlint_AllowNoExplanation(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{
		requireExplanation: true,
		allowNoExplanation: map[string]bool{
			"forbidigo":      true,
			"gochecknoinits": true,
		},
	}, `package x
var _ = 1 //nolint:forbidigo
var _ = 2 //nolint:gochecknoinits
var _ = 3 //nolint:errcheck
var _ = 4 //nolint:forbidigo,errcheck
`)
	// Two diagnostics: lines for errcheck and forbidigo,errcheck.
	wantMessages := []string{
		"must have an explanation",
		"must have an explanation",
	}
	assertDiagnosticMessages(t, diags, wantMessages)
}

// TestNolintlint_Malformed flags directives the recognizer rejects.
func TestNolintlint_Malformed(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{}, `package x
var _ = 1 //nolint:
var _ = 2 //nolint:foo,
var _ = 3 //nolint:foo,,bar
`)
	wantMessages := []string{
		"directive is malformed",
		"directive is malformed",
		"directive is malformed",
	}
	assertDiagnosticMessages(t, diags, wantMessages)
}

// TestNolintlint_BothChecks asserts the two checks compose — a bare
// directive without explanation fires both diagnostics.
func TestNolintlint_BothChecks(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{
		requireSpecific:    true,
		requireExplanation: true,
	}, `package x
var _ = 1 //nolint
`)
	if len(diags) != 2 {
		t.Fatalf("got %d diagnostics, want 2 (specific + explanation)", len(diags))
	}
	var sawSpecific, sawExplanation bool
	for _, d := range diags {
		if strings.Contains(d.Message, "must name at least one specific linter") {
			sawSpecific = true
		}
		if strings.Contains(d.Message, "must have an explanation") {
			sawExplanation = true
		}
	}
	if !sawSpecific {
		t.Error("missing specific diagnostic")
	}
	if !sawExplanation {
		t.Error("missing explanation diagnostic")
	}
}

// TestNolintlint_NoSettings_NoOp asserts the analyzer is a no-op when
// every check is off.
func TestNolintlint_NoSettings_NoOp(t *testing.T) {
	diags := runOnSource(t, nolintlintSettings{}, `package x
var _ = 1 //nolint
var _ = 2 //nolint:errcheck
var _ = 3 //nolint:errcheck // why
`)
	if len(diags) != 0 {
		t.Errorf("got %d diagnostics, want 0 (every check is off)", len(diags))
	}
}

// TestNolintlint_BuildEnableSet covers the Build path: enabling
// `nolintlint` with c1-shaped settings yields a non-nil Analyzer.
func TestNolintlint_BuildEnableSet(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"nolintlint"}
	cfg.Linters.Settings.NoLintLint = config.NoLintLintSettings{
		RequireExplanation: true,
		RequireSpecific:    true,
		AllowNoExplanation: []string{"forbidigo", "tracecheck", "gomnd", "gochecknoinits", "makezero"},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "nolintlint" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("nolintlint Analyzer is nil")
		}
		if r.Analyzer.Name != "nolintlint" {
			t.Errorf("Analyzer.Name = %q, want %q", r.Analyzer.Name, "nolintlint")
		}
		s, ok := r.Settings.(*config.NoLintLintSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.NoLintLintSettings", r.Settings)
		}
		if !s.RequireExplanation || !s.RequireSpecific {
			t.Errorf("RequireExplanation/RequireSpecific lost: %+v", s)
		}
		if len(s.AllowNoExplanation) != 5 {
			t.Errorf("AllowNoExplanation len = %d, want 5", len(s.AllowNoExplanation))
		}
	}
	if !seen {
		t.Error("nolintlint missing from Enabled()")
	}
}

// TestNolintlint_DirectiveString sanity-checks the diagnostic-message
// formatter — bare vs specific renders.
func TestNolintlint_DirectiveString(t *testing.T) {
	bare := nolintDirectiveString(&nolintDirective{Specific: false})
	if bare != "//nolint" {
		t.Errorf("bare = %q, want %q", bare, "//nolint")
	}
	spec := nolintDirectiveString(&nolintDirective{Specific: true, Linters: []string{"errcheck", "govet"}})
	if spec != "//nolint:errcheck,govet" {
		t.Errorf("specific = %q, want %q", spec, "//nolint:errcheck,govet")
	}
}

// TestNolintlint_DirectiveExempt covers the boundary cases of the
// allow-no-explanation list.
func TestNolintlint_DirectiveExempt(t *testing.T) {
	allow := map[string]bool{"forbidigo": true, "gochecknoinits": true}

	cases := []struct {
		name  string
		d     *nolintDirective
		allow map[string]bool
		want  bool
	}{
		{"bare-never-exempt", &nolintDirective{Specific: false}, allow, false},
		{"single-match", &nolintDirective{Specific: true, Linters: []string{"forbidigo"}}, allow, true},
		{"all-match", &nolintDirective{Specific: true, Linters: []string{"forbidigo", "gochecknoinits"}}, allow, true},
		{"mixed-not-exempt", &nolintDirective{Specific: true, Linters: []string{"forbidigo", "errcheck"}}, allow, false},
		{"empty-allow-list", &nolintDirective{Specific: true, Linters: []string{"forbidigo"}}, map[string]bool{}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := directiveExempt(tc.d, tc.allow); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// runOnSource parses src as a single Go file, synthesizes a minimal
// *analysis.Pass via the standard package loader path, runs the
// analyzer's Run callback, and returns the diagnostics. We don't go
// through analysistest because the `// want` convention treats trailing
// `// want \`...\`` text on the same line as test fixture metadata, but
// our nolintlint parser treats it as the directive's explanation —
// which is exactly the source comment shape under test.
func runOnSource(t *testing.T, settings nolintlintSettings, src string) []analysis.Diagnostic {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	a := newNolintlintAnalyzer(settings)
	var diags []analysis.Diagnostic
	pass := &analysis.Pass{
		Analyzer: a,
		Fset:     fset,
		Files:    []*ast.File{f},
		Report:   func(d analysis.Diagnostic) { diags = append(diags, d) },
	}
	if _, err := a.Run(pass); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return diags
}

// assertDiagnosticMessages checks that the slice of diagnostics
// matches the expected count and that each diagnostic's Message
// contains the corresponding substring. Order of diagnostics follows
// source position.
func assertDiagnosticMessages(t *testing.T, diags []analysis.Diagnostic, want []string) {
	t.Helper()
	if len(diags) != len(want) {
		t.Errorf("got %d diagnostics, want %d", len(diags), len(want))
		for i, d := range diags {
			t.Logf("  [%d] %s", i, d.Message)
		}
		return
	}
	for i := range want {
		if !strings.Contains(diags[i].Message, want[i]) {
			t.Errorf("diag[%d] = %q, want substring %q", i, diags[i].Message, want[i])
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
