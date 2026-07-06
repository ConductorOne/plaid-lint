// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch3Linters lists the linters wired in wire_analyzers_batch3.go.
var batch3Linters = []string{
	"contextcheck",
	"copyloopvar",
	"fatcontext",
	"gochecknoglobals",
	"gocognit",
	"gosmopolitan",
	"maintidx",
	"makezero",
	"nilnil",
	"nlreturn",
	"sqlclosecheck",
	"testableexamples",
}

func TestBatch3_ShapeNative(t *testing.T) {
	for _, name := range batch3Linters {
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

func TestBatch3_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch3Linters {
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

func TestBatch3_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch3Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch3Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch3_Gocognit_AppliesFlags verifies MinComplexity threads through
// the package-global `over` flag on gocognit.Analyzer.
func TestBatch3_Gocognit_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gocognit"}
	cfg.Linters.Settings.Gocognit.MinComplexity = 42

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "gocognit" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("gocognit Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("over")
		if f == nil {
			t.Fatal("gocognit Analyzer missing 'over' flag")
		}
		if got := f.Value.String(); got != "42" {
			t.Errorf("over flag = %q, want \"42\"", got)
		}
	}
	if !seen {
		t.Error("gocognit missing from Enabled()")
	}
}

// TestBatch3_Maintidx_AppliesFlags verifies Under threads through
// maintidx.Analyzer's `under` flag.
func TestBatch3_Maintidx_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"maintidx"}
	cfg.Linters.Settings.MaintIdx.Under = 15

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "maintidx" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("maintidx Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("under")
		if f == nil {
			t.Fatal("maintidx Analyzer missing 'under' flag")
		}
		if got := f.Value.String(); got != "15" {
			t.Errorf("under flag = %q, want \"15\"", got)
		}
	}
	if !seen {
		t.Error("maintidx missing from Enabled()")
	}
}

// TestBatch3_Gosmopolitan_UsesConfigConstructor verifies the
// constructor-arg path: settings are passed to NewAnalyzerWithConfig.
// The settings are only observable via behavior, not via Flags.Lookup
// (the cfg-based constructor doesn't bind flags), so we just confirm
// the Analyzer is non-nil and Settings round-tripped.
func TestBatch3_Gosmopolitan_RespectsSettings(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"gosmopolitan"}
	cfg.Linters.Settings.Gosmopolitan.AllowTimeLocal = true
	cfg.Linters.Settings.Gosmopolitan.EscapeHatches = []string{"fmt.Println"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "gosmopolitan" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("gosmopolitan Analyzer is nil")
		}
		gs, ok := r.Settings.(*config.GosmopolitanSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.GosmopolitanSettings", r.Settings)
		}
		if !gs.AllowTimeLocal {
			t.Error("AllowTimeLocal didn't thread through Resolved.Settings")
		}
		if len(gs.EscapeHatches) != 1 || gs.EscapeHatches[0] != "fmt.Println" {
			t.Errorf("EscapeHatches = %v, want [\"fmt.Println\"]", gs.EscapeHatches)
		}
	}
	if !seen {
		t.Error("gosmopolitan missing from Enabled()")
	}
}
