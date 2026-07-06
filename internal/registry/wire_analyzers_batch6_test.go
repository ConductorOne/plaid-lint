// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch6Linters lists the linters wired in wire_analyzers_batch6.go.
var batch6Linters = []string{
	"exhaustruct",
	"goheader",
	"importas",
	"ireturn",
	"loggercheck",
	"spancheck",
	"tagliatelle",
	"testifylint",
	"thelper",
	"varnamelen",
	"wsl_v5",
}

func TestBatch6_ShapeNative(t *testing.T) {
	for _, name := range batch6Linters {
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

func TestBatch6_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch6Linters {
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

func TestBatch6_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch6Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch6Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch6_Varnamelen_AppliesFlags verifies the camelCase flag
// translation lands int/string/bool fields on the Analyzer's flag set.
func TestBatch6_Varnamelen_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"varnamelen"}
	cfg.Linters.Settings.Varnamelen.MinNameLength = 3
	cfg.Linters.Settings.Varnamelen.CheckReceiver = true
	cfg.Linters.Settings.Varnamelen.IgnoreNames = []string{"i", "j"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "varnamelen" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("varnamelen Analyzer is nil")
		}
		if f := r.Analyzer.Flags.Lookup("minNameLength"); f == nil || f.Value.String() != "3" {
			t.Errorf("minNameLength = %v, want 3", f)
		}
		if f := r.Analyzer.Flags.Lookup("checkReceiver"); f == nil || f.Value.String() != "true" {
			t.Errorf("checkReceiver = %v, want true", f)
		}
		if f := r.Analyzer.Flags.Lookup("ignoreNames"); f == nil || f.Value.String() != "i,j" {
			t.Errorf("ignoreNames = %v, want i,j", f)
		}
	}
	if !seen {
		t.Error("varnamelen missing from Enabled()")
	}
}

// TestBatch6_Ireturn_AppliesFlags verifies the comma-string Flags.Set
// path for allow/reject.
func TestBatch6_Ireturn_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"ireturn"}
	cfg.Linters.Settings.Ireturn.Allow = []string{"error", "empty"}
	cfg.Linters.Settings.Ireturn.Reject = []string{"foo.Bar"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "ireturn" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("ireturn Analyzer is nil")
		}
		if f := r.Analyzer.Flags.Lookup("allow"); f == nil || f.Value.String() != "error,empty" {
			t.Errorf("allow = %v, want error,empty", f)
		}
		if f := r.Analyzer.Flags.Lookup("reject"); f == nil || f.Value.String() != "foo.Bar" {
			t.Errorf("reject = %v, want foo.Bar", f)
		}
	}
}

// TestBatch6_Thelper_NilPointersUseDefaults verifies the nil-pointer
// path leaves upstream's default (all twelve checks) untouched.
func TestBatch6_Thelper_NilPointersUseDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"thelper"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "thelper" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("thelper Analyzer is nil")
		}
		// Default Stringer for thelper's enabledChecksValue produces
		// the comma-joined token list. With zero settings the
		// translation must not have invoked Flags.Set; we verify by
		// checking that all twelve tokens are present.
		got := r.Analyzer.Flags.Lookup("checks").Value.String()
		for _, want := range []string{
			"t_begin", "t_first", "t_name",
			"f_begin", "f_first", "f_name",
			"b_begin", "b_first", "b_name",
			"tb_begin", "tb_first", "tb_name",
		} {
			if !contains(splitComma(got), want) {
				t.Errorf("checks=%q missing token %q (zero settings should keep upstream default)", got, want)
			}
		}
	}
}

// TestBatch6_Thelper_DisableOne verifies that setting a single *bool
// to false drops that single token from the checks list and keeps the
// other eleven enabled.
func TestBatch6_Thelper_DisableOne(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"thelper"}
	disabled := false
	cfg.Linters.Settings.Thelper.Test.First = &disabled

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "thelper" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("thelper Analyzer is nil")
		}
		got := splitComma(r.Analyzer.Flags.Lookup("checks").Value.String())
		if contains(got, "t_first") {
			t.Errorf("checks=%v still contains t_first; want it disabled", got)
		}
		// Remaining tokens should still be present (nil-as-enabled).
		for _, want := range []string{"t_begin", "t_name", "f_begin", "tb_name"} {
			if !contains(got, want) {
				t.Errorf("checks=%v missing %q", got, want)
			}
		}
	}
}

