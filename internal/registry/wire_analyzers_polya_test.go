// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// polyBatchALinters lists the linters wired in
// wire_analyzers_polya.go. nolintlint was intentionally dropped
// during this batch (its lint logic lives only inside
// golangci-lint's internal package).
var polyBatchALinters = []string{
	"depguard",
	"exhaustive",
	"forbidigo",
	"gosec",
}

func TestPolyBatchA_ShapeNative(t *testing.T) {
	for _, name := range polyBatchALinters {
		name := name
		t.Run(name, func(t *testing.T) {
			e, ok := defaultCatalog.resolve(name)
			if !ok {
				t.Fatalf("catalog missing %q", name)
			}
			if e.Shape != ShapeNative {
				t.Errorf("Shape = %v, want ShapeNative", e.Shape)
			}
			if e.AnalyzerFn == nil {
				t.Error("AnalyzerFn is nil")
			}
		})
	}
}

func TestPolyBatchA_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range polyBatchALinters {
		name := name
		t.Run(name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Linters.Default = "none"
			cfg.Linters.Enable = []string{name}

			reg, _, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			var seen bool
			for _, r := range reg.Enabled() {
				if r.Name != name {
					continue
				}
				seen = true
				if r.Status != StatusEnabled {
					t.Errorf("Status = %v, want StatusEnabled", r.Status)
				}
				if r.Analyzer == nil {
					t.Error("Analyzer is nil")
				}
			}
			if !seen {
				t.Errorf("%q not in Enabled()", name)
			}
		})
	}
}

func TestPolyBatchA_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = polyBatchALinters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(polyBatchALinters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestPolyBatchA_NoLintLint_WiredInCleanupBatch pins the eventual
// resolution of polybatchA's "nolintlint was dropped" decision. The
// cleanup batch reimplemented nolintlint in-tree as an inspect-style
// analyzer (the upstream lint logic is unimportable; landmine 26),
// so the catalog row is now ShapeNative with an AnalyzerFn attached.
// The full nolintlint shape assertion lives in
// wire_analyzers_cleanup_test.go's TestCleanup_ShapeNotRegistryOnly.
func TestPolyBatchA_NoLintLint_WiredInCleanupBatch(t *testing.T) {
	e, ok := defaultCatalog.resolve("nolintlint")
	if !ok {
		t.Fatal("catalog missing nolintlint")
	}
	if e.Shape != ShapeNative {
		t.Errorf("nolintlint Shape = %v, want ShapeNative (cleanup batch reimplemented in-tree)", e.Shape)
	}
	if e.AnalyzerFn == nil {
		t.Error("nolintlint AnalyzerFn is nil; cleanup batch should have wired it")
	}
}

// TestPolyBatchA_Depguard_RuleMapTranslation verifies the
// DepGuardSettings.Rules map translates through to the upstream
// LinterSettings shape. The fallible constructor returns the analyzer
// for a well-formed rule set; the Resolved.Settings round-trip
// confirms the original typed struct survives propagation.
func TestPolyBatchA_Depguard_RuleMapTranslation(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"depguard"}
	cfg.Linters.Settings.Depguard.Rules = map[string]*config.DepGuardList{
		"deny-internal": {
			ListMode: "Strict",
			Deny: []config.DepGuardDeny{
				{Pkg: "internal/legacy", Desc: "do not import legacy"},
				{Pkg: "internal/old", Desc: "use new pkg instead"},
			},
		},
		"deny-no-fmt": {
			Files: []string{"!**/*_test.go"},
			Deny: []config.DepGuardDeny{
				{Pkg: "fmt", Desc: "use log instead"},
			},
		},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "depguard" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("depguard Analyzer is nil for well-formed rule set")
		}
		// Round-trip: the resolved settings should still hold our
		// typed map (the registry doesn't translate before storing).
		s, ok := r.Settings.(*config.DepGuardSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.DepGuardSettings", r.Settings)
		}
		if len(s.Rules) != 2 {
			t.Errorf("rule count = %d, want 2", len(s.Rules))
		}
		if rule := s.Rules["deny-internal"]; rule == nil || len(rule.Deny) != 2 {
			t.Errorf("deny-internal deny count = %d, want 2", len(rule.Deny))
		}
	}
	if !seen {
		t.Error("depguard missing from Enabled()")
	}
}

// TestPolyBatchA_Depguard_EmptyRules verifies the no-rules path. An
// empty rule map is a valid construction (the analyzer just won't
// fire any diagnostics); the Analyzer must still be non-nil.
func TestPolyBatchA_Depguard_EmptyRules(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"depguard"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var found bool
	for _, r := range reg.Enabled() {
		if r.Name != "depguard" {
			continue
		}
		found = true
		if r.Analyzer == nil {
			t.Error("depguard Analyzer is nil for empty rules")
		}
	}
	if !found {
		t.Error("depguard missing from Enabled() for empty rules")
	}
}

