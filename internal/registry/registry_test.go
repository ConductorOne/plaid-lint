// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestBuild_DefaultGroup_Standard asserts that the default group
// (empty Default) selects the v2 "standard" set: errcheck, govet,
// ineffassign, staticcheck, typecheck, unused. `unused`
// is ShapeNative (library-wrap) and surfaces as enabled.
func TestBuild_DefaultGroup_Standard(t *testing.T) {
	cfg := config.NewDefault()
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	names := map[string]bool{}
	for _, r := range reg.Enabled() {
		names[r.Name] = true
	}

	mustHave := []string{"errcheck", "govet", "ineffassign", "staticcheck", "typecheck", "unused"}
	for _, n := range mustHave {
		if !names[n] {
			t.Errorf("default group missing %q (got %v)", n, sortedKeys(names))
		}
	}

	// unused is ShapeNative (library-wrap) and must surface as
	// an Enabled row with a non-nil Analyzer attached.
	var seenUnused bool
	for _, r := range reg.All() {
		if r.Name == "unused" {
			seenUnused = true
			if r.Status != StatusEnabled {
				t.Errorf("unused: status = %v, want StatusEnabled", r.Status)
			}
			if r.Analyzer == nil {
				t.Error("unused: Analyzer must be attached")
			}
		}
	}
	if !seenUnused {
		t.Error("unused not present in registry.All()")
	}
}

// TestBuild_DefaultGroup_None disables everything.
func TestBuild_DefaultGroup_None(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := len(reg.Enabled()); got != 0 {
		names := []string{}
		for _, r := range reg.Enabled() {
			names = append(names, r.Name)
		}
		t.Errorf("none group: len(Enabled) = %d, want 0; got %v", got, names)
	}
}

// TestBuild_DefaultGroup_All activates every non-deprecated linter.
// Check via [Registry.All] (which surfaces non-runnable rows for
// diagnostic purposes) because ShapeRegistryOnly entries — the
// long-tail catalog rows whose `*analysis.Analyzer` isn't wired yet
// — don't appear in [Registry.Enabled].
func TestBuild_DefaultGroup_All(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "all"
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	active := map[string]bool{}
	for _, r := range reg.All() {
		// Disabled rows (formatters misplaced, family disabled-all) and
		// subprocess-deferred rows are out of the "all" expansion's intent.
		if r.Status == StatusDisabled {
			continue
		}
		active[r.Name] = true
	}

	for _, e := range defaultCatalog.entries() {
		if e.Shape == ShapeFormatter || e.Shape == ShapeSubprocess {
			continue
		}
		if e.Deprecated != "" {
			continue
		}
		if !active[e.Name] {
			t.Errorf("all group missing %q", e.Name)
		}
	}

	// Deprecated linters (wsl, mnd) must NOT be in `all`.
	for _, dep := range []string{"wsl", "mnd"} {
		if active[dep] {
			t.Errorf("all group included deprecated linter %q", dep)
		}
	}
}

// TestBuild_DefaultGroup_Fast restricts to fast linters.
func TestBuild_DefaultGroup_Fast(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "fast"
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	enabled := map[string]bool{}
	for _, r := range reg.Enabled() {
		enabled[r.Name] = true
	}

	// staticcheck (slow, full type info) must not be in fast.
	if enabled["staticcheck"] {
		t.Error("fast group included staticcheck (requires full type info)")
	}
	// typecheck IS fast — it's the parser surface.
	if !enabled["typecheck"] {
		t.Error("fast group missing typecheck")
	}
	// errcheck is NOT fast in upstream — it needs type info.
	if enabled["errcheck"] {
		t.Error("fast group included errcheck (requires type info)")
	}
}

// TestBuild_EnableDisable_Union runs enable + disable through resolution.
func TestBuild_EnableDisable_Union(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"errcheck", "govet", "ineffassign"}
	cfg.Linters.Disable = []string{"govet"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	names := map[string]bool{}
	for _, r := range reg.Enabled() {
		names[r.Name] = true
	}
	if !names["errcheck"] || !names["ineffassign"] {
		t.Errorf("enable did not stick: %v", sortedKeys(names))
	}
	if names["govet"] {
		t.Errorf("disable did not stick: %v", sortedKeys(names))
	}
}

