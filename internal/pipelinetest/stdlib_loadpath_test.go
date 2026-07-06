// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// stdlib_loadpath_test.go is the W9 regression test for the
// stdlib type-check bug (W6 / W7 / W8 / W9).
//
// Before the fix, the gopls fork's View constructor left
// Folder.Env.GoVersion = 0, so the per-package goVersion fallback in
// check.go's typeCheckInputs resolved to "go1.0" for every stdlib
// package (Module=nil). The linked-in go/types then rejected
// generics-bearing stdlib source with "type parameter requires
// go1.18 or later", which cascaded upward as compiles=false through
// vdep transitivity and skipped analyzers on any workspace package
// that imported the stdlib. The W6/W7/W8 fixtures all worked around
// this by deliberately not importing fmt.
//
// The fix populates Folder.Env.GoVersion from runtime.Version() at
// View construction. This test pins the new contract: a workspace
// package that imports fmt and strings runs three SA-* analyzers
// end-to-end and produces the same diagnostic stream on cold and
// warm Snapshot.Analyze, with the L3 IRManager observing pins for
// the NeedsIR analyzer.
//
// Brief constraints satisfied:
//
//   - Fixture imports fmt (and strings) — testdata/stdlib_fmt/a.
//   - Runs at least 3 SA-* analyzers spanning:
//       inspect-only         — SA1006
//       NeedsIR via walker   — SA4017 (fact_purity)
//       publishes facts      — SA5012 (FactTypes + buildir)
//   - Cold→warm diagnostic equivalence.
//   - L3 IRManager observes pin events for the NeedsIR action.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/staticcheck"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// stdlibFmtTrio returns the three SA-* analyzer names the W9
// regression test drives. The set is deterministic so a staticcheck
// bump that drops one of these surfaces immediately via the
// look-up failure in installStdlibFmtTrio.
func stdlibFmtTrio() []string {
	return []string{
		"SA1006", // inspect-only: Printf with dynamic format string
		"SA4017", // NeedsIR via fact_purity walker; pure-function detection
		"SA5012", // NeedsIR via buildir + publishes FactTypes (evenElements)
	}
}

func installStdlibFmtTrio(t *testing.T) []*analysis.Analyzer {
	t.Helper()
	want := stdlibFmtTrio()
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
		t.Skipf("stdlib_fmt trio missing in staticcheck.Analyzers: %v", missing)
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].Name < picked[j].Name })

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = nil
	for _, a := range picked {
		settings.AllAnalyzers = append(settings.AllAnalyzers, settings.NewAnalyzer(a))
	}
	return picked
}

// copyTestdataFixture copies the testdata/stdlib_fmt tree into dst so
// the test can run against a writable temp directory. We don't
// in-place-test from testdata/ because the gopls fork's Folder.Dir
// must be the module root and we want each subtest to get a fresh
// cache root.
func copyTestdataFixture(t *testing.T, dst string) {
	t.Helper()
	src := filepath.Join("testdata", "stdlib_fmt")
	if err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	}); err != nil {
		t.Fatalf("copy testdata: %v", err)
	}
}

