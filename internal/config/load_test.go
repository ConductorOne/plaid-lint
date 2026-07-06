// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"strings"
	"testing"
	"time"
)

func TestDecode_V2Minimal(t *testing.T) {
	body := []byte(`
version: "2"
run:
  timeout: 5m
  go: "1.22"
linters:
  default: standard
  enable:
    - errcheck
    - govet
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("Decode returned warnings on clean v2: %v", warns)
	}
	if cfg.Version != "2" {
		t.Errorf("Version: got %q want %q", cfg.Version, "2")
	}
	if cfg.Run.Timeout.AsDuration() != 5*time.Minute {
		t.Errorf("Run.Timeout: got %v want 5m", cfg.Run.Timeout)
	}
	if cfg.Run.Go != "1.22" {
		t.Errorf("Run.Go: got %q want 1.22", cfg.Run.Go)
	}
	if cfg.Linters.Default != "standard" {
		t.Errorf("Linters.Default: got %q want standard", cfg.Linters.Default)
	}
	if len(cfg.Linters.Enable) != 2 {
		t.Fatalf("Linters.Enable: got %v want 2 entries", cfg.Linters.Enable)
	}
	if cfg.Linters.Exclusions.Generated != GeneratedModeStrict {
		t.Errorf("Linters.Exclusions.Generated: got %q want %q (default)",
			cfg.Linters.Exclusions.Generated, GeneratedModeStrict)
	}
}

func TestDecode_LegacyV1Skips(t *testing.T) {
	body := []byte(`
run:
  skip-files:
    - ".*\\.gen\\.go$"
  skip-dirs:
    - "vendor"
    - "third_party"
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(warns) == 0 {
		t.Fatal("expected warnings on v1 skips")
	}
	wantPaths := map[string]bool{
		`.*\.gen\.go$`: false,
		"vendor":       false,
		"third_party":  false,
	}
	for _, p := range cfg.Linters.Exclusions.Paths {
		if _, ok := wantPaths[p]; ok {
			wantPaths[p] = true
		}
	}
	for k, found := range wantPaths {
		if !found {
			t.Errorf("missing migrated path %q (got %v)", k, cfg.Linters.Exclusions.Paths)
		}
	}
}

func TestDecode_LegacyEnableAll(t *testing.T) {
	body := []byte(`
linters:
  enable-all: true
  disable:
    - dupl
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Linters.Default != GroupAll {
		t.Errorf("Default: got %q want %q", cfg.Linters.Default, GroupAll)
	}
	foundWarn := false
	for _, w := range warns {
		if w.Field == "linters.enable-all" {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Errorf("expected warning for linters.enable-all, got %v", warns)
	}
}

func TestDecode_LegacyIssuesExcludeRules(t *testing.T) {
	body := []byte(`
issues:
  exclude-rules:
    - path: "_test\\.go"
      linters:
        - dupl
        - gocyclo
    - text: "should have comment"
      path: ".*"
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(cfg.Linters.Exclusions.Rules) != 2 {
		t.Fatalf("Rules: got %d want 2", len(cfg.Linters.Exclusions.Rules))
	}
	if cfg.Linters.Exclusions.Rules[0].Path != "_test\\.go" {
		t.Errorf("Rules[0].Path: got %q", cfg.Linters.Exclusions.Rules[0].Path)
	}
	hasFieldWarn := false
	for _, w := range warns {
		if w.Field == "issues.exclude-rules" {
			hasFieldWarn = true
		}
	}
	if !hasFieldWarn {
		t.Errorf("expected migration warning, got %v", warns)
	}
}

func TestDecode_LegacyExcludeGenerated(t *testing.T) {
	cases := map[string]string{
		"none":    GeneratedModeDisable,
		"default": GeneratedModeStrict,
		"strict":  GeneratedModeStrict,
		"lax":     GeneratedModeLax,
		"disable": GeneratedModeDisable,
	}
	for v1Value, wantV2 := range cases {
		body := []byte("issues:\n  exclude-generated: " + v1Value + "\n")
		cfg, _, err := Decode(body, ".yml")
		if err != nil {
			t.Fatalf("Decode %q: %v", v1Value, err)
		}
		if cfg.Linters.Exclusions.Generated != wantV2 {
			t.Errorf("exclude-generated=%q: got %q want %q",
				v1Value, cfg.Linters.Exclusions.Generated, wantV2)
		}
	}
}

func TestDecode_LegacyLintersSettings(t *testing.T) {
	body := []byte(`
linters-settings:
  errcheck:
    check-blank: true
    exclude-functions:
      - fmt.Println
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !cfg.Linters.Settings.Errcheck.CheckAssignToBlank {
		t.Errorf("Errcheck.CheckAssignToBlank: got false; expected migration to populate it")
	}
	if len(cfg.Linters.Settings.Errcheck.ExcludeFunctions) != 1 {
		t.Errorf("Errcheck.ExcludeFunctions: got %v want [fmt.Println]",
			cfg.Linters.Settings.Errcheck.ExcludeFunctions)
	}
	hasWarn := false
	for _, w := range warns {
		if w.Field == "linters-settings" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected linters-settings migration warning, got %v", warns)
	}
}

func TestDecode_LegacySeverityRename(t *testing.T) {
	body := []byte(`
severity:
  default-severity: warning
  rules:
    - severity: error
      linters:
        - errcheck
`)
	cfg, warns, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Severity.Default != "warning" {
		t.Errorf("Severity.Default: got %q want warning", cfg.Severity.Default)
	}
	hasWarn := false
	for _, w := range warns {
		if w.Field == "severity.default-severity" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected severity.default-severity warning, got %v", warns)
	}
}

func TestDecode_ForbidigoPolymorphicForbid(t *testing.T) {
	body := []byte(`
linters:
  settings:
    forbidigo:
      forbid:
        - "^fmt\\.Println$"
        - p: "^os\\.Exit$"
          msg: "use log.Fatal"
        - pattern: "^panic$"
          pkg: ".*"
`)
	cfg, _, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	f := cfg.Linters.Settings.Forbidigo.Forbid
	if len(f) != 3 {
		t.Fatalf("forbid: got %d want 3", len(f))
	}
	if f[0].Pattern != `^fmt\.Println$` {
		t.Errorf("forbid[0].Pattern: got %q", f[0].Pattern)
	}
	if f[1].Pattern != `^os\.Exit$` || f[1].Msg != "use log.Fatal" {
		t.Errorf("forbid[1]: got %+v", f[1])
	}
	if f[2].Pattern != "^panic$" || f[2].Package != ".*" {
		t.Errorf("forbid[2]: got %+v", f[2])
	}
}

func TestDecode_ReviveRulesOrdered(t *testing.T) {
	body := []byte(`
linters:
  settings:
    revive:
      rules:
        - name: var-naming
          disabled: true
        - name: package-comments
          arguments:
            - "foo"
            - 42
`)
	cfg, _, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	r := cfg.Linters.Settings.Revive.Rules
	if len(r) != 2 {
		t.Fatalf("rules: got %d want 2", len(r))
	}
	if r[0].Name != "var-naming" || !r[0].Disabled {
		t.Errorf("rules[0]: got %+v", r[0])
	}
	if r[1].Name != "package-comments" || len(r[1].Arguments) != 2 {
		t.Errorf("rules[1]: got %+v", r[1])
	}
}

func TestDecode_GoCriticSettingsFreeForm(t *testing.T) {
	body := []byte(`
linters:
  settings:
    gocritic:
      enabled-checks:
        - hugeParam
      settings:
        hugeParam:
          sizeThreshold: 80
`)
	cfg, _, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	g := cfg.Linters.Settings.Gocritic
	if len(g.EnabledChecks) != 1 {
		t.Errorf("EnabledChecks: got %v", g.EnabledChecks)
	}
	if g.SettingsPerCheck["hugeParam"]["sizeThreshold"] != 80 {
		t.Errorf("SettingsPerCheck: got %+v", g.SettingsPerCheck)
	}
}

func TestDecode_EmptyYieldsDefault(t *testing.T) {
	cfg, warns, err := Decode([]byte(""), ".yml")
	if err != nil {
		t.Fatalf("Decode empty: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warns on empty: got %v", warns)
	}
	if cfg.Linters.Exclusions.Generated != GeneratedModeStrict {
		t.Errorf("default Generated: got %q want %q",
			cfg.Linters.Exclusions.Generated, GeneratedModeStrict)
	}
}

func TestValidate_BadGovet(t *testing.T) {
	cfg := &Config{
		Linters: Linters{
			Settings: LintersSettings{
				Govet: GovetSettings{
					EnableAll:  true,
					DisableAll: true,
				},
			},
		},
	}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected govet validation error")
	}
	if !strings.Contains(errs[0].Error(), "enable-all and disable-all") {
		t.Errorf("unexpected error: %v", errs[0])
	}
}

func TestValidate_RunModulesDownloadMode(t *testing.T) {
	cfg := &Config{Run: Run{ModulesDownloadMode: "frob"}}
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "modules-download-mode") {
		t.Errorf("expected modules-download-mode error, got %v", errs)
	}
}

func TestValidate_BadExclusionGenerated(t *testing.T) {
	cfg := &Config{Linters: Linters{Exclusions: LinterExclusions{Generated: "yolo"}}}
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "generated") {
		t.Errorf("expected generated mode error, got %v", errs)
	}
}

func TestValidate_BadExcludeRule(t *testing.T) {
	cfg := &Config{
		Linters: Linters{
			Exclusions: LinterExclusions{
				Rules: []ExcludeRule{
					{BaseRule: BaseRule{Path: "[bad-regex"}},
				},
			},
		},
	}
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "regex") {
		t.Errorf("expected regex error, got %v", errs)
	}
}

func TestValidate_BadSortOrder(t *testing.T) {
	cfg := &Config{Output: Output{SortOrder: []string{"foo"}}}
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "sort-order") {
		t.Errorf("expected sort-order error, got %v", errs)
	}
}

func TestValidate_NoFormattersUnderLinters(t *testing.T) {
	cfg := &Config{Linters: Linters{Enable: []string{"gofmt"}}}
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "formatter") {
		t.Errorf("expected formatter-in-linters error, got %v", errs)
	}
}

func TestMerge_OverlayWinsScalars(t *testing.T) {
	base := &Config{Run: Run{Timeout: Duration(time.Minute), Go: "1.21"}}
	overlay := &Config{Run: Run{Timeout: Duration(5 * time.Minute), Go: "1.22"}}
	merged := Merge(base, overlay)
	if merged.Run.Timeout.AsDuration() != 5*time.Minute {
		t.Errorf("Timeout: got %v want 5m", merged.Run.Timeout)
	}
	if merged.Run.Go != "1.22" {
		t.Errorf("Go: got %q want 1.22", merged.Run.Go)
	}
}

func TestMerge_AppendSlices(t *testing.T) {
	base := &Config{Linters: Linters{Enable: []string{"a", "b"}}}
	overlay := &Config{Linters: Linters{Enable: []string{"c", "b"}}}
	merged := Merge(base, overlay)
	if len(merged.Linters.Enable) != 3 {
		t.Errorf("Enable: got %v want [a b c]", merged.Linters.Enable)
	}
}

func TestMerge_NilArgs(t *testing.T) {
	cfg := &Config{Version: "2"}
	if got := Merge(nil, cfg); got.Version != "2" {
		t.Errorf("Merge(nil, x): got %q", got.Version)
	}
	if got := Merge(cfg, nil); got.Version != "2" {
		t.Errorf("Merge(x, nil): got %q", got.Version)
	}
	if got := Merge(nil, nil); got == nil {
		t.Error("Merge(nil, nil): got nil")
	}
}

func TestMerge_AppendRules(t *testing.T) {
	base := &Config{
		Linters: Linters{
			Exclusions: LinterExclusions{
				Rules: []ExcludeRule{
					{BaseRule: BaseRule{Path: "p1", Linters: []string{"foo"}}},
				},
			},
		},
	}
	overlay := &Config{
		Linters: Linters{
			Exclusions: LinterExclusions{
				Rules: []ExcludeRule{
					{BaseRule: BaseRule{Path: "p2", Linters: []string{"bar"}}},
				},
			},
		},
	}
	merged := Merge(base, overlay)
	if len(merged.Linters.Exclusions.Rules) != 2 {
		t.Errorf("Rules: got %d want 2", len(merged.Linters.Exclusions.Rules))
	}
}

func TestDecode_JSON(t *testing.T) {
	body := []byte(`{
  "version": "2",
  "run": {"timeout": "5m"},
  "linters": {"default": "none", "enable": ["errcheck"]}
}`)
	cfg, warns, err := Decode(body, ".json")
	if err != nil {
		t.Fatalf("Decode JSON: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warns on clean v2 JSON: %v", warns)
	}
	if cfg.Version != "2" {
		t.Errorf("Version: got %q", cfg.Version)
	}
	if cfg.Linters.Default != "none" {
		t.Errorf("Default: got %q want none", cfg.Linters.Default)
	}
}

func TestDecode_TOMLNotSupported(t *testing.T) {
	_, _, err := Decode([]byte(`anything`), ".toml")
	if err == nil || !strings.Contains(err.Error(), "TOML") {
		t.Errorf("expected TOML-not-supported error, got %v", err)
	}
}

func TestDiscoverDirs_StartsFromCwdWalksUp(t *testing.T) {
	dirs := DiscoverDirs("/some/deep/path")
	// Should walk up to /, plus $HOME if not already in the chain.
	if len(dirs) < 4 {
		t.Errorf("DiscoverDirs: got %v (too short)", dirs)
	}
	if dirs[0] != "/some/deep/path" {
		t.Errorf("DiscoverDirs[0]: got %q", dirs[0])
	}
}

// TestNewDefault_AppliesGoconstDefaults pins the goconst defaults
// upstream's `defaultLintersSettings` carries. Without these, the
// goconst library's zero values (MinStringLen=0, MinOccurrencesCount=0,
// IgnoreCalls=false) cause every string with >=0 occurrences to fire.
func TestNewDefault_AppliesGoconstDefaults(t *testing.T) {
	cfg := NewDefault()
	gc := cfg.Linters.Settings.Goconst
	if gc.MinStringLen != 3 {
		t.Errorf("Goconst.MinStringLen: got %d want 3", gc.MinStringLen)
	}
	if gc.MinOccurrencesCount != 3 {
		t.Errorf("Goconst.MinOccurrencesCount: got %d want 3", gc.MinOccurrencesCount)
	}
	if gc.NumberMin != 3 {
		t.Errorf("Goconst.NumberMin: got %d want 3", gc.NumberMin)
	}
	if gc.NumberMax != 3 {
		t.Errorf("Goconst.NumberMax: got %d want 3", gc.NumberMax)
	}
	if !gc.MatchWithConstants {
		t.Errorf("Goconst.MatchWithConstants: got false want true")
	}
	if !gc.IgnoreCalls {
		t.Errorf("Goconst.IgnoreCalls: got false want true")
	}
}

// TestFinalize_RespectsExplicitGoconstYaml asserts that an explicit
// YAML value wins over the injected default — the "user always wins"
// property of `applyLinterSettingsDefaults`.
func TestFinalize_RespectsExplicitGoconstYaml(t *testing.T) {
	body := []byte(`
linters:
  settings:
    goconst:
      min-len: 5
      min-occurrences: 4
`)
	cfg, _, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	gc := cfg.Linters.Settings.Goconst
	if gc.MinStringLen != 5 {
		t.Errorf("explicit MinStringLen lost: got %d want 5", gc.MinStringLen)
	}
	if gc.MinOccurrencesCount != 4 {
		t.Errorf("explicit MinOccurrencesCount lost: got %d want 4", gc.MinOccurrencesCount)
	}
	// Unset fields still get the upstream defaults.
	if gc.NumberMin != 3 {
		t.Errorf("default NumberMin not injected: got %d want 3", gc.NumberMin)
	}
}

// TestNewDefault_AppliesUnusedDefaults pins the upstream defaults for
// the `unused` linter. honnef.co/go/tools/unused's library zero-values
// flag every parameter / local / write-only field as unused; upstream
// golangci-lint compensates in defaultLintersSettings. Without
// this guard the native port produces ~5k unused diagnostics on
// c1's pkg/controller/... where the same config under golangci-lint
// v2.9 produces 0.
func TestNewDefault_AppliesUnusedDefaults(t *testing.T) {
	cfg := NewDefault()
	u := cfg.Linters.Settings.Unused
	if !u.FieldWritesAreUses {
		t.Errorf("Unused.FieldWritesAreUses: got false want true")
	}
	if !u.ExportedFieldsAreUsed {
		t.Errorf("Unused.ExportedFieldsAreUsed: got false want true")
	}
	if !u.ParametersAreUsed {
		t.Errorf("Unused.ParametersAreUsed: got false want true")
	}
	if !u.LocalVariablesAreUsed {
		t.Errorf("Unused.LocalVariablesAreUsed: got false want true")
	}
	if !u.GeneratedIsUsed {
		t.Errorf("Unused.GeneratedIsUsed: got false want true")
	}
	// PostStatementsAreReads stays false in upstream defaults.
	if u.PostStatementsAreReads {
		t.Errorf("Unused.PostStatementsAreReads: got true want false")
	}
}

// TestFinalize_RespectsExplicitUnusedYaml asserts that an explicit
// YAML block — even one that flips a single bool off — is treated as
// user-authoritative for every bool toggle in `unused`. Same "user
// always wins" property the goconst block above pins.
func TestFinalize_RespectsExplicitUnusedYaml(t *testing.T) {
	body := []byte(`
linters:
  settings:
    unused:
      post-statements-are-reads: true
`)
	cfg, _, err := Decode(body, ".yml")
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	u := cfg.Linters.Settings.Unused
	if !u.PostStatementsAreReads {
		t.Errorf("explicit PostStatementsAreReads lost: got false want true")
	}
	// Authoritative-block detection: because the user touched the unused
	// block (any non-zero bool), the bool-default-injection path is
	// skipped. The remaining bools keep their zero values.
	if u.FieldWritesAreUses {
		t.Errorf("FieldWritesAreUses default injected over explicit block")
	}
	if u.ParametersAreUsed {
		t.Errorf("ParametersAreUsed default injected over explicit block")
	}
}
