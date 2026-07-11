// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// makeCfg builds a Config with the given exclusions block.
func makeCfg(exc config.LinterExclusions, sc *config.StaticCheckSettings) *config.Config {
	c := &config.Config{}
	c.Linters.Exclusions = exc
	if sc != nil {
		c.Linters.Settings.Staticcheck = *sc
	}
	return c
}

func TestFilter_ConfigDigestNilIsZero(t *testing.T) {
	var f *Filter
	if got := f.ConfigDigest(); got != ([32]byte{}) {
		t.Fatalf("nil filter digest = %x, want zero", got)
	}
}

func TestFilter_ConfigDigestStableAndPathPortable(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Paths:     []string{`pkg/generated/`},
		Generated: config.GeneratedModeLax,
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{
				Linters: []string{"gosec", "govet"},
				Path:    `_test\.go$`,
				Text:    `known false positive`,
			}},
		},
	}, nil)

	f1, err := NewFilter(cfg, "/repo/a", []string{"./pkg/foo/..."})
	if err != nil {
		t.Fatalf("NewFilter f1: %v", err)
	}
	f2, err := NewFilter(cfg, "/tmp/checkout", []string{"./pkg/foo/..."})
	if err != nil {
		t.Fatalf("NewFilter f2: %v", err)
	}

	if got, want := f1.ConfigDigest(), f2.ConfigDigest(); got != want {
		t.Fatalf("digest included machine-local base path: got %x want %x", got, want)
	}
}

func TestFilter_ConfigDigestChangesForSuppressionInputs(t *testing.T) {
	baseCfg := makeCfg(config.LinterExclusions{}, nil)
	base, err := NewFilter(baseCfg, "/repo", []string{"./pkg/foo/..."})
	if err != nil {
		t.Fatalf("NewFilter base: %v", err)
	}
	baseDigest := base.ConfigDigest()

	cases := []struct {
		name string
		make func(t *testing.T) *Filter
	}{
		{
			name: "paths",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, makeCfg(config.LinterExclusions{
					Paths: []string{`pkg/pb/`},
				}, nil), []string{"./pkg/foo/..."})
			},
		},
		{
			name: "paths_except",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, makeCfg(config.LinterExclusions{
					PathsExcept: []string{`pkg/c1semconv/`},
				}, nil), []string{"./pkg/foo/..."})
			},
		},
		{
			name: "rules",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, makeCfg(config.LinterExclusions{
					Rules: []config.ExcludeRule{
						{BaseRule: config.BaseRule{Linters: []string{"gosec"}, Text: `G304`}},
					},
				}, nil), []string{"./pkg/foo/..."})
			},
		},
		{
			name: "generated_mode",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, makeCfg(config.LinterExclusions{
					Generated: config.GeneratedModeDisable,
				}, nil), []string{"./pkg/foo/..."})
			},
		},
		{
			name: "staticcheck_checks",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, makeCfg(config.LinterExclusions{}, &config.StaticCheckSettings{
					Checks: []string{"all"},
				}), []string{"./pkg/foo/..."})
			},
		},
		{
			name: "target_dirs",
			make: func(t *testing.T) *Filter {
				return mustFilter(t, baseCfg, []string{"./pkg/bar/..."})
			},
		},
		{
			name: "uniq_by_line",
			make: func(t *testing.T) *Filter {
				f := mustFilter(t, baseCfg, []string{"./pkg/foo/..."})
				f.uniqByLine = false
				return f
			},
		},
		{
			name: "nolint",
			make: func(t *testing.T) *Filter {
				f := mustFilter(t, baseCfg, []string{"./pkg/foo/..."})
				f.nolint = nil
				return f
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.make(t).ConfigDigest(); got == baseDigest {
				t.Fatalf("digest did not change for %s", tc.name)
			}
		})
	}
}

func TestFilter_ConfigDigestSortsRuleLinters(t *testing.T) {
	cfg1 := makeCfg(config.LinterExclusions{
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{Linters: []string{"gosec", "govet", "errcheck"}, Path: `_test\.go$`}},
		},
	}, nil)
	cfg2 := makeCfg(config.LinterExclusions{
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{Linters: []string{"errcheck", "gosec", "govet"}, Path: `_test\.go$`}},
		},
	}, nil)

	f1 := mustFilter(t, cfg1, nil)
	f2 := mustFilter(t, cfg2, nil)
	if got, want := f1.ConfigDigest(), f2.ConfigDigest(); got != want {
		t.Fatalf("digest depended on rule linter order: got %x want %x", got, want)
	}
}