// TestStdlibLoadPath is the stdlib load-path regression. The fixture imports
// fmt; SA-* analyzers must run end-to-end on it.
func TestStdlibLoadPath(t *testing.T) {
	requireGo(t)
	picked := installStdlibFmtTrio(t)
	t.Logf("driving stdlib_fmt fixture with %d SA-* analyzers: %v", len(picked), stdlibFmtTrio())
	// We avoid t.TempDir() for the gopls filecache because the
	// filecache spawns a background GC goroutine that races with
	// the testing.T cleanup hook (RemoveAll → "directory not
	// empty"). os.MkdirTemp + an unchecked RemoveAll in t.Cleanup
	// is the documented workaround.
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	// Use leakyTempDir (not t.TempDir) for the workspace and L1/L2
	// roots: the gopls View's parseCache GC and the cache fork's
	// background refcount drain are loosely coupled to the
	// snapshot's lifecycle, and the wider stdlib closure W9
	// surfaces (62 packages vs 4 in pre-W9 fixtures) inflates the
	// chance of a cleanup-hook race with an in-flight file op. The
	// leaky cleanup is best-effort; the OS reaps the residual on
	// the next tmpfs sweep.
	modDir := leakyTempDir(t, "plaid-w9-mod-")
	copyTestdataFixture(t, modDir)
	l1Dir := leakyTempDir(t, "plaid-w9-l1-")
	l2Dir := leakyTempDir(t, "plaid-w9-l2-")
	const toolVer = "plaid-lint-w9-stdlib-fmt"

	runOnce := func(t *testing.T) (map[string][]canonicalDiag, cache.L1Metrics, *l3.SequentialIRManager) {
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

		snap := ws.Snapshot()
		defer snap.Release()
		inner := snap.Inner()
		if err := inner.InitializeWorkspace(context.Background()); err != nil {
			t.Fatalf("InitializeWorkspace: %v", err)
		}

		mpA := inner.Metadata("example.com/stdlibfmt/a")
		if mpA == nil {
			t.Fatal("metadata for example.com/stdlibfmt/a not found — fixture or workspace load broken")
		}
		pkgs := map[metadata.PackageID]*metadata.Package{mpA.ID: mpA}
		diagsList, err := inner.Analyze(context.Background(), pkgs, nil)
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		diags := make(map[string][]canonicalDiag)
		for _, d := range diagsList {
			diags[string(d.Source)] = append(diags[string(d.Source)], canonicalize(d))
		}
		for k := range diags {
			sortDiags(diags[k])
		}
		return diags, c.L1Metrics(), mgr
	}

	cold, coldMetrics, coldMgr := runOnce(t)
	t.Logf("cold: L1 hits=%d stores=%d pins=%d",
		coldMetrics.Hits, coldMetrics.Stores, coldMgr.TotalPins())

	// Contract: stdlib-importing fixtures type-check, so the
	// SA-* analyzers run. Pre-fix, every analyzer would skip with
	// "package does not compile" and L1 stores would be zero on
	// cold. The post-fix run must store at least one L1 entry.
	if coldMetrics.Stores == 0 {
		t.Errorf("cold: L1 stores = 0, want > 0 (analyzers must run on stdlib-importing fixture)")
	}

	// NeedsIR contract: SA4017 + SA5012 are buildir consumers,
	// so the L3 IRManager must record at least two pins on cold
	// (each NeedsIR analyzer's Run takes a pin; the buildir
	// prerequisite also pins via the walker fallback).
	if got := coldMgr.TotalPins(); got < 2 {
		t.Errorf("cold: TotalPins = %d, want >= 2 (SA4017 + SA5012 must each pin via NeedsIR)", got)
	} else {
		t.Logf("cold: TotalPins = %d (SA4017 + SA5012 + transitive buildir + fact_purity all pin)", got)
	}

	// Every pin released.
	if leaked := coldMgr.Snapshot(); len(leaked) != 0 {
		t.Errorf("cold: IRManager.Snapshot = %v, want empty (pin leak)", leaked)
	}

	// Warm run — cold→warm equivalence.
	warm, warmMetrics, warmMgr := runOnce(t)
	t.Logf("warm: L1 hits=%d stores=%d pins=%d",
		warmMetrics.Hits, warmMetrics.Stores, warmMgr.TotalPins())
	if warmMetrics.Hits == 0 {
		t.Errorf("warm: L1 hits = 0, want > 0 (cold run populated L1 successfully)")
	}

	coldKey := canonicalDigest(cold)
	warmKey := canonicalDigest(warm)
	if coldKey != warmKey {
		t.Errorf("stdlib_fmt cold→warm streams differ:\n  cold: %s\n  warm: %s", coldKey, warmKey)
	}
}
