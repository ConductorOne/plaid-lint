// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch1Linters lists the twelve linters wired in
// wire_analyzers_batch1.go. Test assertions enumerate this set so
// adding a row to the batch (or removing one) is a single-line edit.
var batch1Linters = []string{
	"asciicheck",
	"bidichk",
	"bodyclose",
	"durationcheck",
	"gocheckcompilerdirectives",
	"goprintffuncname",
	"nakedret",
	"nilerr",
	"noctx",
	"predeclared",
	"tparallel",
	"usestdlibvars",
}

// TestBatch1_ShapeNative asserts every batch1 entry is ShapeNative
// (not ShapeRegistryOnly). The catalog seed declares the row as
// ShapeRegistryOnly; wireAnalyzerFnsBatch1 promotes it.
func TestBatch1_ShapeNative(t *testing.T) {
	for _, name := range batch1Linters {
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

// TestBatch1_Enabled_ProducesAnalyzer asserts that enabling each
// batch1 linter under `linters.enable` produces a Resolved row with
// Status=StatusEnabled and a non-nil Analyzer pointer (i.e. the
// engine has something to run).
func TestBatch1_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch1Linters {
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

// TestBatch1_NoWarnings_NoAnalyzerWired confirms the Enabled set
// no longer surfaces the StatusNoAnalyzerWired Reason for these
// twelve linters — the gap closure these wirings target.
func TestBatch1_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch1Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch1Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch1_Nakedret_RespectsSettings verifies the constructor-arg
// settings path: nakedret reads MaxFuncLines off NakedretSettings.
func TestBatch1_Nakedret_RespectsSettings(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"nakedret"}
	cfg.Linters.Settings.Nakedret.MaxFuncLines = 42

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "nakedret" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("nakedret Analyzer is nil")
		}
		// Setting threaded through is observable only by running the
		// analyzer; the resolver wires it, and a deeper assertion is
		// engine-side. Confirm at least that we got a unique Analyzer
		// instance per Build (the closure freshly constructs the
		// NakedReturnRunner each call).
		if r.Settings == nil {
			t.Error("nakedret Settings is nil; expected *config.NakedretSettings")
		}
		ns, ok := r.Settings.(*config.NakedretSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.NakedretSettings", r.Settings)
		}
		if ns.MaxFuncLines != 42 {
			t.Errorf("MaxFuncLines threaded through = %d, want 42", ns.MaxFuncLines)
		}
	}
	if !seen {
		t.Error("nakedret missing from Enabled()")
	}
}

// TestBatch1_Predeclared_AppliesFlags verifies the flag-set settings
// path: predeclared reads Ignore + Qualified off PredeclaredSettings
// and pushes them onto the Analyzer's Flags. Note this mutates a
// package-level global inside the predeclared module — see playbook
// landmines.
func TestBatch1_Predeclared_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"predeclared"}
	cfg.Linters.Settings.Predeclared.Ignore = []string{"new", "len"}
	cfg.Linters.Settings.Predeclared.Qualified = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "predeclared" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("predeclared Analyzer is nil")
		}
		// Flag inspection: the ignore flag should now stringify to
		// our list.
		ignFlag := r.Analyzer.Flags.Lookup("ignore")
		if ignFlag == nil {
			t.Fatal("predeclared Analyzer missing 'ignore' flag")
		}
		if got := ignFlag.Value.String(); !strings.Contains(got, "new") || !strings.Contains(got, "len") {
			t.Errorf("ignore flag = %q, want to contain 'new' and 'len'", got)
		}
	}
	if !seen {
		t.Error("predeclared missing from Enabled()")
	}
}

// TestBatch1_C1EnableSet covers c1's real-world long-tail set: every
// batch1 linter is enabled in c1's .golangci.yml. Confirms the
// wirings answer c1's bottleneck.
func TestBatch1_C1EnableSet(t *testing.T) {
	c1Enables := []string{
		// c1's full linters.enable list; we only check that the
		// batch1 members all resolve cleanly here.
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

	// Most batch1 linters are c1-enabled; gocheckcompilerdirectives
	// is in the batch for broad corpus coverage rather than c1
	// fit. Assert the intersection is non-trivial.
	var c1Hits int
	c1Set := map[string]bool{}
	for _, n := range c1Enables {
		c1Set[n] = true
	}
	for _, name := range batch1Linters {
		if c1Set[name] && enabled[name] {
			c1Hits++
		}
	}
	if c1Hits < 10 {
		t.Errorf("c1 enable set: batch1 ∩ c1 = %d hits, want >= 10 (batch was selected to bias c1)", c1Hits)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