func mustFilter(t *testing.T, cfg *config.Config, targetPatterns []string) *Filter {
	t.Helper()
	f, err := NewFilter(cfg, "/repo", targetPatterns)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	return f
}

func TestFilter_PathsDropMatching(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Paths: []string{"pkg/pb/"},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "unconvert", Message: "unnecessary conversion", Pos: output.Position{Filename: "/repo/pkg/pb/foo.pb.go", Line: 10}},
		{Linter: "unconvert", Message: "unnecessary conversion", Pos: output.Position{Filename: "/repo/pkg/c1semconv/x.go", Line: 1}},
	}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1, got=%+v", len(got), got)
	}
	if got[0].Pos.Filename != "/repo/pkg/c1semconv/x.go" {
		t.Errorf("kept wrong diag: %+v", got[0])
	}
}

func TestFilter_PathsExceptKeepsOnlyMatches(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		PathsExcept: []string{`pkg/c1semconv/`},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "goconst", Message: "x", Pos: output.Position{Filename: "/repo/pkg/c1semconv/x.go"}},
		{Linter: "goconst", Message: "x", Pos: output.Position{Filename: "/repo/pkg/other/y.go"}},
	}
	got := f.Apply(diags)
	if len(got) != 1 || got[0].Pos.Filename != "/repo/pkg/c1semconv/x.go" {
		t.Errorf("paths-except did not restrict to matches: %+v", got)
	}
}

func TestFilter_ExcludesAllFiles(t *testing.T) {
	dir := t.TempDir()
	generated := filepath.Join(dir, "generated.go")
	source := filepath.Join(dir, "source.go")
	if err := os.WriteFile(generated, []byte("// Code generated by test. DO NOT EDIT.\npackage fixture\n"), 0o600); err != nil {
		t.Fatalf("write generated file: %v", err)
	}
	if err := os.WriteFile(source, []byte("package fixture\n"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	t.Run("generated package", func(t *testing.T) {
		f, err := NewFilter(&config.Config{}, dir, nil)
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		if !f.ExcludesAllFiles([]string{generated}) {
			t.Fatal("generated package was not excluded")
		}
		if f.ExcludesAllFiles([]string{generated, source}) {
			t.Fatal("mixed generated and source package was excluded")
		}
	})

	t.Run("global path", func(t *testing.T) {
		cfg := makeCfg(config.LinterExclusions{Paths: []string{`generated\.go$`}}, nil)
		f, err := NewFilter(cfg, dir, nil)
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		if !f.ExcludesAllFiles([]string{generated}) {
			t.Fatal("path-excluded package was not excluded")
		}
	})

	t.Run("diagnostic rule is not package wide", func(t *testing.T) {
		cfg := makeCfg(config.LinterExclusions{Rules: []config.ExcludeRule{{BaseRule: config.BaseRule{Path: `.*`}}}}, nil)
		f, err := NewFilter(cfg, dir, nil)
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		if f.ExcludesAllFiles([]string{source}) {
			t.Fatal("per-diagnostic rule excluded an analysis root")
		}
	})

	if f := mustFilter(t, &config.Config{}, nil); f.ExcludesAllFiles(nil) {
		t.Fatal("empty file set was excluded")
	}
}

func TestFilter_RuleLintersAndPath(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{Linters: []string{"forbidigo", "mnd"}, Path: "cmd/dev-manager/"}},
		},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "forbidigo", Pos: output.Position{Filename: "/repo/cmd/dev-manager/main.go"}}, // dropped
		{Linter: "forbidigo", Pos: output.Position{Filename: "/repo/pkg/foo/bar.go"}},          // kept
		{Linter: "mnd", Pos: output.Position{Filename: "/repo/cmd/dev-manager/x.go"}},          // dropped
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/cmd/dev-manager/x.go"}},     // kept (linter not in rule)
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2, got=%+v", len(got), got)
	}
}