// TestBuild_EnableOnly_Exclusive asserts the end-to-end behavior of
// the exclusive `--enable-only` / `--default=none --enable=` path: a
// CLI overlay that resets the default group to "none" must produce an
// active set of exactly the enabled linters, with none of the
// file-config's enabled linters leaking through.
func TestBuild_EnableOnly_Exclusive(t *testing.T) {
	// File config enables a broad set alongside default=standard.
	base := config.NewDefault()
	base.Linters.Default = "standard"
	base.Linters.Enable = []string{"bodyclose", "errorlint", "gocritic", "gosec", "misspell", "revive"}

	// Overlay mirrors applyOverlay() for `--enable-only staticcheck`.
	overlay := &config.Config{}
	overlay.Linters.Default = "none"
	overlay.Linters.Enable = []string{"staticcheck"}

	cfg := config.Merge(base, overlay)

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	names := map[string]bool{}
	for _, r := range reg.Enabled() {
		names[r.Name] = true
	}
	if !names["staticcheck"] {
		t.Errorf("staticcheck not enabled: %v", sortedKeys(names))
	}
	for _, leaked := range base.Linters.Enable {
		if names[leaked] {
			t.Errorf("file-config linter %q leaked through --enable-only: %v", leaked, sortedKeys(names))
		}
	}
}

// TestBuild_EnableAdditive_KeepsConfig is the regression guard for the
// normal additive path: a plain `--enable=X` overlay (no default
// override) must ADD X to the file-config enabled set, not replace it.
func TestBuild_EnableAdditive_KeepsConfig(t *testing.T) {
	base := config.NewDefault()
	base.Linters.Default = "standard"
	base.Linters.Enable = []string{"bodyclose", "gosec"}

	overlay := &config.Config{}
	overlay.Linters.Enable = []string{"misspell"} // plain --enable=misspell

	cfg := config.Merge(base, overlay)

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	names := map[string]bool{}
	for _, r := range reg.Enabled() {
		names[r.Name] = true
	}
	for _, want := range []string{"bodyclose", "gosec", "misspell"} {
		if !names[want] {
			t.Errorf("additive enable dropped %q: %v", want, sortedKeys(names))
		}
	}
}

