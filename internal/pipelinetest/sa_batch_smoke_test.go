// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// sa_batch_smoke_test.go — W8 smoke test for the mass-wired
// staticcheck SA-* analyzers. Drives Snapshot.Analyze
// with a representative subset of SA checks (mix of inspect-only and
// buildir-using) on a small fixture, and asserts:
//
//   - Cold and warm runs produce byte-identical diagnostic streams.
//     (cold→warm equivalence — the pipelinetest contract.)
//   - Warm run hits L1 for L1-eligible actions across the subset.
//   - SA-* descriptors registered in BundledRegistry are picked up by
//     Snapshot.Analyze on the warm run (descriptor-driven NeedsIR,
//     ConfigSalt, ResultCodec=nil → prereq-bypass on buildir).
//
// The fixture is intentionally minimal: a single package with
// dead-simple Go code that does NOT trigger any actual SA diagnostic.
// The smoke test's load-bearing claim is "the SA-* descriptors are
// runnable through the pipeline without panicking", not "we exercised
// every check's reporting logic" — that's Phase 2 / Phase 5 work.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/staticcheck"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// saBatchSubset returns 10 SA-* analyzers that span both
// inspect-only and buildir-using shapes. Two from each of the SA
// 1xxx/4xxx/5xxx/6xxx/9xxx families, balanced across NeedsIR=true
// and NeedsIR=false.
//
// The set is deterministic so the smoke test is stable across runs.
// If staticcheck removes any of these checks in a future version,
// the test's setup-time skip in installSABatchAnalyzers will surface
// it.
func saBatchSubset() []string {
	return []string{
		"SA1001", // inspect-only: invalid regular expression
		"SA1006", // inspect-only: printf with dynamic format
		"SA1015", // buildir: leaky time.Tick
		"SA1029", // buildir: built-in key collisions in context.Value
		"SA4006", // buildir: never-used value
		"SA4017", // buildir: side-effect-free pure function
		"SA5001", // inspect-only: defer before err check
		"SA5008", // inspect-only: invalid struct tag
		"SA6005", // inspect-only: inefficient string comparison
		"SA9009", // no Requires (no IR): ineffectual go directive
	}
}

func installSABatchAnalyzers(t *testing.T) []*analysis.Analyzer {
	t.Helper()
	want := saBatchSubset()
	wantSet := make(map[string]bool, len(want))
	for _, n := range want {
		wantSet[n] = true
	}
	picked := make([]*analysis.Analyzer, 0, len(want))
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil {
			continue
		}
		if wantSet[a.Name] {
			picked = append(picked, a)
		}
	}
	if len(picked) != len(want) {
		// Identify the missing analyzers for diagnostics.
		gotNames := make(map[string]bool, len(picked))
		for _, a := range picked {
			gotNames[a.Name] = true
		}
		missing := make([]string, 0)
		for _, n := range want {
			if !gotNames[n] {
				missing = append(missing, n)
			}
		}
		t.Skipf("SA-* batch subset incomplete in staticcheck.Analyzers @ pinned version: missing %v", missing)
	}
	// Deterministic order by name so the test logs are reproducible.
	sort.Slice(picked, func(i, j int) bool { return picked[i].Name < picked[j].Name })

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = nil
	for _, a := range picked {
		settings.AllAnalyzers = append(settings.AllAnalyzers, settings.NewAnalyzer(a))
	}
	return picked
}

// saBatchFixture writes a minimal go module rooted at dir. The
// fixture deliberately avoids stdlib imports (the gopls fork's
// stdlib load path is incomplete; packages
// importing stdlib mark compiles=false and analyzers skip). It also
// avoids actually triggering any SA-* finding — the smoke test's
// load-bearing claim is the pipeline doesn't panic.
func saBatchFixture(t *testing.T, dir string) {
	t.Helper()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write("go.mod", "module example.com/sabatch\n\ngo 1.22\n")
	// A single self-contained package; no stdlib imports.
	write("a/a.go", `package a

type T struct {
	Name string
}

func New(name string) *T { return &T{Name: name} }

func Touch(t *T) string {
	if t == nil {
		return ""
	}
	return t.Name
}
`)
}