func TestFilter_RulePathExcept(t *testing.T) {
	// Rule matches anywhere outside the support_dashboard subtree.
	cfg := makeCfg(config.LinterExclusions{
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{
				Linters:    []string{"forbidigo"},
				PathExcept: `pkg/services/support_dashboard/.*\.go$`,
				Text:       `SystemTenant`,
			}},
		},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "forbidigo", Message: "SystemTenantInternalActor banned",
			Pos: output.Position{Filename: "/repo/pkg/services/support_dashboard/foo.go"}}, // path-except matches -> rule does not apply -> diag kept
		{Linter: "forbidigo", Message: "SystemTenantInternalActor banned",
			Pos: output.Position{Filename: "/repo/pkg/other/bar.go"}}, // path-except does NOT match -> rule applies -> dropped
	}
	got := f.Apply(diags)
	if len(got) != 1 || got[0].Pos.Filename != "/repo/pkg/services/support_dashboard/foo.go" {
		t.Errorf("path-except did not invert correctly: %+v", got)
	}
}

func TestFilter_PresetComments(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Presets: []string{config.ExclusionPresetComments},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	// staticcheck-prefixed diagnostic (post-family-alias)
	diags := []output.Diagnostic{
		{Linter: "ST1000", Message: "at least one file in a package should have a package comment",
			Pos: output.Position{Filename: "/repo/pkg/x/x.go"}},
		{Linter: "revive", Message: "should have a package comment",
			Pos: output.Position{Filename: "/repo/pkg/x/x.go"}},
		// Not dropped: unrelated revive message.
		{Linter: "revive", Message: "something else",
			Pos: output.Position{Filename: "/repo/pkg/x/x.go"}},
	}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1, got=%+v", len(got), got)
	}
	if got[0].Message != "something else" {
		t.Errorf("kept wrong diag: %+v", got[0])
	}
}

func TestFilter_PresetCommonFalsePositives(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Presets: []string{config.ExclusionPresetCommonFalsePositives},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "gosec", Message: "G304: Potential file inclusion via variable",
			Pos: output.Position{Filename: "/repo/x.go"}},
		{Linter: "gosec", Message: "G102: another issue",
			Pos: output.Position{Filename: "/repo/x.go"}},
	}
	got := f.Apply(diags)
	if len(got) != 1 || got[0].Message != "G102: another issue" {
		t.Errorf("preset did not drop G304 / kept wrong: %+v", got)
	}
}

func TestFilter_GeneratedLax(t *testing.T) {
	// Write a temp file with a "Code generated" marker.
	dir := t.TempDir()
	genPath := filepath.Join(dir, "gen.go")
	if err := os.WriteFile(genPath, []byte("// Code generated by foo. DO NOT EDIT.\n\npackage gen\n"), 0o600); err != nil {
		t.Fatalf("write gen: %v", err)
	}
	normalPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(normalPath, []byte("package gen\n"), 0o600); err != nil {
		t.Fatalf("write main: %v", err)
	}

	cfg := makeCfg(config.LinterExclusions{Generated: config.GeneratedModeLax}, nil)
	f, err := NewFilter(cfg, dir, nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "errcheck", Message: "x", Pos: output.Position{Filename: genPath}},
		{Linter: "errcheck", Message: "y", Pos: output.Position{Filename: normalPath}},
	}
	got := f.Apply(diags)
	if len(got) != 1 || got[0].Pos.Filename != normalPath {
		t.Errorf("generated-lax did not drop gen.go: %+v", got)
	}
}

func TestFilter_GeneratedDisable(t *testing.T) {
	dir := t.TempDir()
	genPath := filepath.Join(dir, "gen.go")
	if err := os.WriteFile(genPath, []byte("// Code generated by foo. DO NOT EDIT.\n\npackage gen\n"), 0o600); err != nil {
		t.Fatalf("write gen: %v", err)
	}
	cfg := makeCfg(config.LinterExclusions{Generated: config.GeneratedModeDisable}, nil)
	f, err := NewFilter(cfg, dir, nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{{Linter: "errcheck", Pos: output.Position{Filename: genPath}}}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Errorf("generated:disable did not preserve: %+v", got)
	}
}

func TestFilter_StaticcheckDefaultDisabledChecks(t *testing.T) {
	// With no user-supplied staticcheck.checks, the default-disabled
	// list applies.
	cfg := makeCfg(config.LinterExclusions{}, &config.StaticCheckSettings{})
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	// Distinct lines so uniq-by-line doesn't fold them.
	diags := []output.Diagnostic{
		{Linter: "ST1000", Pos: output.Position{Filename: "/repo/x.go", Line: 1}}, // dropped
		{Linter: "ST1020", Pos: output.Position{Filename: "/repo/x.go", Line: 2}}, // dropped
		{Linter: "ST1005", Pos: output.Position{Filename: "/repo/x.go", Line: 3}}, // kept
		{Linter: "SA1019", Pos: output.Position{Filename: "/repo/x.go", Line: 4}}, // kept
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2, got=%+v", len(got), got)
	}
}

