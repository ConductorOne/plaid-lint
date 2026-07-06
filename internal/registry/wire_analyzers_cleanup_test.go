// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/conductorone/plaid-lint/internal/config"
)

// cleanupLinters enumerates the entries the cleanup batch wired. The
// in-tree nolintlint analyzer plus the modernize family and the
// upstream-wired unqueryvet. Iotamixing is deliberately absent — it
// stays ShapeRegistryOnly.
var cleanupLinters = []string{
	"modernize",
	"nolintlint",
	"unqueryvet",
}

// TestCleanup_ShapeNotRegistryOnly asserts every cleanup row was
// flipped off ShapeRegistryOnly. The expected post-batch shape varies
// by row — modernize is a family fan-out, nolintlint and unqueryvet
// are single-analyzer natives.
func TestCleanup_ShapeNotRegistryOnly(t *testing.T) {
	want := map[string]Shape{
		"modernize":  ShapeNativeFamily,
		"nolintlint": ShapeNative,
		"unqueryvet": ShapeNative,
	}
	for name, w := range want {
		name, w := name, w
		t.Run(name, func(t *testing.T) {
			e, ok := defaultCatalog.resolve(name)
			if !ok {
				t.Fatalf("catalog missing %q", name)
			}
			if e.Shape != w {
				t.Errorf("Shape = %v, want %v", e.Shape, w)
			}
			if e.AnalyzerFn == nil {
				t.Error("AnalyzerFn is nil")
			}
		})
	}
}

// TestCleanup_Iotamixing_StaysRegistryOnly pins iotamixing's
// "deliberately skipped" status. If a future contributor wires it,
// they need to update this test to flip the expected shape — that's
// the intended speed bump.
func TestCleanup_Iotamixing_StaysRegistryOnly(t *testing.T) {
	e, ok := defaultCatalog.resolve("iotamixing")
	if !ok {
		t.Fatal("catalog missing iotamixing")
	}
	if e.Shape != ShapeRegistryOnly {
		t.Errorf("Shape = %v, want ShapeRegistryOnly (iotamixing has no upstream module — see seed.go)", e.Shape)
	}
	if e.AnalyzerFn != nil {
		t.Error("AnalyzerFn is non-nil; iotamixing should not be wired (no upstream module)")
	}
}

// TestCleanup_Enabled_ProducesAnalyzer asserts enabling each cleanup
// linter under `linters.default=none` lands a Resolved row with
// Status=StatusEnabled and a non-nil Analyzer.
func TestCleanup_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range cleanupLinters {
		name := name
		t.Run(name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Linters.Default = "none"
			cfg.Linters.Enable = []string{name}

			reg, _, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			var seen, withAnalyzer int
			for _, r := range reg.Enabled() {
				if r.Name != name {
					continue
				}
				seen++
				if r.Status != StatusEnabled {
					t.Errorf("Status = %v, want StatusEnabled", r.Status)
				}
				if r.Analyzer != nil {
					withAnalyzer++
				}
			}
			if seen == 0 {
				t.Errorf("%q not in Enabled()", name)
			}
			if withAnalyzer == 0 {
				t.Errorf("%q: no Resolved row carried a non-nil Analyzer", name)
			}
		})
	}
}

