// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	gocriticlinter "github.com/go-critic/go-critic/linter"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestGocritic_Catalog_Shape verifies the seed flip landed:
// gocritic is ShapeNative and has an AnalyzerFn attached.
func TestGocritic_Catalog_Shape(t *testing.T) {
	e, ok := defaultCatalog.resolve("gocritic")
	if !ok {
		t.Fatal("catalog missing gocritic")
	}
	if e.Shape != ShapeNative {
		t.Errorf("Shape = %v, want ShapeNative", e.Shape)
	}
	if e.AnalyzerFn == nil {
		t.Error("AnalyzerFn is nil")
	}
	if !e.HasGoVersion {
		t.Error("HasGoVersion = false, want true (gocritic surfaces Settings.Go)")
	}
}

// TestGocritic_Default_ProducesAnalyzer verifies the no-settings
// path: enabling gocritic with no SettingsPerCheck / EnabledChecks /
// tags yields a non-nil analyzer with the upstream default-on set
// (no experimental / opinionated / performance / security tags).
func TestGocritic_Default_ProducesAnalyzer(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gocritic"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "gocritic" {
			continue
		}
		seen = true
		if r.Status != StatusEnabled {
			t.Errorf("Status = %v, want StatusEnabled", r.Status)
		}
		if r.Analyzer == nil {
			t.Fatal("Analyzer is nil")
		}
		if r.Analyzer.Name != "gocritic" {
			t.Errorf("Analyzer.Name = %q, want gocritic", r.Analyzer.Name)
		}
	}
	if !seen {
		t.Error("gocritic not in Enabled()")
	}
}

// TestGocritic_NoWarnings_NoAnalyzerWired guards against a catalog
// shape <-> wire mismatch.
func TestGocritic_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gocritic"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if r.Name != "gocritic" {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("gocritic StatusNoAnalyzerWired (reason=%q)", r.Reason)
		}
	}
}

// TestGocritic_DefaultEnabledSet verifies the default-enabled
// inference matches upstream's isEnabledByDefaultGoCriticChecker:
// the experimental/opinionated/performance/security tag set is
// excluded, diagnostic+style is included.
func TestGocritic_DefaultEnabledSet(t *testing.T) {
	enabled := gocriticEnabledChecks(nil)
	if len(enabled) == 0 {
		t.Fatal("default-enabled set is empty (registration broken?)")
	}
	// Walk all checks; any with a non-default tag must be excluded;
	// any with no non-default tag must be included.
	for _, info := range gocriticlinter.GetCheckersInfo() {
		isDefault := isEnabledByDefaultGocritic(info)
		if isDefault && !enabled[info.Name] {
			t.Errorf("default-on check %q missing from default set", info.Name)
		}
		if !isDefault && enabled[info.Name] {
			t.Errorf("non-default check %q present in default set", info.Name)
		}
	}
}

// TestGocritic_ExplicitEnableList verifies EnabledChecks adds checks
// on top of the default base — including checks that wouldn't be on
// by default (e.g. a `performance`-tagged check).
func TestGocritic_ExplicitEnableList(t *testing.T) {
	s := &config.GoCriticSettings{
		EnabledChecks: []string{"hugeParam"}, // performance-tagged, off by default
	}
	enabled := gocriticEnabledChecks(s)
	if !enabled["hugeParam"] {
		t.Error("EnabledChecks did not add hugeParam")
	}
	// appendAssign is purely diagnostic-tagged so it's still in the
	// default set — explicit enable does not erase the default base.
	if !enabled["appendAssign"] {
		t.Error("EnabledChecks erased default set; expected appendAssign still on")
	}
}

// TestGocritic_ExplicitDisableList verifies DisabledChecks removes
// checks from the default base.
func TestGocritic_ExplicitDisableList(t *testing.T) {
	// appendAssign is on by default (pure diagnostic); disable it.
	s := &config.GoCriticSettings{
		DisabledChecks: []string{"appendAssign"},
	}
	enabled := gocriticEnabledChecks(s)
	if enabled["appendAssign"] {
		t.Error("DisabledChecks did not remove appendAssign")
	}
	// elseif is also default-on (diagnostic-only); make sure it
	// survived.
	if !enabled["elseif"] {
		t.Error("DisabledChecks dropped unrelated default-on check elseif")
	}
}

// TestGocritic_EnableAll verifies EnableAll yields the full known
// set (every registered check), and EnabledTags is additive
// (no-op when all are already on).
func TestGocritic_EnableAll(t *testing.T) {
	s := &config.GoCriticSettings{EnableAll: true}
	enabled := gocriticEnabledChecks(s)
	all := gocriticlinter.GetCheckersInfo()
	if len(enabled) != len(all) {
		t.Errorf("EnableAll active count = %d, want %d", len(enabled), len(all))
	}
}