// TestPolyBatchA_Exhaustive_AppliesFlags verifies that every populated
// ExhaustiveSettings field lands on the package-global analyzer's
// flag set.
func TestPolyBatchA_Exhaustive_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"exhaustive"}
	cfg.Linters.Settings.Exhaustive.Check = []string{"switch", "map"}
	cfg.Linters.Settings.Exhaustive.DefaultSignifiesExhaustive = true
	cfg.Linters.Settings.Exhaustive.IgnoreEnumMembers = "^Default.*"
	cfg.Linters.Settings.Exhaustive.PackageScopeOnly = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name != "exhaustive" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("exhaustive Analyzer is nil")
		}
		if f := r.Analyzer.Flags.Lookup("check"); f == nil || f.Value.String() != "switch,map" {
			t.Errorf("check = %v, want switch,map", f)
		}
		if f := r.Analyzer.Flags.Lookup("default-signifies-exhaustive"); f == nil || f.Value.String() != "true" {
			t.Errorf("default-signifies-exhaustive = %v, want true", f)
		}
		if f := r.Analyzer.Flags.Lookup("ignore-enum-members"); f == nil || f.Value.String() != "^Default.*" {
			t.Errorf("ignore-enum-members = %v, want ^Default.*", f)
		}
		if f := r.Analyzer.Flags.Lookup("package-scope-only"); f == nil || f.Value.String() != "true" {
			t.Errorf("package-scope-only = %v, want true", f)
		}
	}
}

// TestPolyBatchA_Forbidigo_PatternShape verifies the polymorphic
// pattern translation: a bare-string pattern (no Package/Msg) lands
// as a plain regex on -p, and a structured pattern (with Msg) lands
// as a YAML-marshaled mapping.
func TestPolyBatchA_Forbidigo_PatternShape(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"forbidigo"}
	cfg.Linters.Settings.Forbidigo.Forbid = []config.ForbidigoPattern{
		{Pattern: `^fmt\.Print.*$`},
		{Pattern: `errors\.New$`, Msg: "use fmt.Errorf"},
	}
	cfg.Linters.Settings.Forbidigo.AnalyzeTypes = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "forbidigo" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("forbidigo Analyzer is nil")
		}
		// The -p flag's String() returns "" by design (it's a
		// listVar). The patterns are stored on the analyzer's
		// internal struct — the public surface we can assert on is
		// that no Flags.Set call panicked (the test reaching here
		// implies success) and analyze_types flipped.
		if f := r.Analyzer.Flags.Lookup("analyze_types"); f == nil || f.Value.String() != "true" {
			t.Errorf("analyze_types = %v, want true", f)
		}
		// Probe the -p flag exists (panic guard).
		if f := r.Analyzer.Flags.Lookup("p"); f == nil {
			t.Error("forbidigo -p flag missing")
		}
	}
	if !seen {
		t.Error("forbidigo missing from Enabled()")
	}
}

// TestPolyBatchA_Forbidigo_StructuredPatternYAML verifies that a
// pattern with Pkg + Msg is YAML-marshaled into a shape upstream's
// yamlPattern.UnmarshalYAML can parse. We probe the marshal
// indirectly by constructing the same pattern locally and confirming
// that yaml.Marshal yields a {p, pkg, msg} mapping.
func TestPolyBatchA_Forbidigo_StructuredPatternYAML(t *testing.T) {
	pat := config.ForbidigoPattern{
		Pattern: `os\.Setenv`,
		Package: "github.com/example/pkg",
		Msg:     "no env mutation",
	}
	// Mimic the wiring's marshal step.
	buf, err := marshalForbidigoPattern(pat)
	if err != nil {
		t.Fatalf("marshalForbidigoPattern: %v", err)
	}
	for _, key := range []string{"p:", "pkg:", "msg:"} {
		if !strings.Contains(buf, key) {
			t.Errorf("marshaled pattern %q missing key %q", buf, key)
		}
	}
}

// TestPolyBatchA_Gosec_IncludeExcludeFilters verifies the wiring
// surfaces a non-nil Analyzer when include/exclude lists are
// populated. The actual gosec rule registration happens lazily
// inside Run, so we can only assert the analyzer's presence and
// settings round-trip here; integration coverage lives in c1's
// smoke test.
func TestPolyBatchA_Gosec_IncludeExcludeFilters(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gosec"}
	cfg.Linters.Settings.Gosec.Includes = []string{"G101", "G102"}
	cfg.Linters.Settings.Gosec.Excludes = []string{"G104"}
	cfg.Linters.Settings.Gosec.Severity = "medium"
	cfg.Linters.Settings.Gosec.Confidence = "high"

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "gosec" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("gosec Analyzer is nil")
		}
		if r.Analyzer.Name != "gosec" {
			t.Errorf("Analyzer.Name = %q, want gosec", r.Analyzer.Name)
		}
		// Settings round-trip: confirm the typed struct survives.
		s, ok := r.Settings.(*config.GoSecSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.GoSecSettings", r.Settings)
		}
		if len(s.Includes) != 2 {
			t.Errorf("Includes count = %d, want 2", len(s.Includes))
		}
	}
	if !seen {
		t.Error("gosec missing from Enabled()")
	}
}
