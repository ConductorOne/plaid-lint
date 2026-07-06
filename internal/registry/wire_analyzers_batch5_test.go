// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch5Linters lists the linters wired in wire_analyzers_batch5.go.
var batch5Linters = []string{
	"decorder",
	"embeddedstructfieldcheck",
	"errorlint",
	"grouper",
	"musttag",
	"perfsprint",
	"protogetter",
	"recvcheck",
	"sloglint",
	"tagalign",
	"usetesting",
	"wrapcheck",
}

func TestBatch5_ShapeNative(t *testing.T) {
	for _, name := range batch5Linters {
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

func TestBatch5_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch5Linters {
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

func TestBatch5_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch5Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch5Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch5_Recvcheck_RespectsSettings verifies the constructor-arg
// path threads RecvcheckSettings into recvcheck.NewAnalyzer.
func TestBatch5_Recvcheck_RespectsSettings(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"recvcheck"}
	cfg.Linters.Settings.Recvcheck.DisableBuiltin = true
	cfg.Linters.Settings.Recvcheck.Exclusions = []string{"Foo.Bar", "*.Baz"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "recvcheck" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("recvcheck Analyzer is nil")
		}
		s, ok := r.Settings.(*config.RecvcheckSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.RecvcheckSettings", r.Settings)
		}
		if !s.DisableBuiltin {
			t.Errorf("DisableBuiltin = false, want true")
		}
		if len(s.Exclusions) != 2 || s.Exclusions[0] != "Foo.Bar" {
			t.Errorf("Exclusions = %v, want [Foo.Bar *.Baz]", s.Exclusions)
		}
	}
	if !seen {
		t.Error("recvcheck missing from Enabled()")
	}
}

// TestBatch5_Grouper_AppliesFlags verifies Flags.Set translates every
// bool field — grouper defaults are all false so the per-flag setting
// always lands a fresh value.
func TestBatch5_Grouper_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"grouper"}
	cfg.Linters.Settings.Grouper.ConstRequireGrouping = true
	cfg.Linters.Settings.Grouper.VarRequireSingleVar = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "grouper" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("grouper Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("const-require-grouping")
		if f == nil || f.Value.String() != "true" {
			t.Errorf("const-require-grouping = %v, want true", f)
		}
		f = r.Analyzer.Flags.Lookup("var-require-single-var")
		if f == nil || f.Value.String() != "true" {
			t.Errorf("var-require-single-var = %v, want true", f)
		}
		f = r.Analyzer.Flags.Lookup("type-require-grouping")
		if f == nil || f.Value.String() != "false" {
			t.Errorf("type-require-grouping = %v, want false", f)
		}
	}
	if !seen {
		t.Error("grouper missing from Enabled()")
	}
}

// TestBatch5_Usetesting_ZeroSettings_PreservesDefaults verifies the
// "any non-zero" guard leaves upstream defaults intact when the user
// did not configure any field.
func TestBatch5_Usetesting_ZeroSettings_PreservesDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"usetesting"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name != "usetesting" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("usetesting Analyzer is nil")
		}
		// Upstream defaults: oschdir / osmkdirtemp / oscreatetemp = true.
		// With zero settings the analyzer should still carry those.
		for _, name := range []string{"oschdir", "osmkdirtemp", "oscreatetemp"} {
			f := r.Analyzer.Flags.Lookup(name)
			if f == nil {
				t.Errorf("usetesting missing flag %q", name)
				continue
			}
			if got := f.Value.String(); got != "true" {
				t.Errorf("%s = %q, want %q (zero settings should preserve upstream default)",
					name, got, "true")
			}
		}
	}
}

// TestBatch5_Usetesting_NonZero_AppliesFlags verifies the translation
// fires once any field is non-zero.
func TestBatch5_Usetesting_NonZero_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"usetesting"}
	cfg.Linters.Settings.UseTesting.ContextBackground = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name != "usetesting" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("usetesting Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("contextbackground")
		if f == nil || f.Value.String() != "true" {
			t.Errorf("contextbackground = %v, want true", f)
		}
		// Other fields fall back to the zero-value of the settings struct
		// once anyNonZero is true. The default-on fields get flipped to
		// false here — that's intentional, since the user opted into
		// explicit configuration mode.
		f = r.Analyzer.Flags.Lookup("oschdir")
		if f == nil || f.Value.String() != "false" {
			t.Errorf("oschdir = %v, want false (explicit zero from user override)", f)
		}
	}
}

// TestBatch5_Sloglint_RenameMapping verifies sloglint v0.12.0 field
// renames map correctly across the wrapper rename gap (NoRawKeys ->
// ConstantKeys etc).
func TestBatch5_Sloglint_RenameMapping(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"sloglint"}
	cfg.Linters.Settings.SlogLint.NoRawKeys = true
	cfg.Linters.Settings.SlogLint.KeyNamingCase = "snake"

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "sloglint" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("sloglint Analyzer is nil")
		}
		// The upstream Options struct field is bound to "const-keys".
		f := r.Analyzer.Flags.Lookup("const-keys")
		if f == nil {
			t.Fatal("sloglint missing const-keys flag")
		}
		if got := f.Value.String(); got != "true" {
			t.Errorf("const-keys (mapped from NoRawKeys) = %q, want \"true\"", got)
		}
		f = r.Analyzer.Flags.Lookup("key-naming-case")
		if f == nil {
			t.Fatal("sloglint missing key-naming-case flag")
		}
		if got := f.Value.String(); got != "snake" {
			t.Errorf("key-naming-case = %q, want \"snake\"", got)
		}
	}
	if !seen {
		t.Error("sloglint missing from Enabled()")
	}
}

// TestBatch5_Decorder_GolangciDefaults verifies the wrapper-defaults
// baseline applies when settings are zero — three "disable-X" flags
// land as true, not false.
func TestBatch5_Decorder_GolangciDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"decorder"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name != "decorder" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("decorder Analyzer is nil")
		}
		want := map[string]string{
			"disable-dec-num-check":         "true",
			"disable-dec-order-check":       "true",
			"disable-init-func-first-check": "true",
			"disable-type-dec-num-check":    "false",
		}
		for k, v := range want {
			f := r.Analyzer.Flags.Lookup(k)
			if f == nil {
				t.Errorf("decorder missing flag %q", k)
				continue
			}
			if got := f.Value.String(); got != v {
				t.Errorf("%s = %q, want %q", k, got, v)
			}
		}
	}
}

// TestBatch5_Errorlint_ZeroSettings_PreservesDefaults confirms the
// landmine-17 guard: zero ErrorLintSettings leaves comparison/asserts
// at their upstream-true defaults rather than flipping them to false.
func TestBatch5_Errorlint_ZeroSettings_PreservesDefaults(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"errorlint"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name != "errorlint" {
			continue
		}
		if r.Analyzer == nil {
			t.Fatal("errorlint Analyzer is nil")
		}
		// Upstream defaults: comparison=true, asserts=true, errorf=false,
		// errorf-multi=true. Zero settings must not flip any of them.
		want := map[string]string{
			"comparison":   "true",
			"asserts":      "true",
			"errorf":       "false",
			"errorf-multi": "true",
		}
		for k, v := range want {
			f := r.Analyzer.Flags.Lookup(k)
			if f == nil {
				t.Errorf("errorlint missing flag %q", k)
				continue
			}
			if got := f.Value.String(); got != v {
				t.Errorf("%s = %q, want %q (zero settings should preserve upstream default)",
					k, got, v)
			}
		}
	}
}