// TestGocritic_DisableAll_ThenEnable verifies the DisableAll + then
// only-enable-X path.
func TestGocritic_DisableAll_ThenEnable(t *testing.T) {
	s := &config.GoCriticSettings{
		DisableAll:    true,
		EnabledChecks: []string{"hugeParam"},
	}
	enabled := gocriticEnabledChecks(s)
	if !enabled["hugeParam"] {
		t.Error("DisableAll+EnabledChecks did not enable hugeParam")
	}
	if enabled["appendAssign"] {
		t.Error("DisableAll left appendAssign on")
	}
}

// TestGocritic_EnabledTag_Performance verifies tag expansion. The
// `performance` tag is off by default; EnabledTags=[performance]
// should flip every performance-tagged check on (including
// hugeParam).
func TestGocritic_EnabledTag_Performance(t *testing.T) {
	s := &config.GoCriticSettings{
		EnabledTags: []string{gocriticlinter.PerformanceTag},
	}
	enabled := gocriticEnabledChecks(s)
	var perfFound int
	for _, info := range gocriticlinter.GetCheckersInfo() {
		if info.HasTag(gocriticlinter.PerformanceTag) {
			perfFound++
			if !enabled[info.Name] {
				t.Errorf("perf tag enable: %q missing", info.Name)
			}
		}
	}
	if perfFound == 0 {
		t.Fatal("no performance-tagged checks registered (upstream broken?)")
	}
	if !enabled["hugeParam"] {
		t.Error("perf tag enable did not include hugeParam")
	}
}

// TestGocritic_DisabledTag_Diagnostic verifies tag disable removes
// every check carrying the named tag.
func TestGocritic_DisabledTag_Diagnostic(t *testing.T) {
	s := &config.GoCriticSettings{
		DisabledTags: []string{gocriticlinter.DiagnosticTag},
	}
	enabled := gocriticEnabledChecks(s)
	for _, info := range gocriticlinter.GetCheckersInfo() {
		if info.HasTag(gocriticlinter.DiagnosticTag) && enabled[info.Name] {
			t.Errorf("DisabledTags=diagnostic still leaves %q on", info.Name)
		}
	}
}

// TestGocritic_SettingsPerCheck_HugeParam verifies that a per-check
// parameter (`sizeThreshold` for hugeParam, well-known and stable
// upstream) threads through the prototype's CheckerInfo.Params and
// is read at NewChecker construction time.
//
// This is the load-bearing per-check translation test — exercises
// the case-insensitive lookup AND the prototype-mutation path.
func TestGocritic_SettingsPerCheck_HugeParam(t *testing.T) {
	// Snapshot the original sizeThreshold so we can restore it. The
	// mutation in gocriticApplyParams is process-global (landmine 28)
	// and would leak across tests otherwise.
	var origValue any
	for _, info := range gocriticlinter.GetCheckersInfo() {
		if info.Name == "hugeParam" {
			if p, ok := info.Params["sizeThreshold"]; ok {
				origValue = p.Value
			}
			break
		}
	}
	defer func() {
		for _, info := range gocriticlinter.GetCheckersInfo() {
			if info.Name == "hugeParam" {
				if p, ok := info.Params["sizeThreshold"]; ok {
					p.Value = origValue
				}
				break
			}
		}
	}()

	s := &config.GoCriticSettings{
		EnabledChecks: []string{"hugeParam"},
		SettingsPerCheck: map[string]config.GoCriticCheckSettings{
			"hugeParam": {"sizeThreshold": 512},
		},
	}
	gocriticApplyParams(s)

	var landed any
	for _, info := range gocriticlinter.GetCheckersInfo() {
		if info.Name != "hugeParam" {
			continue
		}
		landed = info.Params["sizeThreshold"].Value
	}
	if landed != 512 {
		t.Errorf("sizeThreshold = %v (%T), want 512 (int)", landed, landed)
	}

	// Case-insensitive check name lookup: try the lowercased
	// equivalent and verify it still threads through.
	s2 := &config.GoCriticSettings{
		SettingsPerCheck: map[string]config.GoCriticCheckSettings{
			"hugeparam": {"SIZETHRESHOLD": 1024},
		},
	}
	gocriticApplyParams(s2)
	for _, info := range gocriticlinter.GetCheckersInfo() {
		if info.Name != "hugeParam" {
			continue
		}
		if got := info.Params["sizeThreshold"].Value; got != 1024 {
			t.Errorf("case-insensitive: sizeThreshold = %v, want 1024", got)
		}
	}
}

