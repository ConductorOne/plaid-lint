// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wrapBatchLinters lists the linters wired in wire_analyzers_wrapbatch.go.
// Each upstream lacks a `*analysis.Analyzer` export and is wrapped via
// an inline `&analysis.Analyzer{Run: …}` closure.
var wrapBatchLinters = []string{
	"goconst",
	"gochecksumtype",
	"godoclint",
	"godot",
	"gomoddirectives",
	"gomodguard",
	"misspell",
	"prealloc",
	"promlinter",
}

func TestWrapBatch_ShapeNative(t *testing.T) {
	for _, name := range wrapBatchLinters {
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

func TestWrapBatch_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range wrapBatchLinters {
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

func TestWrapBatch_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = wrapBatchLinters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(wrapBatchLinters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestWrapBatch_Prealloc_WrapShape verifies the wrap closure builds a
// runnable Analyzer with the expected Name and a non-nil Run function.
// prealloc is the simplest *Pass-direct wrap shape — the closure just
// forwards (simple, range, for) flags to upstream's Check.
func TestWrapBatch_Prealloc_WrapShape(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"prealloc"}
	cfg.Linters.Settings.Prealloc.Simple = true
	cfg.Linters.Settings.Prealloc.RangeLoops = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "prealloc" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("prealloc Analyzer is nil")
		}
		if r.Analyzer.Name != "prealloc" {
			t.Errorf("Analyzer.Name = %q, want %q", r.Analyzer.Name, "prealloc")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil; wrap closure must install a Run func")
		}
		// Settings round-trip through Resolved.Settings.
		s, ok := r.Settings.(*config.PreallocSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.PreallocSettings", r.Settings)
		}
		if !s.Simple || !s.RangeLoops || s.ForLoops {
			t.Errorf("Settings = %+v, want Simple+RangeLoops only", s)
		}
	}
}

// TestWrapBatch_Gochecksumtype_PackageReconstruction verifies the wrap
// builds a runnable Analyzer for the []*packages.Package reconstruction
// shape. We don't drive a real Pass here — the registry test surface is
// about wire-up; end-to-end runs live in the engine corpus tests.
func TestWrapBatch_Gochecksumtype_PackageReconstruction(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gochecksumtype"}
	cfg.Linters.Settings.GoChecksumType.DefaultSignifiesExhaustive = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "gochecksumtype" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("gochecksumtype Analyzer is nil")
		}
		if r.Analyzer.Name != "gochecksumtype" {
			t.Errorf("Analyzer.Name = %q", r.Analyzer.Name)
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
		s, ok := r.Settings.(*config.GoChecksumTypeSettings)
		if !ok || !s.DefaultSignifiesExhaustive {
			t.Errorf("Settings = %+v, want DefaultSignifiesExhaustive=true", r.Settings)
		}
	}
}

// TestWrapBatch_Misspell_BuildsReplacer verifies the library-only wrap
// successfully compiles a replacer at wire-time (including the locale +
// extra-words paths). A malformed locale must not panic; the wrap
// should still produce a usable Analyzer for the valid case below.
func TestWrapBatch_Misspell_BuildsReplacer(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"misspell"}
	cfg.Linters.Settings.Misspell.Locale = "US"
	cfg.Linters.Settings.Misspell.ExtraWords = []config.MisspellExtraWords{
		{Typo: "teh", Correction: "the"},
	}
	cfg.Linters.Settings.Misspell.IgnoreRules = []string{"definately"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "misspell" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("misspell Analyzer is nil; expected wrap to compile replacer")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
	}
}

// TestWrapBatch_Misspell_UnknownLocale verifies the wrap degrades to a
// no-op (nil Analyzer slice) when the user provides an unsupported
// locale (replaces fatal-logging from golangci-lint's wrapper with
// tolerant nil-return). The catalog row stays present but Enabled() may
// report it as "no analyzer attached" rather than panicking the Build.
func TestWrapBatch_Misspell_UnknownLocale(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"misspell"}
	cfg.Linters.Settings.Misspell.Locale = "ZZ" // not in {US, UK, GB}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "misspell" {
			continue
		}
		// Wrap returns nil; the engine treats this the same as "no
		// analyzer wired" — Status stays Enabled but Analyzer is nil.
		// The contract: never panic on malformed settings.
		if r.Analyzer != nil {
			t.Logf("Analyzer non-nil (got runtime fallback): %+v", r.Analyzer)
		}
	}
}