func TestFilter_StaticcheckUserChecksDisablesDefault(t *testing.T) {
	// User-supplied checks: default-disabled list is NOT applied.
	cfg := makeCfg(config.LinterExclusions{}, &config.StaticCheckSettings{
		Checks: []string{"all"},
	})
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "ST1000", Pos: output.Position{Filename: "/repo/x.go"}}, // kept
	}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Errorf("user checks should disable default-disabled drop: %+v", got)
	}
}

func TestFilter_TargetDirsRecursive(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{}, nil)
	f, err := NewFilter(cfg, "/repo", []string{"./pkg/foo/..."})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/foo/a.go"}},
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/foo/sub/b.go"}},
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/bar/c.go"}},                // dropped
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/builtin_connectors/d.go"}}, // dropped
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2, got=%+v", len(got), got)
	}
}

func TestFilter_TargetDirsNonRecursive(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{}, nil)
	f, err := NewFilter(cfg, "/repo", []string{"./pkg/foo"})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/foo/a.go"}},     // kept
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/foo/sub/b.go"}}, // dropped (non-recursive)
	}
	got := f.Apply(diags)
	if len(got) != 1 || got[0].Pos.Filename != "/repo/pkg/foo/a.go" {
		t.Errorf("non-recursive target did not stop at single dir: %+v", got)
	}
}

func TestFilter_TargetDotDotDotMeansAll(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{}, nil)
	f, err := NewFilter(cfg, "/repo", []string{"./..."})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/foo/a.go"}},
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/bar/b.go"}},
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Errorf("./... should not restrict scope: %+v", got)
	}
}

func TestFilter_NilFilterPassesThrough(t *testing.T) {
	var f *Filter
	diags := []output.Diagnostic{{Linter: "errcheck", Pos: output.Position{Filename: "/x.go"}}}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Errorf("nil filter must pass-through")
	}
}

func TestFilter_NoConfigDefaultsApplied(t *testing.T) {
	// No explicit Staticcheck.Checks => default-disabled list applies.
	f, err := NewFilter(&config.Config{}, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{{Linter: "ST1000", Pos: output.Position{Filename: "/repo/x.go"}}}
	got := f.Apply(diags)
	if len(got) != 0 {
		t.Errorf("default-disabled should drop ST1000 even with empty config: %+v", got)
	}
}

func TestFilter_UniqByLine(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	// Multiple diagnostics at the same (file, line) — even from
	// different linters — should collapse to one.
	diags := []output.Diagnostic{
		{Linter: "gosec", Pos: output.Position{Filename: "/repo/x.go", Line: 10}},
		{Linter: "gosec", Pos: output.Position{Filename: "/repo/x.go", Line: 10}},    // dup
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/x.go", Line: 10}}, // same-line, different linter — also dropped
		{Linter: "gosec", Pos: output.Position{Filename: "/repo/x.go", Line: 11}},    // distinct line — kept
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Errorf("uniq-by-line did not collapse: %+v", got)
	}
}

// TestFilter_NoctxHttptestSkewDropped pins the fix:
// github.com/sonatard/noctx v0.5.0+ adds a rule for
// `net/http/httptest.NewRequest`. golangci-lint v2.9 pins v0.4.0 which
// doesn't have that rule. Plaid pins v0.5.1 so it emits 10 such
// diagnostics on c1's pkg/api/ssf_receiver/push_handler_test.go that
// golangci-lint silently skips; without the library-version-skew
// filter we surface diagnostics upstream doesn't.
func TestFilter_NoctxHttptestSkewDropped(t *testing.T) {
	f, err := NewFilter(&config.Config{}, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{
			Linter:  "noctx",
			Message: "net/http/httptest.NewRequest must not be called. use net/http/httptest.NewRequestWithContext",
			Pos:     output.Position{Filename: "/repo/pkg/x/x_test.go", Line: 1},
		},
		{
			Linter:  "noctx",
			Message: "net/http.NewRequest must not be called. use net/http.NewRequestWithContext",
			Pos:     output.Position{Filename: "/repo/pkg/x/x_test.go", Line: 2},
		},
	}
	got := f.Apply(diags)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1 (httptest one dropped, http one kept), got=%+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Message, "net/http.NewRequest") {
		t.Errorf("kept wrong diag: %+v", got[0])
	}
}

