// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// ifaceSubAnalyzerNames lists every iface sub-analyzer token the
// registry knows about. Test assertions enumerate this set so adding
// a sub-analyzer to the family is a single-line edit.
var ifaceSubAnalyzerNames = []string{
	"identical",
	"opaque",
	"unexported",
	"unused",
	"unusedmethod",
}

// TestIface_CatalogShape asserts iface is wired as ShapeNativeFamily
// (third such row, after govet and staticcheck) with an AnalyzerFn
// attached.
func TestIface_CatalogShape(t *testing.T) {
	e, ok := defaultCatalog.resolve("iface")
	if !ok {
		t.Fatal("catalog missing iface")
	}
	if e.Shape != ShapeNativeFamily {
		t.Errorf("Shape = %v, want ShapeNativeFamily", e.Shape)
	}
	if e.AnalyzerFn == nil {
		t.Fatal("AnalyzerFn is nil")
	}
}

// TestIface_SubAnalyzersNonNil verifies every sub-analyzer pointer is
// populated. Catches an upstream package that exported a nil var (a
// silent failure mode if a future release renames the global).
func TestIface_SubAnalyzersNonNil(t *testing.T) {
	for _, n := range ifaceSubAnalyzerNames {
		n := n
		t.Run(n, func(t *testing.T) {
			a, ok := ifaceSubAnalyzers[n]
			if !ok {
				t.Fatalf("ifaceSubAnalyzers missing %q", n)
			}
			if a == nil {
				t.Errorf("ifaceSubAnalyzers[%q] is nil", n)
			}
			if a.Name != n {
				t.Errorf("Analyzer.Name = %q, want %q", a.Name, n)
			}
		})
	}
}

// TestIfaceAnalyzers_DefaultSet verifies the default (empty Enable)
// path expands to just `identical` — the only default-on sub-analyzer
// per the uudashr/iface README.
func TestIfaceAnalyzers_DefaultSet(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"iface"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	subs := map[string]bool{}
	for _, r := range reg.Enabled() {
		if r.Name == "iface" && r.Analyzer != nil {
			subs[r.Analyzer.Name] = true
		}
	}
	if !subs["identical"] {
		t.Errorf("iface default set missing identical; got %v", sortedKeys(subs))
	}
	for _, opt := range []string{"unused", "unusedmethod", "opaque", "unexported"} {
		if subs[opt] {
			t.Errorf("iface default set wrongly includes %q (opt-in only)", opt)
		}
	}
}

// TestIfaceAnalyzers_EnableAll covers an explicit enable list naming
// every sub-analyzer. Iface has no enable-all flag (unlike govet); the
// user must list each token.
func TestIfaceAnalyzers_EnableAll(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"iface"}
	cfg.Linters.Settings.Iface.Enable = []string{
		"identical", "opaque", "unexported", "unused", "unusedmethod",
	}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	subs := map[string]bool{}
	for _, r := range reg.Enabled() {
		if r.Name == "iface" && r.Analyzer != nil {
			subs[r.Analyzer.Name] = true
		}
	}
	for _, n := range ifaceSubAnalyzerNames {
		if !subs[n] {
			t.Errorf("iface enable-all missing %q; got %v", n, sortedKeys(subs))
		}
	}
}

// TestIfaceAnalyzers_ExplicitEnable verifies a partial enable list
// selects exactly the named sub-analyzers and replaces the default
// `identical` row (no implicit union with the default set).
func TestIfaceAnalyzers_ExplicitEnable(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"iface"}
	cfg.Linters.Settings.Iface.Enable = []string{"unused", "opaque"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	subs := map[string]bool{}
	for _, r := range reg.Enabled() {
		if r.Name == "iface" && r.Analyzer != nil {
			subs[r.Analyzer.Name] = true
		}
	}
	want := map[string]bool{"unused": true, "opaque": true}
	for n := range want {
		if !subs[n] {
			t.Errorf("explicit enable missing %q; got %v", n, sortedKeys(subs))
		}
	}
	for n := range subs {
		if !want[n] {
			t.Errorf("explicit enable wrongly includes %q; want only %v", n, sortedKeys(want))
		}
	}
}

// TestIfaceAnalyzers_DisableAll verifies that explicitly disabling
// iface from linters.disable drops every sub-analyzer (no fan-out
// rows in Enabled()). Iface has no per-sub-analyzer disable list; the
// only way to silence the family is to drop the catalog row.
func TestIfaceAnalyzers_DisableAll(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"iface"}
	cfg.Linters.Disable = []string{"iface"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	for _, r := range reg.Enabled() {
		if r.Name == "iface" {
			t.Errorf("iface leaked into Enabled() despite disable; analyzer=%v", r.Analyzer)
		}
	}
}

// TestIfaceAnalyzers_DirectNilCfg covers the cfg==nil safety branch
// in ifaceAnalyzers (defensive — the catalog should always pass an
// *IfaceSettings, but the fallback must yield the default set).
func TestIfaceAnalyzers_DirectNilCfg(t *testing.T) {
	got := ifaceAnalyzers(nil)
	if len(got) != 1 {
		t.Fatalf("ifaceAnalyzers(nil) len = %d, want 1 (default identical only)", len(got))
	}
	if got[0].Name != "identical" {
		t.Errorf("ifaceAnalyzers(nil)[0] = %q, want identical", got[0].Name)
	}
}
