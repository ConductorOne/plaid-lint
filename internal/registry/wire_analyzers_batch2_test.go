// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch2Linters lists the linters wired in wire_analyzers_batch2.go.
// Test assertions enumerate this set so adding a row to the batch (or
// removing one) is a single-line edit.
var batch2Linters = []string{
	"asasalint",
	"canonicalheader",
	"containedctx",
	"errname",
	"exptostd",
	"forcetypeassert",
	"intrange",
	"mirror",
	"nilnesserr",
	"noinlineerr",
	"nonamedreturns",
	"nosprintfhostport",
	"wastedassign",
	"whitespace",
	"zerologlint",
}

// TestBatch2_ShapeNative asserts every batch2 entry is ShapeNative
// (not ShapeRegistryOnly). The catalog seed declares the row as
// ShapeRegistryOnly; wireAnalyzerFnsBatch2 promotes it.
func TestBatch2_ShapeNative(t *testing.T) {
	for _, name := range batch2Linters {
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

// TestBatch2_Enabled_ProducesAnalyzer asserts that enabling each
// batch2 linter under `linters.enable` produces a Resolved row with
// Status=StatusEnabled and a non-nil Analyzer pointer (i.e. the
// engine has something to run).
func TestBatch2_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch2Linters {
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

// TestBatch2_NoWarnings_NoAnalyzerWired confirms the Enabled set no
// longer surfaces the StatusNoAnalyzerWired Reason for these linters —
// the gap closure these wirings target.
func TestBatch2_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch2Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch2Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch2_Whitespace_RespectsSettings verifies the constructor-arg
// settings path: whitespace.NewAnalyzer receives a *Settings struct.
func TestBatch2_Whitespace_RespectsSettings(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"whitespace"}
	cfg.Linters.Settings.Whitespace.MultiIf = true
	cfg.Linters.Settings.Whitespace.MultiFunc = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "whitespace" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("whitespace Analyzer is nil")
		}
		ws, ok := r.Settings.(*config.WhitespaceSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.WhitespaceSettings", r.Settings)
		}
		if !ws.MultiIf || !ws.MultiFunc {
			t.Errorf("whitespace Settings threaded through = %+v, want both true", ws)
		}
	}
	if !seen {
		t.Error("whitespace missing from Enabled()")
	}
}

// TestBatch2_NoNamedReturns_AppliesFlags verifies the flag-set settings
// path: nonamedreturns reads ReportErrorInDefer off
// NoNamedReturnsSettings and pushes it onto the Analyzer's Flags. Like
// predeclared this mutates a package-global Analyzer FlagSet.
func TestBatch2_NoNamedReturns_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"nonamedreturns"}
	cfg.Linters.Settings.NoNamedReturns.ReportErrorInDefer = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "nonamedreturns" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("nonamedreturns Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("report-error-in-defer")
		if f == nil {
			t.Fatal("nonamedreturns Analyzer missing 'report-error-in-defer' flag")
		}
		if got := f.Value.String(); got != "true" {
			t.Errorf("flag = %q, want \"true\"", got)
		}
	}
	if !seen {
		t.Error("nonamedreturns missing from Enabled()")
	}
}

// TestBatch2_C1EnableSet covers c1's real-world long-tail set —
// asasalint, nonamedreturns, nosprintfhostport, and whitespace are
// enabled in c1's .golangci.yml. Confirms batch2 closes those gaps.
func TestBatch2_C1EnableSet(t *testing.T) {
	c1Enables := []string{
		"asasalint", "asciicheck", "bidichk", "bodyclose", "depguard",
		"durationcheck", "errcheck", "errorlint", "exhaustive",
		"forbidigo", "gochecknoinits", "goconst", "gocritic",
		"gomoddirectives", "goprintffuncname", "gosec", "govet",
		"ineffassign", "nakedret", "nilerr", "noctx", "nolintlint",
		"nonamedreturns", "nosprintfhostport", "predeclared", "revive",
		"staticcheck", "tparallel", "unconvert", "usestdlibvars",
		"whitespace",
	}

	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = c1Enables

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	enabled := map[string]bool{}
	for _, r := range reg.Enabled() {
		enabled[r.Name] = true
	}

	// Four batch2 linters are c1-enabled: asasalint, nonamedreturns,
	// nosprintfhostport, whitespace. Assert all four resolve cleanly.
	c1Wants := []string{"asasalint", "nonamedreturns", "nosprintfhostport", "whitespace"}
	for _, name := range c1Wants {
		if !enabled[name] {
			t.Errorf("batch2 ∩ c1: %q expected in Enabled()", name)
		}
	}
}