// TestGocritic_SettingsPerCheck_NormalizeFloat verifies the YAML
// float64-with-zero-fraction coercion path: a JSON-parsed numeric
// literal often arrives as float64, and gocritic asserts on int.
func TestGocritic_SettingsPerCheck_NormalizeFloat(t *testing.T) {
	got := normalizeGocriticParamValue(float64(42))
	if v, ok := got.(int); !ok || v != 42 {
		t.Errorf("float64(42) normalized to %v (%T), want int(42)", got, got)
	}
	// Non-integer float stays as-is.
	got = normalizeGocriticParamValue(3.14)
	if _, ok := got.(int); ok {
		t.Errorf("3.14 should not coerce to int")
	}
	// Bool, string, int pass through unchanged.
	if got := normalizeGocriticParamValue(true); got != true {
		t.Errorf("bool normalized to %v", got)
	}
	if got := normalizeGocriticParamValue("hello"); got != "hello" {
		t.Errorf("string normalized to %v", got)
	}
	if got := normalizeGocriticParamValue(int64(99)); got != 99 {
		t.Errorf("int64 normalized to %v (%T), want int(99)", got, got)
	}
}

// TestGocritic_UnknownCheck_Drops verifies an unknown check name in
// SettingsPerCheck is silently dropped (matches our other
// polymorphic-config wirings — validation belongs to config.Validate).
func TestGocritic_UnknownCheck_Drops(t *testing.T) {
	s := &config.GoCriticSettings{
		SettingsPerCheck: map[string]config.GoCriticCheckSettings{
			"bogusNonexistentChecker": {"foo": 1},
		},
	}
	gocriticApplyParams(s) // must not panic
}

// TestGocritic_Settings_RoundTrip verifies the typed settings struct
// survives Build via Resolved.Settings.
func TestGocritic_Settings_RoundTrip(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gocritic"}
	cfg.Linters.Settings.Gocritic.EnabledChecks = []string{"hugeParam"}
	cfg.Linters.Settings.Gocritic.DisabledChecks = []string{"paramTypeCombine"}
	cfg.Linters.Settings.Gocritic.SettingsPerCheck = map[string]config.GoCriticCheckSettings{
		"hugeParam": {"sizeThreshold": 256},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "gocritic" {
			continue
		}
		seen = true
		s, ok := r.Settings.(*config.GoCriticSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.GoCriticSettings", r.Settings)
		}
		if len(s.EnabledChecks) != 1 || s.EnabledChecks[0] != "hugeParam" {
			t.Errorf("EnabledChecks round-trip = %v", s.EnabledChecks)
		}
		if len(s.DisabledChecks) != 1 || s.DisabledChecks[0] != "paramTypeCombine" {
			t.Errorf("DisabledChecks round-trip = %v", s.DisabledChecks)
		}
		if v := s.SettingsPerCheck["hugeParam"]["sizeThreshold"]; v != 256 {
			t.Errorf("sizeThreshold round-trip = %v, want 256", v)
		}
	}
	if !seen {
		t.Error("gocritic missing from Enabled()")
	}
}

// TestGocritic_AllTagsCovered guards against drift in the canonical
// tag list. The four exclude-from-default tags (experimental /
// opinionated / performance / security) plus the two default-on
// tags (diagnostic / style) make up gocriticAllTags. We assert that
// the four exclude-from-default tags PLUS diagnostic+style each
// appear on at least one registered checker — except `security`,
// which is upstream-canonical but currently unused by the static
// check set (the embedded ruleguard rules don't tag it either).
// Security is included for future-proofing; if a registered check
// ever adds it, the wiring already handles it correctly.
func TestGocritic_AllTagsCovered(t *testing.T) {
	seen := make(map[string]bool)
	for _, info := range gocriticlinter.GetCheckersInfo() {
		for _, t := range info.Tags {
			seen[t] = true
		}
	}
	// These tags must appear on at least one check (load-bearing for
	// our default-enabled inference and tag-expansion paths).
	required := []string{
		gocriticlinter.DiagnosticTag,
		gocriticlinter.ExperimentalTag,
		gocriticlinter.OpinionatedTag,
		gocriticlinter.PerformanceTag,
		gocriticlinter.StyleTag,
	}
	for _, tag := range required {
		if !seen[tag] {
			t.Errorf("required tag %q unused by any registered checker", tag)
		}
	}
	// Sanity: canonical list still has 6 elements (catches a
	// shrink-the-list mistake).
	if got := len(gocriticAllTagsSorted()); got != 6 {
		t.Errorf("gocriticAllTags has %d elements, want 6", got)
	}
}
