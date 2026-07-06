// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	revivelint "github.com/mgechev/revive/lint"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestRevive_ShapeNative verifies the catalog entry was flipped to
// ShapeNative and an AnalyzerFn is wired.
func TestRevive_ShapeNative(t *testing.T) {
	e, ok := defaultCatalog.resolve("revive")
	if !ok {
		t.Fatal("catalog missing revive")
	}
	if e.Shape != ShapeNative {
		t.Errorf("Shape = %v, want ShapeNative", e.Shape)
	}
	if e.AnalyzerFn == nil {
		t.Error("AnalyzerFn is nil")
	}
}

// TestRevive_Enabled_ProducesAnalyzer verifies the typical wire flow:
// `linters.enable: [revive]` resolves to StatusEnabled with a non-nil
// Analyzer. No settings → revive's default rule set applies.
func TestRevive_Enabled_ProducesAnalyzer(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"revive"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "revive" {
			continue
		}
		seen = true
		if r.Status != StatusEnabled {
			t.Errorf("Status = %v, want StatusEnabled", r.Status)
		}
		if r.Analyzer == nil {
			t.Error("Analyzer is nil")
		}
		if r.Analyzer != nil && r.Analyzer.Name != "revive" {
			t.Errorf("Analyzer.Name = %q, want %q", r.Analyzer.Name, "revive")
		}
	}
	if !seen {
		t.Error("revive missing from Enabled()")
	}
}

// TestRevive_NoNoAnalyzerWired_Regression guards against a seed/wiring
// mismatch — flipping the shape but forgetting the AnalyzerFn would
// produce StatusNoAnalyzerWired here.
func TestRevive_NoNoAnalyzerWired_Regression(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"revive"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if r.Name != "revive" {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("revive still StatusNoAnalyzerWired (reason=%q)", r.Reason)
		}
	}
}

// TestRevive_DefaultRuleSet_Applied verifies that with zero settings the
// translator opts into revive's default-on rule list (golint's
// historical defaults). The translator's contract: empty Rules slice +
// no enable-all/enable-default-rules → set EnableDefaultRules=true and
// populate conf.Rules with the 23 default-rule names so
// reviveconfig.GetLintingRules materializes them.
func TestRevive_DefaultRuleSet_Applied(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	if !conf.EnableDefaultRules {
		t.Error("EnableDefaultRules = false, want true (empty settings → default rule set)")
	}
	if conf.Confidence != reviveDefaultConfidence {
		t.Errorf("Confidence = %v, want %v (default)", conf.Confidence, reviveDefaultConfidence)
	}
	if conf.Severity != revivelint.SeverityWarning {
		t.Errorf("Severity = %q, want %q (default)", conf.Severity, revivelint.SeverityWarning)
	}
	// Spot-check a few names that should be in the default-on map. If
	// reviveDefaultRules drifts versus upstream, these will fail loud
	// (landmine 30).
	for _, want := range []string{"exported", "var-naming", "package-comments"} {
		if _, ok := conf.Rules[want]; !ok {
			t.Errorf("default-rule %q missing from conf.Rules", want)
		}
	}
	if len(conf.Rules) != len(reviveDefaultRules) {
		t.Errorf("conf.Rules length = %d, want %d (mirror parity)", len(conf.Rules), len(reviveDefaultRules))
	}
}