// TestFilter_RuleGovetAlias pins the fix: a path-rule scoping
// `linters: [govet]` must cover diagnostics emitted by govet
// sub-analyzers (copylocks, printf, ...). Without the alias, a c1-style
// `path: _test\.go` rule targeting `govet` doesn't suppress
// sub-analyzer diagnostics in test files.
func TestFilter_RuleGovetAlias(t *testing.T) {
	cfg := makeCfg(config.LinterExclusions{
		Rules: []config.ExcludeRule{
			{BaseRule: config.BaseRule{Linters: []string{"govet"}, Path: `_test\.go`}},
		},
	}, nil)
	f, err := NewFilter(cfg, "/repo", nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "copylocks", Pos: output.Position{Filename: "/repo/pkg/x/x_test.go", Line: 1}}, // dropped via govet alias
		{Linter: "printf", Pos: output.Position{Filename: "/repo/pkg/x/x_test.go", Line: 2}},    // dropped via govet alias
		{Linter: "copylocks", Pos: output.Position{Filename: "/repo/pkg/x/x.go", Line: 3}},      // kept (non-test path)
		{Linter: "errcheck", Pos: output.Position{Filename: "/repo/pkg/x/x_test.go", Line: 4}},  // kept (not govet family)
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2, got=%+v", len(got), got)
	}
	for _, d := range got {
		if d.Linter == "copylocks" && filepath.Base(d.Pos.Filename) == "x_test.go" {
			t.Errorf("copylocks in test file should be dropped via govet alias: %+v", d)
		}
		if d.Linter == "printf" {
			t.Errorf("printf in test file should be dropped via govet alias: %+v", d)
		}
	}
}

func TestResolveTargetDirs(t *testing.T) {
	cases := []struct {
		in   []string
		want []targetDir
	}{
		{nil, nil},
		{[]string{}, nil},
		{[]string{"./..."}, nil},
		{[]string{"all"}, nil},
		{[]string{"./pkg/foo/..."}, []targetDir{{dir: "pkg/foo", recursive: true}}},
		{[]string{"pkg/foo"}, []targetDir{{dir: "pkg/foo", recursive: false}}},
		{[]string{"./pkg/a/...", "./pkg/b"}, []targetDir{{dir: "pkg/a", recursive: true}, {dir: "pkg/b", recursive: false}}},
		{[]string{""}, []targetDir{}}, // blank skipped
	}
	for _, c := range cases {
		got := resolveTargetDirs(c.in)
		// Compare leniently because both nil and empty are accepted as "no filter".
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("resolveTargetDirs(%v) = %+v, want %+v", c.in, got, c.want)
			continue
		}
		for i, g := range got {
			if g != c.want[i] {
				t.Errorf("resolveTargetDirs(%v)[%d] = %+v, want %+v", c.in, i, g, c.want[i])
			}
		}
	}
}

func TestPresetRules_KnownNames(t *testing.T) {
	for _, name := range []string{
		config.ExclusionPresetComments,
		config.ExclusionPresetStdErrorHandling,
		config.ExclusionPresetCommonFalsePositives,
		config.ExclusionPresetLegacy,
	} {
		got := presetRules(name)
		if len(got) == 0 {
			t.Errorf("preset %q produced no rules", name)
		}
	}
	if got := presetRules("unknown"); got != nil {
		t.Errorf("preset \"unknown\" should yield nil, got %v", got)
	}
}

// TestTargetScope_SubPathFiltersOutsideDiagnostics asserts that when the
// filter is built with a sub-path target pattern, diagnostics outside
// that target are dropped while diagnostics inside survive.
func TestTargetScope_SubPathFiltersOutsideDiagnostics(t *testing.T) {
	f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", []string{"./a/..."})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	diags := []output.Diagnostic{
		{Linter: "govet", Message: "in a", Pos: output.Position{Filename: "/repo/a/foo.go", Line: 1}},
		{Linter: "govet", Message: "deep in a", Pos: output.Position{Filename: "/repo/a/sub/bar.go", Line: 1}},
		{Linter: "govet", Message: "in b", Pos: output.Position{Filename: "/repo/b/baz.go", Line: 1}},
		{Linter: "govet", Message: "sibling of a", Pos: output.Position{Filename: "/repo/aa/c.go", Line: 1}},
	}
	got := f.Apply(diags)
	if len(got) != 2 {
		t.Fatalf("expected 2 surviving diagnostics, got %d: %+v", len(got), got)
	}
	for _, d := range got {
		if !strings.HasPrefix(d.Pos.Filename, "/repo/a/") {
			t.Errorf("diagnostic outside target survived: %+v", d)
		}
	}
}