// TestBuild_V1Alias_Gosimple resolves the v1 alias to staticcheck and
// emits a structured warning naming the alias.
func TestBuild_V1Alias_Gosimple(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gosimple"}

	reg, warnings, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	enabled := map[string]bool{}
	for _, r := range reg.Enabled() {
		enabled[r.Name] = true
	}
	if !enabled["staticcheck"] {
		t.Errorf("gosimple alias did not resolve to staticcheck (enabled=%v)", sortedKeys(enabled))
	}
	// stylecheck shouldn't end up enabled separately.
	if enabled["stylecheck"] {
		t.Error("stylecheck shouldn't appear as a separate enabled row")
	}

	// At least one warning naming gosimple.
	var found bool
	for _, w := range warnings {
		if strings.Contains(w.Message, `"gosimple"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning mentioned gosimple alias resolution; warnings=%v", warnings)
	}
}

// TestValidate_UnknownLinter_DidYouMean checks the registry-aware
// validation surface — the T2.1 gap closure.
func TestValidate_UnknownLinter_DidYouMean(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Enable = []string{"errchek"} // typo

	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("Validate: expected an error for typo'd linter name")
	}
	got := errs[0].Error()
	if !strings.Contains(got, "errchek") {
		t.Errorf("error doesn't name the typo: %s", got)
	}
	if !strings.Contains(got, "errcheck") {
		t.Errorf("did-you-mean missing errcheck: %s", got)
	}
}

// TestValidate_UnknownLinter_NoSuggestion verifies that wildly
// different names produce a plain "unknown linter" error without a
// misleading suggestion.
func TestValidate_UnknownLinter_NoSuggestion(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Enable = []string{"completely-fabricated-name"}

	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("Validate: expected an error for fabricated name")
	}
	got := errs[0].Error()
	if strings.Contains(got, "did you mean") {
		t.Errorf("Validate suggested something for a name with no close match: %s", got)
	}
}

// TestValidate_UnknownDefaultGroup rejects bad default values.
func TestValidate_UnknownDefaultGroup(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "everything-and-the-kitchen-sink"

	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown default group")
	}
	if !strings.Contains(errs[0].Error(), "linters.default") {
		t.Errorf("error doesn't name the offending field: %s", errs[0].Error())
	}
}

// TestPropagateGoVersion_FansOut verifies Run.Go writes into per-linter
// Go fields when blank.
func TestPropagateGoVersion_FansOut(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Run.Go = "1.22"
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet", "revive", "gocritic", "paralleltest"}

	if _, _, err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if got := cfg.Linters.Settings.Govet.Go; got != "1.22" {
		t.Errorf("Govet.Go = %q, want 1.22", got)
	}
	if got := cfg.Linters.Settings.Revive.Go; got != "1.22" {
		t.Errorf("Revive.Go = %q, want 1.22", got)
	}
	if got := cfg.Linters.Settings.Gocritic.Go; got != "1.22" {
		t.Errorf("Gocritic.Go = %q, want 1.22", got)
	}
	if got := cfg.Linters.Settings.ParallelTest.Go; got != "1.22" {
		t.Errorf("ParallelTest.Go = %q, want 1.22", got)
	}
}

// TestPropagateGoVersion_DoesNotOverwrite verifies that an explicit
// per-linter Go value wins over Run.Go.
func TestPropagateGoVersion_DoesNotOverwrite(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Run.Go = "1.22"
	cfg.Linters.Settings.Govet.Go = "1.20" // explicit
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet"}

	if _, _, err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cfg.Linters.Settings.Govet.Go; got != "1.20" {
		t.Errorf("Govet.Go = %q, want 1.20 (explicit override should win)", got)
	}
}

// TestPropagateGoVersion_GofumptUnderFormatters verifies the formatter
// path also gets fan-out.
func TestPropagateGoVersion_GofumptUnderFormatters(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Run.Go = "1.21"
	cfg.Formatters.Enable = []string{"gofumpt"}

	if _, _, err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cfg.Formatters.Settings.GoFumpt.LangVersion; got != "1.21" {
		t.Errorf("GoFumpt.LangVersion = %q, want 1.21", got)
	}
}

// TestPropagateGoVersion_OnlyEnabled verifies that linters not in the
// active set are skipped.
func TestPropagateGoVersion_OnlyEnabled(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Run.Go = "1.22"
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet"}
	// revive is not enabled.

	if _, _, err := Build(cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := cfg.Linters.Settings.Govet.Go; got != "1.22" {
		t.Errorf("Govet.Go = %q, want 1.22", got)
	}
	if got := cfg.Linters.Settings.Revive.Go; got != "" {
		t.Errorf("Revive.Go = %q, want empty (linter not enabled)", got)
	}
}

// TestConsolidateStaticcheckChecks_Dedupe verifies that duplicate
// entries in Staticcheck.Checks are dedup'd while preserving order.
func TestConsolidateStaticcheckChecks_Dedupe(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"staticcheck"}
	cfg.Linters.Settings.Staticcheck.Checks = []string{"SA1*", "ST1*", "SA1*", "QF*", "ST1*"}

	_, warnings, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := cfg.Linters.Settings.Staticcheck.Checks
	want := []string{"SA1*", "ST1*", "QF*"}
	if len(got) != len(want) {
		t.Fatalf("Checks = %v, want %v", got, want)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("Checks[%d] = %q, want %q (order should be preserved)", i, got[i], v)
		}
	}
	// Warning should have been emitted.
	var sawConsolidate bool
	for _, w := range warnings {
		if w.Field == "linters.settings.staticcheck.checks" {
			sawConsolidate = true
		}
	}
	if !sawConsolidate {
		t.Errorf("no consolidation warning emitted; warnings=%v", warnings)
	}
}

// TestStaticcheckAnalyzers_FanOut verifies that staticcheck resolves
// to many Resolved rows — one per honnef family member.
func TestStaticcheckAnalyzers_FanOut(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"staticcheck"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Count Resolved rows named "staticcheck". Should be >= 100 (95 SA + 18 ST + 35 S + 12 QF = 160).
	var count int
	for _, r := range reg.Enabled() {
		if r.Name == "staticcheck" {
			if r.Analyzer == nil {
				t.Error("staticcheck family member has nil Analyzer")
			}
			count++
		}
	}
	if count < 100 {
		t.Errorf("staticcheck fan-out: got %d rows, want at least 100 (honnef tables)", count)
	}
}

// TestGovetAnalyzers_DefaultSet verifies that govet expands to its
// vet-default sub-analyzers when no enable/disable list is given.
func TestGovetAnalyzers_DefaultSet(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	subs := map[string]bool{}
	for _, r := range reg.Enabled() {
		if r.Name == "govet" && r.Analyzer != nil {
			subs[r.Analyzer.Name] = true
		}
	}
	// printf is the canonical vet-default sub-analyzer.
	if !subs["printf"] {
		t.Errorf("govet default set missing printf; got %v", sortedKeys(subs))
	}
	// fieldalignment is opt-in (not default-on); must NOT be in the default set.
	if subs["fieldalignment"] {
		t.Error("govet default set wrongly includes fieldalignment (opt-in only)")
	}
	// shadow is opt-in too.
	if subs["shadow"] {
		t.Error("govet default set wrongly includes shadow (opt-in only)")
	}
}

// TestGovetAnalyzers_EnableAll covers `govet.enable-all: true`.
func TestGovetAnalyzers_EnableAll(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet"}
	cfg.Linters.Settings.Govet.EnableAll = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	subs := map[string]bool{}
	for _, r := range reg.Enabled() {
		if r.Name == "govet" && r.Analyzer != nil {
			subs[r.Analyzer.Name] = true
		}
	}
	// enable-all should pick up fieldalignment and shadow.
	if !subs["fieldalignment"] {
		t.Error("govet enable-all missing fieldalignment")
	}
	if !subs["shadow"] {
		t.Error("govet enable-all missing shadow")
	}
}

// TestTracecheck_NativeWired pins tracecheck's wiring post-vendor:
// the analyzer source was imported from github.com/ductone/ci-tools
// into internal/analyzers/tracecheck and registered via
// wire_analyzers_tracecheck.go. The shape flipped from ShapeSubprocess
// to ShapeNative, so an Analyzer is now attached and tracecheck
// surfaces in Enabled() when the user opts in.
func TestTracecheck_NativeWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"tracecheck"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var found bool
	for _, r := range reg.All() {
		if r.Name != "tracecheck" {
			continue
		}
		found = true
		if r.Shape != ShapeNative {
			t.Errorf("tracecheck shape = %v, want ShapeNative", r.Shape)
		}
		if r.Analyzer == nil {
			t.Errorf("tracecheck has no Analyzer attached; native wiring failed")
		}
	}
	if !found {
		t.Errorf("tracecheck missing from registry")
	}

	var enabled bool
	for _, r := range reg.Enabled() {
		if r.Name == "tracecheck" {
			enabled = true
			break
		}
	}
	if !enabled {
		t.Errorf("tracecheck not in Enabled() despite being listed in cfg.Linters.Enable")
	}
}

// TestCustomLinterPlugin verifies that linters.settings.custom adds a
// registry-only entry without crashing.
func TestCustomLinterPlugin(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Settings.Custom = map[string]config.CustomLinterSettings{
		"my-private-linter": {
			Type: "module",
		},
	}

	reg, warnings, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var found bool
	for _, r := range reg.All() {
		if r.Name == "my-private-linter" {
			found = true
			if r.Shape != ShapeRegistryOnly {
				t.Errorf("custom plugin shape = %v, want ShapeRegistryOnly", r.Shape)
			}
		}
	}
	if !found {
		t.Error("custom plugin not in registry.All()")
	}

	// Custom plugins should emit a "loaded at engine run time" warning.
	var sawCustom bool
	for _, w := range warnings {
		if strings.Contains(w.Field, "custom") {
			sawCustom = true
		}
	}
	if !sawCustom {
		t.Errorf("no custom-plugin warning emitted; warnings=%v", warnings)
	}
}

// TestFormatter_InvalidUnderLintersEnable verifies that listing a
// formatter under linters.enable does NOT crash the registry (the
// config-level validation catches it, but the registry must tolerate
// it if config.Validate is skipped).
func TestFormatter_InvalidUnderLintersEnable(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gofmt"} // formatter, not linter

	// validateAgainstCatalog accepts this; the catalog has gofmt as a
	// ShapeFormatter so resolve() finds it. T2.1's config.Validate is
	// where the rejection happens.
	if errs := Validate(cfg); len(errs) != 0 {
		t.Errorf("Validate complained about formatter-as-linter (T2.1's job, not T2.3's): %v", errs)
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// gofmt must not be in Enabled().
	for _, r := range reg.Enabled() {
		if r.Name == "gofmt" {
			t.Error("gofmt leaked into Enabled() — formatter should never enable as a linter")
		}
	}
}

// TestDeprecatedLinter_EmitsWarning verifies that enabling a
// deprecated linter produces a warning.
func TestDeprecatedLinter_EmitsWarning(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"wsl"} // deprecated alias for wsl_v5

	_, warnings, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var sawDeprecation bool
	for _, w := range warnings {
		if strings.Contains(w.Message, "deprecated") {
			sawDeprecation = true
		}
	}
	if !sawDeprecation {
		t.Errorf("no deprecation warning for wsl; warnings=%v", warnings)
	}
}

// TestPerLinterSettings_Govet verifies that a Resolved row for govet
// carries the typed *GovetSettings pointer.
func TestPerLinterSettings_Govet(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"govet"}
	cfg.Linters.Settings.Govet.Disable = []string{"printf"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var sawTyped bool
	for _, r := range reg.Enabled() {
		if r.Name == "govet" {
			gv, ok := r.Settings.(*config.GovetSettings)
			if !ok {
				t.Errorf("govet Settings type = %T, want *config.GovetSettings", r.Settings)
				continue
			}
			if len(gv.Disable) != 1 || gv.Disable[0] != "printf" {
				t.Errorf("govet Settings.Disable = %v, want [printf]", gv.Disable)
			}
			sawTyped = true
			break
		}
	}
	if !sawTyped {
		t.Error("no govet Resolved row found")
	}
	// And the printf sub-analyzer must NOT appear in govet's enabled set.
	for _, r := range reg.Enabled() {
		if r.Name == "govet" && r.Analyzer != nil && r.Analyzer.Name == "printf" {
			t.Error("printf wasn't filtered out by govet.disable")
		}
	}
}

// TestBuild_NilConfig_DoesNotCrash uses the default config when nil.
func TestBuild_NilConfig_DoesNotCrash(t *testing.T) {
	reg, _, err := Build(nil)
	if err != nil {
		t.Fatalf("Build(nil): %v", err)
	}
	if len(reg.Enabled()) == 0 {
		t.Error("Build(nil) produced empty enabled set; should default to standard")
	}
}

// TestRevive_RulesOrderPreserved confirms that the catalog/resolver
// doesn't reorder revive.rules[].
func TestRevive_RulesOrderPreserved(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"revive"}
	cfg.Linters.Settings.Revive.Rules = []config.ReviveRule{
		{Name: "var-naming"},
		{Name: "exported"},
		{Name: "context-as-argument"},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var found bool
	for _, r := range reg.All() {
		if r.Name == "revive" {
			found = true
			rv, ok := r.Settings.(*config.ReviveSettings)
			if !ok {
				t.Fatalf("revive Settings type = %T", r.Settings)
			}
			want := []string{"var-naming", "exported", "context-as-argument"}
			if len(rv.Rules) != len(want) {
				t.Fatalf("Rules len = %d, want %d", len(rv.Rules), len(want))
			}
			for i, w := range want {
				if rv.Rules[i].Name != w {
					t.Errorf("Rules[%d].Name = %q, want %q (order MUST NOT be sorted)",
						i, rv.Rules[i].Name, w)
				}
			}
		}
	}
	if !found {
		t.Error("revive missing from registry")
	}
}

// sortedKeys is a test helper.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// not sorting — caller uses for output only
	return out
}