// TestBatch6_Testifylint_ZeroSettings_PreservesDefaults verifies the
// landmine-17 guard: zero TestifylintSettings does not touch the
// formatter.* default-on flags.
func TestBatch6_Testifylint_ZeroSettings_PreservesDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"testifylint"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "testifylint" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("testifylint Analyzer is nil")
		}
		// Upstream defaults: formatter.check-format-string=true,
		// formatter.require-string-msg=true. Zero settings must not
		// flip these.
		for _, name := range []string{
			"formatter.check-format-string",
			"formatter.require-string-msg",
		} {
			f := r.Analyzer.Flags.Lookup(name)
			if f == nil {
				t.Errorf("testifylint missing flag %q", name)
				continue
			}
			if got := f.Value.String(); got != "true" {
				t.Errorf("%s = %q, want %q (zero settings should preserve upstream default)",
					name, got, "true")
			}
		}
	}
}

// TestBatch6_Testifylint_FormatterPointer verifies the pointer-bool
// path threads explicit false through to formatter.check-format-string.
func TestBatch6_Testifylint_FormatterPointer(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"testifylint"}
	off := false
	cfg.Linters.Settings.Testifylint.Formatter.CheckFormatString = &off

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "testifylint" {
			continue
		}
		f := r.Analyzer.Flags.Lookup("formatter.check-format-string")
		if f == nil || f.Value.String() != "false" {
			t.Errorf("formatter.check-format-string = %v, want false", f)
		}
	}
}

// TestBatch6_Loggercheck_ZeroSettings_PreservesDefaults verifies the
// any-non-zero guard leaves upstream's "disable kitlog only" default
// intact when no logger flag is set.
func TestBatch6_Loggercheck_ZeroSettings_PreservesDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"loggercheck"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "loggercheck" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("loggercheck Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("disable")
		if f == nil {
			t.Fatal("loggercheck missing disable flag")
		}
		// Upstream default: disable=kitlog. Zero settings must not have
		// invoked WithDisable, so the flag's String() reflects the
		// "kitlog" default-disabled set.
		if got := f.Value.String(); got != "kitlog" {
			t.Errorf("disable = %q, want %q (zero settings should preserve upstream default)",
				got, "kitlog")
		}
	}
}

// TestBatch6_Importas_AppliesAliases verifies the per-alias Flags.Set
// path lands an alias on the global config.
func TestBatch6_Importas_AppliesAliases(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"importas"}
	cfg.Linters.Settings.ImportAs.NoUnaliased = true
	cfg.Linters.Settings.ImportAs.Alias = []config.ImportAsAlias{
		{Pkg: "k8s.io/api/core/v1", Alias: "corev1"},
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "importas" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("importas Analyzer is nil")
		}
		if f := r.Analyzer.Flags.Lookup("no-unaliased"); f == nil || f.Value.String() != "true" {
			t.Errorf("no-unaliased = %v, want true", f)
		}
		if f := r.Analyzer.Flags.Lookup("alias"); f == nil ||
			!stringContains(f.Value.String(), "k8s.io/api/core/v1 corev1") {
			t.Errorf("alias = %v, want to contain k8s.io/api/core/v1:corev1", f)
		}
	}
}

// TestBatch6_Exhaustruct_FallibleConstructor verifies the
// (*Analyzer, error) constructor shape. A clearly-malformed include
// regex should not panic; the registry must yield a non-nil Analyzer
// for the well-formed case below.
func TestBatch6_Exhaustruct_FallibleConstructor(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"exhaustruct"}
	cfg.Linters.Settings.Exhaustruct.Include = []string{`.*\.Cookie$`}
	cfg.Linters.Settings.Exhaustruct.AllowEmpty = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.Enabled() {
		if r.Name != "exhaustruct" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("exhaustruct Analyzer is nil for well-formed include regex")
		}
		// Settings round-trip through the resolved entry.
		s, ok := r.Settings.(*config.ExhaustructSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.ExhaustructSettings", r.Settings)
		}
		if !s.AllowEmpty {
			t.Errorf("AllowEmpty = false, want true")
		}
	}
}

// stringContains is a tiny strings.Contains shim — kept local so the
// test file's imports stay minimal.
func stringContains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// splitComma splits the standard `checks` flag value on commas; an
// empty string yields a nil slice.
func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}