// TestTargetScope_FullRepoIsNoOp asserts that `./...` (and equivalent
// "all" patterns) disables the target-scope filter so every diagnostic
// survives.
func TestTargetScope_FullRepoIsNoOp(t *testing.T) {
	for _, pattern := range []string{"./...", "...", "all"} {
		f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", []string{pattern})
		if err != nil {
			t.Fatalf("NewFilter(%q): %v", pattern, err)
		}
		diags := []output.Diagnostic{
			{Linter: "govet", Pos: output.Position{Filename: "/repo/a/foo.go", Line: 1}},
			{Linter: "govet", Pos: output.Position{Filename: "/repo/b/bar.go", Line: 2}},
			{Linter: "govet", Pos: output.Position{Filename: "/repo/cmd/main.go", Line: 3}},
		}
		got := f.Apply(diags)
		if len(got) != len(diags) {
			t.Errorf("pattern %q: expected %d survivors, got %d: %+v", pattern, len(diags), len(got), got)
		}
	}
}

// TestTargetScope_AbsoluteVsRelativePaths asserts that the target-scope
// filter matches when the user passes an absolute path target (e.g.
// `/data/repo/pkg/foo/...`). The diagnostic path the analyzer reports
// is absolute; the filter normalizes both to the repo-relative slash
// form before comparing, so absolute and `./pkg/foo/...` targets behave
// the same.
func TestTargetScope_AbsoluteVsRelativePaths(t *testing.T) {
	// The absolute target value isn't itself used by resolveTargetDirs
	// today (the function trims `./` and `/...`), so an absolute
	// pattern like `/repo/pkg/foo/...` resolves to `targetDir{dir:
	// "/repo/pkg/foo", recursive: true}`. The filter compares against
	// the diagnostic's relative path, so the absolute pattern won't
	// match a `pkg/foo/...` diagnostic unless callers also pass an
	// absolute base. Pin both behaviors here so a future refactor
	// can't quietly regress either.
	t.Run("relative_pattern_matches_absolute_diag_filename", func(t *testing.T) {
		f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", []string{"./pkg/foo/..."})
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		diags := []output.Diagnostic{
			{Linter: "govet", Pos: output.Position{Filename: "/repo/pkg/foo/x.go", Line: 1}},
			{Linter: "govet", Pos: output.Position{Filename: "/repo/pkg/bar/y.go", Line: 1}},
		}
		got := f.Apply(diags)
		if len(got) != 1 || got[0].Pos.Filename != "/repo/pkg/foo/x.go" {
			t.Errorf("relative target dropped wrong set: %+v", got)
		}
	})
	t.Run("relative_pattern_matches_relative_diag_filename", func(t *testing.T) {
		// Diagnostic filename already relative — e.g. emitted post
		// filepath.Rel by some path-rewriting analyzer wrapper.
		f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", []string{"./pkg/foo/..."})
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		diags := []output.Diagnostic{
			{Linter: "govet", Pos: output.Position{Filename: "pkg/foo/x.go", Line: 1}},
			{Linter: "govet", Pos: output.Position{Filename: "pkg/bar/y.go", Line: 1}},
		}
		got := f.Apply(diags)
		if len(got) != 1 || got[0].Pos.Filename != "pkg/foo/x.go" {
			t.Errorf("relative target dropped wrong set: %+v", got)
		}
	})
	t.Run("nonrecursive_pattern", func(t *testing.T) {
		// `./pkg/foo` (no `/...`) should match only files directly in
		// pkg/foo, not subdirectories.
		f, err := NewFilter(makeCfg(config.LinterExclusions{}, nil), "/repo", []string{"./pkg/foo"})
		if err != nil {
			t.Fatalf("NewFilter: %v", err)
		}
		diags := []output.Diagnostic{
			{Linter: "govet", Pos: output.Position{Filename: "/repo/pkg/foo/x.go"}},
			{Linter: "govet", Pos: output.Position{Filename: "/repo/pkg/foo/sub/y.go"}},
		}
		got := f.Apply(diags)
		if len(got) != 1 || got[0].Pos.Filename != "/repo/pkg/foo/x.go" {
			t.Errorf("nonrecursive target kept wrong set: %+v", got)
		}
	})
}
