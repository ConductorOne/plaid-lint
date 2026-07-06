// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// batch4Linters lists the linters wired in wire_analyzers_batch4.go.
var batch4Linters = []string{
	"cyclop",
	"dupword",
	"errchkjson",
	"funcorder",
	"funlen",
	"ginkgolinter",
	"inamedparam",
	"interfacebloat",
	"paralleltest",
	"reassign",
	"rowserrcheck",
	"testpackage",
}

func TestBatch4_ShapeNative(t *testing.T) {
	for _, name := range batch4Linters {
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

func TestBatch4_Enabled_ProducesAnalyzer(t *testing.T) {
	for _, name := range batch4Linters {
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

func TestBatch4_NoWarnings_NoAnalyzerWired(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = batch4Linters

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, r := range reg.All() {
		if !contains(batch4Linters, r.Name) {
			continue
		}
		if r.Status == StatusNoAnalyzerWired {
			t.Errorf("%s: still StatusNoAnalyzerWired (reason=%q)", r.Name, r.Reason)
		}
	}
}

// TestBatch4_Funlen_RespectsConstructor verifies the constructor-arg path.
func TestBatch4_Funlen_RespectsConstructor(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"funlen"}
	cfg.Linters.Settings.Funlen.Lines = 100
	cfg.Linters.Settings.Funlen.Statements = 50
	cfg.Linters.Settings.Funlen.IgnoreComments = true

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "funlen" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("funlen Analyzer is nil")
		}
		fs, ok := r.Settings.(*config.FunlenSettings)
		if !ok {
			t.Fatalf("Settings type = %T, want *config.FunlenSettings", r.Settings)
		}
		if fs.Lines != 100 || fs.Statements != 50 || !fs.IgnoreComments {
			t.Errorf("Settings = %+v, want Lines=100 Statements=50 IgnoreComments=true", fs)
		}
	}
	if !seen {
		t.Error("funlen missing from Enabled()")
	}
}

// TestBatch4_Cyclop_AppliesFlags verifies Flags.Set path for cyclop.
func TestBatch4_Cyclop_AppliesFlags(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"cyclop"}
	cfg.Linters.Settings.Cyclop.MaxComplexity = 25

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "cyclop" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("cyclop Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("maxComplexity")
		if f == nil {
			t.Fatal("cyclop Analyzer missing 'maxComplexity' flag")
		}
		if got := f.Value.String(); got != "25" {
			t.Errorf("maxComplexity flag = %q, want \"25\"", got)
		}
	}
	if !seen {
		t.Error("cyclop missing from Enabled()")
	}
}

// TestBatch4_Reassign_JoinsPatterns verifies multiple regex patterns are
// joined with alternation into a single flag value.
func TestBatch4_Reassign_JoinsPatterns(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"reassign"}
	cfg.Linters.Settings.Reassign.Patterns = []string{"Err.*", "EOF", "OK"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var seen bool
	for _, r := range reg.Enabled() {
		if r.Name != "reassign" {
			continue
		}
		seen = true
		if r.Analyzer == nil {
			t.Fatal("reassign Analyzer is nil")
		}
		f := r.Analyzer.Flags.Lookup("pattern")
		if f == nil {
			t.Fatal("reassign Analyzer missing 'pattern' flag")
		}
		if got, want := f.Value.String(), "Err.*|EOF|OK"; got != want {
			t.Errorf("pattern flag = %q, want %q", got, want)
		}
	}
	if !seen {
		t.Error("reassign missing from Enabled()")
	}
}