// TestCleanup_NoWarnings_NoAnalyzerWired confirms the Enabled set no
// longer surfaces the StatusNoAnalyzerWired Reason for these three —
// the gap closure this batch targets.
func TestCleanup_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = cleanupLinters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(cleanupLinters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestCleanup_LongTailComplete is the "final batch" assertion: every
// non-deprecated catalog row whose Shape is ShapeRegistryOnly must be
// iotamixing (the documented hold-out). If any other row is still
// ShapeRegistryOnly, a future contributor either added a new linter
// without wiring it OR a previously-wired entry regressed.
func TestCleanup_LongTailComplete(t *testing.T) {
	allowed := map[string]bool{
		"iotamixing": true, // no upstream module
	}
	var remaining []string
	for _, e := range defaultCatalog.entries() {
		if e.Shape != ShapeRegistryOnly {
			continue
		}
		if e.Deprecated != "" {
			continue
		}
		if allowed[e.Name] {
			continue
		}
		remaining = append(remaining, e.Name)
	}
	if len(remaining) > 0 {
		t.Errorf("ShapeRegistryOnly hold-outs: %v (expected only iotamixing). Wire them or add to the allow-list with a documented reason.", remaining)
	}
}

// TestCleanup_Modernize_FansOutAcrossSuite asserts modernize produces
// one Resolved row per Suite member (22 today). Disabling members via
// settings should reduce the count.
func TestCleanup_Modernize_FansOutAcrossSuite(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"modernize"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var rows int
	for _, r := range reg.Enabled() {
		if r.Name != "modernize" {
			continue
		}
		rows++
		if r.Analyzer == nil {
			t.Error("modernize Resolved with nil Analyzer")
		}
	}
	if rows != len(modernize.Suite) {
		t.Errorf("modernize rows = %d, want %d (one per Suite member)", rows, len(modernize.Suite))
	}
}

// TestCleanup_Modernize_DisableSkipsSuiteMembers verifies the
// `modernize.disable` setting filters out specific Suite members.
func TestCleanup_Modernize_DisableSkipsSuiteMembers(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"modernize"}
	// Pick two well-known Suite members; if the Suite ever drops these,
	// the assertion still works (Build skips unknown disable names).
	cfg.Linters.Settings.Modernize.Disable = []string{"minmax", "rangeint"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var rows int
	for _, r := range reg.Enabled() {
		if r.Name != "modernize" {
			continue
		}
		rows++
		if r.Analyzer == nil {
			continue
		}
		if r.Analyzer.Name == "minmax" || r.Analyzer.Name == "rangeint" {
			t.Errorf("disabled member %q survived the filter", r.Analyzer.Name)
		}
	}
	if rows != len(modernize.Suite)-2 {
		t.Errorf("modernize rows = %d, want %d (Suite minus two disabled)", rows, len(modernize.Suite)-2)
	}
}

// TestCleanup_Unqueryvet_ZeroSettings_UsesDefault asserts that an
// empty UnqueryvetSettings keeps upstream's `DefaultSettings` in
// effect — the path the c1's "enable but don't configure" use-case
// hits. We can't introspect upstream's defaults directly, but the
// Resolved Analyzer is non-nil and `.Name` matches the upstream
// canonical name.
func TestCleanup_Unqueryvet_ZeroSettings_UsesDefault(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"unqueryvet"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "unqueryvet" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("unqueryvet Analyzer is nil")
		}
	}
	if !seen {
		t.Error("unqueryvet missing from Enabled()")
	}
}

// TestCleanup_Unqueryvet_TranslatesSettings asserts the translator
// round-trips our typed UnqueryvetSettings into upstream's config
// shape. The Resolved Analyzer is non-nil and distinct from the
// default-config instance — distinctness is enforced because
// NewWithConfig constructs a fresh Analyzer each call.
func TestCleanup_Unqueryvet_TranslatesSettings(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"unqueryvet"}
	cfg.Linters.Settings.Unqueryvet = config.UnqueryvetSettings{
		CheckSQLBuilders:     true,
		AllowedPatterns:      []string{`SELECT \* FROM tmp_.*`},
		IgnoredFunctions:     []string{"debug.Query"},
		CheckAliasedWildcard: true,
		CustomRules: []config.UnqueryvetCustomRule{
			{ID: "no-temp", Pattern: "SELECT * FROM temp_*", Message: "use explicit columns"},
		},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "unqueryvet" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("unqueryvet Analyzer is nil")
		}
		// Settings round-trip — registry stores the typed struct.
		s, ok := r.Settings.(*config.UnqueryvetSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.UnqueryvetSettings", r.Settings)
		}
		if !s.CheckSQLBuilders || !s.CheckAliasedWildcard {
			t.Errorf("Settings bools lost: %+v", s)
		}
		if len(s.CustomRules) != 1 || s.CustomRules[0].ID != "no-temp" {
			t.Errorf("CustomRules round-trip mismatch: %+v", s.CustomRules)
		}
	}
	if !seen {
		t.Error("unqueryvet missing from Enabled()")
	}
}