// TestRevive_ExplicitEnable_NoDefault verifies that supplying an
// explicit rule list prevents the empty-settings default-rule-set
// shortcut from kicking in. The user's rules become the active set.
func TestRevive_ExplicitEnable_NoDefault(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Rules: []config.ReviveRule{
			{Name: "var-naming"},
			{Name: "exported"},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	if conf.EnableDefaultRules {
		t.Error("EnableDefaultRules = true with explicit Rules; should remain false")
	}
	if _, ok := conf.Rules["var-naming"]; !ok {
		t.Error(`Rules missing "var-naming"`)
	}
	if _, ok := conf.Rules["exported"]; !ok {
		t.Error(`Rules missing "exported"`)
	}
}

// TestRevive_DisabledRule verifies that ReviveRule.Disabled threads
// through to lint.RuleConfig.Disabled. This is how a user turns off one
// rule from the default-on set.
func TestRevive_DisabledRule(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Rules: []config.ReviveRule{
			{Name: "exported", Disabled: true},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	rc, ok := conf.Rules["exported"]
	if !ok {
		t.Fatal(`Rules missing "exported"`)
	}
	if !rc.Disabled {
		t.Error("Disabled = false, want true")
	}
}

// TestRevive_PerRuleSeverity verifies per-rule severity override. Top
// level severity propagates to rules that don't specify their own.
func TestRevive_PerRuleSeverity(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Severity: "error",
		Rules: []config.ReviveRule{
			{Name: "exported"},
			{Name: "var-naming", Severity: "warning"},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	if got := conf.Rules["exported"].Severity; got != "error" {
		t.Errorf(`exported severity = %q, want "error" (inherited)`, got)
	}
	if got := conf.Rules["var-naming"].Severity; got != "warning" {
		t.Errorf(`var-naming severity = %q, want "warning" (override)`, got)
	}
}

// TestRevive_Arguments_RoundTrip verifies the load-bearing case — per-
// rule Arguments threading. We pick line-length-limit which takes a
// single int64 argument; the upstream Configure validates and stores
// the limit. The argument should survive translation byte-for-byte
// (revive does its own type assertion on the []any).
func TestRevive_Arguments_RoundTrip(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Rules: []config.ReviveRule{
			{Name: "line-length-limit", Arguments: []any{int64(120)}},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	rc, ok := conf.Rules["line-length-limit"]
	if !ok {
		t.Fatal(`Rules missing "line-length-limit"`)
	}
	if len(rc.Arguments) != 1 {
		t.Fatalf("Arguments length = %d, want 1", len(rc.Arguments))
	}
	got, ok := rc.Arguments[0].(int64)
	if !ok {
		t.Fatalf("Arguments[0] type = %T, want int64", rc.Arguments[0])
	}
	if got != 120 {
		t.Errorf("Arguments[0] = %d, want 120", got)
	}
}

// TestRevive_Arguments_MapAny_Normalized verifies the landmine-28
// defensive case: if a yaml.v2-shaped argument (map[any]any) sneaks in,
// the normalizer converts it to map[string]any (the shape revive's
// rules expect). This is also exercised by the file-length-limit rule
// upstream — it asserts arguments[0].(map[string]any).
func TestRevive_Arguments_MapAny_Normalized(t *testing.T) {
	// Use map[any]any as the input — the wire layer should flip it to
	// map[string]any before handing to revive.
	arg := map[any]any{
		"max":          int64(500),
		"skipComments": true,
	}
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Rules: []config.ReviveRule{
			{Name: "file-length-limit", Arguments: []any{arg}},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	rc, ok := conf.Rules["file-length-limit"]
	if !ok {
		t.Fatal(`Rules missing "file-length-limit"`)
	}
	if len(rc.Arguments) != 1 {
		t.Fatalf("Arguments length = %d, want 1", len(rc.Arguments))
	}
	got, ok := rc.Arguments[0].(map[string]any)
	if !ok {
		t.Fatalf("Arguments[0] type = %T, want map[string]any (post-normalize)", rc.Arguments[0])
	}
	if got["max"] != int64(500) {
		t.Errorf("max = %v, want 500", got["max"])
	}
	if got["skipComments"] != true {
		t.Errorf("skipComments = %v, want true", got["skipComments"])
	}
}

// TestRevive_Directives_Translated verifies the directives slice
// round-trips into lint.Config.Directives.
func TestRevive_Directives_Translated(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Directives: []config.ReviveDirective{
			{Name: "specify-disable-reason", Severity: "error"},
		},
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	d, ok := conf.Directives["specify-disable-reason"]
	if !ok {
		t.Fatal(`Directives missing "specify-disable-reason"`)
	}
	if d.Severity != "error" {
		t.Errorf("directive severity = %q, want %q", d.Severity, "error")
	}
}

// TestRevive_GoVersion_Parsed verifies the Go string field translates
// to *goversion.Version.
func TestRevive_GoVersion_Parsed(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		Go: "1.21",
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	if conf.GoVersion == nil {
		t.Fatal("GoVersion is nil")
	}
	if got := conf.GoVersion.String(); got != "1.21.0" {
		t.Errorf("GoVersion = %q, want %q", got, "1.21.0")
	}
}

// TestRevive_EnableAllRules verifies that EnableAllRules survives
// translation and conf.Rules gets populated with every entry from the
// reviveAllRules mirror table. GetLintingRules walks conf.Rules to
// pick the active set, so the bool alone is not enough.
func TestRevive_EnableAllRules(t *testing.T) {
	conf, err := translateReviveConfig(&config.ReviveSettings{
		EnableAllRules: true,
	})
	if err != nil {
		t.Fatalf("translateReviveConfig: %v", err)
	}
	if !conf.EnableAllRules {
		t.Error("EnableAllRules = false, want true")
	}
	// Default-rule shortcut must NOT fire when EnableAllRules is set.
	if conf.EnableDefaultRules {
		t.Error("EnableDefaultRules should not flip on when EnableAllRules is set")
	}
	if len(conf.Rules) != len(reviveAllRules) {
		t.Errorf("conf.Rules length = %d, want %d (mirror parity)", len(conf.Rules), len(reviveAllRules))
	}
	// Spot-check an opt-in rule that's NOT in the default-23 list.
	if _, ok := conf.Rules["line-length-limit"]; !ok {
		t.Error(`opt-in rule "line-length-limit" missing under EnableAllRules`)
	}
}

// TestRevive_NormalizeArgValue_Nested verifies that nested map[any]any
// inside a list or map gets recursively normalized to map[string]any.
// This protects against malformed yaml.v2-style inputs anywhere in the
// argument tree.
func TestRevive_NormalizeArgValue_Nested(t *testing.T) {
	in := []any{
		map[any]any{
			"outer": []any{
				map[any]any{"k": "v"},
			},
		},
	}
	out := normalizeReviveArguments(in)
	if len(out) != 1 {
		t.Fatalf("out length = %d, want 1", len(out))
	}
	top, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("out[0] type = %T, want map[string]any", out[0])
	}
	inner, ok := top["outer"].([]any)
	if !ok {
		t.Fatalf("outer type = %T, want []any", top["outer"])
	}
	if _, ok := inner[0].(map[string]any); !ok {
		t.Errorf("nested[0] type = %T, want map[string]any", inner[0])
	}
}

// TestRevive_BuildAnalyzerRuns smoke-tests the closure end-to-end via
// the Build path: with `linters.enable: [revive]` and a real
// settings struct, the resolved Analyzer is non-nil and carries the
// upstream Name. The closure's setup runs lazily on the first Run call,
// so we don't exercise that here — see the Lint engine integration
// test if c1 smoke surfaces a per-pass bug.
func TestRevive_BuildAnalyzerRuns(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"revive"}
	cfg.Linters.Settings.Revive = config.ReviveSettings{
		Confidence: 0.5,
		Severity:   "warning",
		Rules: []config.ReviveRule{
			{Name: "line-length-limit", Arguments: []any{int64(100)}},
		},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "revive" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("revive Analyzer is nil")
		}
		if r.Analyzer.Name != "revive" {
			t.Errorf("Analyzer.Name = %q, want %q", r.Analyzer.Name, "revive")
		}
		// Settings round-trip — the typed struct should survive
		// propagation untouched (registry doesn't translate before
		// storing — translation happens lazily inside the closure).
		s, ok := r.Settings.(*config.ReviveSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.ReviveSettings", r.Settings)
		}
		if len(s.Rules) != 1 || s.Rules[0].Name != "line-length-limit" {
			t.Errorf("Settings.Rules round-trip mismatch: %+v", s.Rules)
		}
	}
}