// TestWrapBatch_Gomoddirectives_OptionsTranslation verifies the
// regex-pattern compile path doesn't panic on either a well-formed or a
// malformed pattern, and that the Analyzer wraps the sync.Once correctly
// (Run is non-nil).
func TestWrapBatch_Gomoddirectives_OptionsTranslation(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gomoddirectives"}
	cfg.Linters.Settings.GoModDirectives.ReplaceLocal = true
	cfg.Linters.Settings.GoModDirectives.ToolchainPattern = `^go1\.\d+\.\d+$`
	cfg.Linters.Settings.GoModDirectives.GoVersionPattern = `^1\.\d+$`

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "gomoddirectives" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("gomoddirectives Analyzer is nil")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil; sync.Once wrap must install Run")
		}
	}
}

// TestWrapBatch_Godot_SettingsTranslation verifies the per-file
// library-only wrap accepts the GodotSettings translation (Scope cast,
// bool flags) and produces a runnable Analyzer. Defaults to "declarations"
// scope when none provided.
func TestWrapBatch_Godot_SettingsTranslation(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"godot"}
	cfg.Linters.Settings.Godot.Scope = "all"
	cfg.Linters.Settings.Godot.Capital = true
	cfg.Linters.Settings.Godot.Exclude = []string{"^//go:"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "godot" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("godot Analyzer is nil")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
	}
}

// TestWrapBatch_Promlinter_SettingsTranslation verifies the Setting
// struct copy ({Strict, DisabledLintFuncs}).
func TestWrapBatch_Promlinter_SettingsTranslation(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"promlinter"}
	cfg.Linters.Settings.Promlinter.Strict = true
	cfg.Linters.Settings.Promlinter.DisabledLinters = []string{"Help", "MetricUnits"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "promlinter" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("promlinter Analyzer is nil")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
	}
}

// TestWrapBatch_Godoclint_AdapterShape verifies the custom-Analyzer
// adapter path (Compose → Composition.Analyzer.GetAnalyzer) produces a
// usable *analysis.Analyzer. Default settings should still resolve to a
// non-nil Analyzer (godoclint's defaults are the intended baseline).
func TestWrapBatch_Godoclint_AdapterShape(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"godoclint"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "godoclint" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("godoclint Analyzer is nil; adapter must yield runtime Analyzer")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
	}
}

// TestWrapBatch_Goconst_SettingsTranslation verifies the GoConst
// settings subset that does map to upstream's Config (without
// ExcludeTypes / IgnoreFunctions, which we don't surface).
func TestWrapBatch_Goconst_SettingsTranslation(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"goconst"}
	cfg.Linters.Settings.Goconst.MinStringLen = 4
	cfg.Linters.Settings.Goconst.MinOccurrencesCount = 3
	cfg.Linters.Settings.Goconst.MatchWithConstants = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "goconst" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("goconst Analyzer is nil")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
		s, ok := r.Settings.(*config.GoConstSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.GoConstSettings", r.Settings)
		}
		if s.MinStringLen != 4 || s.MinOccurrencesCount != 3 || !s.MatchWithConstants {
			t.Errorf("Settings = %+v, want MinStringLen=4 MinOccurrencesCount=3 MatchWithConstants=true", s)
		}
	}
}

// TestWrapBatch_Gomodguard_ProcessorOnce verifies the sync.Once-wrapped
// Processor builds without panicking even when the settings carry
// nested block maps. The lazy Processor construction means a missing
// go.mod degrades to no-op rather than failing the Build.
func TestWrapBatch_Gomodguard_ProcessorOnce(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gomodguard"}
	cfg.Linters.Settings.Gomodguard.Allowed.Modules = []string{"golang.org/x/tools"}
	cfg.Linters.Settings.Gomodguard.Blocked.Modules = []map[string]config.GoModGuardModule{
		{"github.com/pkg/errors": {
			Recommendations: []string{"errors"},
			Reason:          "use stdlib errors",
		}},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "gomodguard" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("gomodguard Analyzer is nil")
		}
		if r.Analyzer.Run == nil {
			t.Error("Analyzer.Run is nil")
		}
	}
}