// TestCleanup_Unqueryvet_TranslateConfig directly exercises the
// translator helper. Verifies field-by-field copying against a fully
// populated UnqueryvetSettings.
func TestCleanup_Unqueryvet_TranslateConfig(t *testing.T) {
	in := &config.UnqueryvetSettings{
		CheckSQLBuilders:     true,
		AllowedPatterns:      []string{"a", "b"},
		IgnoredFunctions:     []string{"x"},
		CheckAliasedWildcard: true,
		CheckStringConcat:    true,
		CheckFormatStrings:   true,
		CheckStringBuilder:   true,
		CheckSubqueries:      true,
		CheckN1:              true,
		CheckSQLInjection:    true,
		CheckTxLeak:          true,
		Allow:                []string{"y"},
		SQLBuilders: config.UnqueryvetSQLBuildersSettings{
			Squirrel: true, GORM: true, SQLx: true, Ent: true,
			PGX: true, Bun: true, SQLBoiler: true, Jet: true,
		},
		CustomRules: []config.UnqueryvetCustomRule{
			{ID: "r1", Pattern: "p1", Patterns: []string{"alt"}, When: "in_loop", Message: "no", Action: "report"},
		},
	}
	out := translateUnqueryvetConfig(in)

	if !out.CheckSQLBuilders || !out.CheckAliasedWildcard || !out.CheckStringConcat ||
		!out.CheckFormatStrings || !out.CheckStringBuilder || !out.CheckSubqueries ||
		!out.N1DetectionEnabled || !out.SQLInjectionDetectionEnabled || !out.TxLeakDetectionEnabled {
		t.Errorf("bool fields not preserved: %+v", out)
	}
	if !out.SQLBuilders.Squirrel || !out.SQLBuilders.Jet {
		t.Errorf("SQLBuilders bools lost: %+v", out.SQLBuilders)
	}
	if len(out.AllowedPatterns) != 2 || len(out.IgnoredFunctions) != 1 || len(out.Allow) != 1 {
		t.Errorf("string slices not preserved: AllowedPatterns=%v IgnoredFunctions=%v Allow=%v",
			out.AllowedPatterns, out.IgnoredFunctions, out.Allow)
	}
	if len(out.CustomRules) != 1 || out.CustomRules[0].ID != "r1" ||
		out.CustomRules[0].Pattern != "p1" || out.CustomRules[0].Action != "report" {
		t.Errorf("CustomRules lost: %+v", out.CustomRules)
	}
}

// TestCleanup_Unqueryvet_IsZero confirms isUnqueryvetZero matches the
// expected boundary cases — bare zero value is "zero", any single
// non-zero scalar / slice / nested field is "non-zero".
func TestCleanup_Unqueryvet_IsZero(t *testing.T) {
	if !isUnqueryvetZero(&config.UnqueryvetSettings{}) {
		t.Error("bare zero UnqueryvetSettings: isUnqueryvetZero = false")
	}
	if isUnqueryvetZero(&config.UnqueryvetSettings{CheckSQLBuilders: true}) {
		t.Error("CheckSQLBuilders=true: isUnqueryvetZero = true")
	}
	if isUnqueryvetZero(&config.UnqueryvetSettings{AllowedPatterns: []string{"x"}}) {
		t.Error("AllowedPatterns set: isUnqueryvetZero = true")
	}
	if isUnqueryvetZero(&config.UnqueryvetSettings{
		SQLBuilders: config.UnqueryvetSQLBuildersSettings{GORM: true},
	}) {
		t.Error("SQLBuilders.GORM=true: isUnqueryvetZero = true")
	}
}