// TestSABatchPipelineSmoke is the W8 mass-wire smoke test.
// Cold and warm Snapshot.Analyze runs with the SA-* subset
// installed; assert pipeline survival and cold→warm equivalence.
func TestSABatchPipelineSmoke(t *testing.T) {
	requireGo(t)
	picked := installSABatchAnalyzers(t)
	t.Logf("driving SA-* batch with %d analyzers: %v", len(picked), saBatchSubset())
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-sa-smoke"

	runOnce := func(t *testing.T) (map[string][]canonicalDiag, cache.L1Metrics, l3.IRManager) {
		t.Helper()
		l1, err := clcache.Open(l1Dir)
		if err != nil {
			t.Fatalf("Open L1: %v", err)
		}
		l2, err := clcache.Open(l2Dir)
		if err != nil {
			t.Fatalf("Open L2: %v", err)
		}
		c := cache.New(nil)
		c.AttachL1(l1, toolVer)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		mgr := l3.NewSequentialIRManager()
		c.AttachIRManager(mgr)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()

		diags := runAnalyzePipeline(t, ws)
		return diags, c.L1Metrics(), mgr
	}

	cold, coldMetrics, coldMgr := runOnce(t)
	t.Logf("cold: L1 hits=%d stores=%d pins=%d",
		coldMetrics.Hits, coldMetrics.Stores, coldMgr.(*l3.SequentialIRManager).TotalPins())
	if coldMetrics.Stores == 0 {
		t.Errorf("cold: L1 stores = 0, want > 0 (mass-wired SA-* descriptors should produce L1 entries)")
	}
	if coldMetrics.Hits != 0 {
		t.Errorf("cold: L1 hits = %d, want 0 (fresh cache)", coldMetrics.Hits)
	}
	if coldMgr.(*l3.SequentialIRManager).TotalPins() == 0 {
		// Subset includes SA1015, SA1029, SA4006, SA4017, SA9009 — at
		// least three of these have NeedsIR=true in BundledRegistry,
		// so at least one pin must have fired on cold.
		t.Errorf("cold: TotalPins = 0; at least one SA-* in the subset has NeedsIR=true")
	}

	warm, warmMetrics, warmMgr := runOnce(t)
	t.Logf("warm: L1 hits=%d stores=%d pins=%d",
		warmMetrics.Hits, warmMetrics.Stores, warmMgr.(*l3.SequentialIRManager).TotalPins())

	// Warm: L1 hits must be > 0. The SA-* root analyzers in the
	// subset are L1-eligible (their descriptors have ConfigSalt and
	// they aren't consumed by another analyzer in the same DAG),
	// so they hit on warm.
	if warmMetrics.Hits == 0 {
		t.Errorf("warm: L1 hits = 0, want > 0")
	}

	// cold→warm diagnostic equivalence is the load-bearing contract.
	coldKey := canonicalDigest(cold)
	warmKey := canonicalDigest(warm)
	if coldKey != warmKey {
		t.Errorf("SA-* batch cold→warm diagnostic streams differ:\n  cold: %s\n  warm: %s",
			coldKey, warmKey)
	}

	// Per-analyzer no-leak invariant: every pin released.
	if got := warmMgr.Snapshot(); len(got) != 0 {
		t.Errorf("warm: IRManager.Snapshot = %v, want empty (pin leak)", got)
	}

	// Sanity: every SA-* descriptor we drove is registered.
	for _, a := range picked {
		if d := analyzers.BundledRegistry.Lookup(a); d == nil {
			t.Errorf("SA-* %q: no descriptor in BundledRegistry", a.Name)
		}
	}
}

// TestSABatchAllRegisteredSurviveTypeCheck is the broadest survival
// check: drive ALL SA-* analyzers (all 95) through the pipeline on
// the same minimal fixture. Cold→warm equivalence + no panic.
//
// This is slower than the 10-analyzer smoke (the 95-analyzer DAG per
// package is wider), so it's gated by testing.Short.
func TestSABatchAllRegisteredSurviveTypeCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping all-SA-* survival test in short mode")
	}
	requireGo(t)

	all := analyzers.AllStaticcheckSAAnalyzers()
	if len(all) == 0 {
		t.Fatal("AllStaticcheckSAAnalyzers returned 0 analyzers")
	}
	t.Logf("driving full SA-* batch with %d analyzers", len(all))

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = nil
	for _, a := range all {
		settings.AllAnalyzers = append(settings.AllAnalyzers, settings.NewAnalyzer(a))
	}

	t.Setenv("GOPLSCACHE", goplsCacheDir(t))
	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-sa-all"

	runOnce := func(t *testing.T) (map[string][]canonicalDiag, cache.L1Metrics) {
		t.Helper()
		l1, err := clcache.Open(l1Dir)
		if err != nil {
			t.Fatalf("Open L1: %v", err)
		}
		l2, err := clcache.Open(l2Dir)
		if err != nil {
			t.Fatalf("Open L2: %v", err)
		}
		c := cache.New(nil)
		c.AttachL1(l1, toolVer)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		c.AttachIRManager(l3.NewSequentialIRManager())
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()
		diags := runAnalyzePipeline(t, ws)
		return diags, c.L1Metrics()
	}

	cold, coldMetrics := runOnce(t)
	t.Logf("cold (all SA-*): L1 hits=%d stores=%d", coldMetrics.Hits, coldMetrics.Stores)
	if coldMetrics.Stores == 0 {
		t.Errorf("cold (all SA-*): L1 stores = 0, want > 0")
	}

	warm, warmMetrics := runOnce(t)
	t.Logf("warm (all SA-*): L1 hits=%d stores=%d", warmMetrics.Hits, warmMetrics.Stores)
	if warmMetrics.Hits == 0 {
		t.Errorf("warm (all SA-*): L1 hits = 0, want > 0")
	}

	coldKey := canonicalDigest(cold)
	warmKey := canonicalDigest(warm)
	if coldKey != warmKey {
		// Print a short head/tail of the diff so the failure is debuggable.
		max := 400
		cd := coldKey
		wd := warmKey
		if len(cd) > max {
			cd = cd[:max] + "..."
		}
		if len(wd) > max {
			wd = wd[:max] + "..."
		}
		t.Errorf("all-SA-* batch cold→warm streams differ:\n  cold: %s\n  warm: %s", cd, wd)
	}
}

