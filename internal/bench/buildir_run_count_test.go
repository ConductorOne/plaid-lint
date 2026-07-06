// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestBuildirRunsOncePerPackage pins the Phase B finding:
// the action graph's per-(package, *analysis.Analyzer) dedupe (in
// mkAction's actions map) already ensures buildir's Run body executes
// exactly once per package — regardless of how many SA*/QF analyzers
// in the same package declare buildir.Analyzer as a Requires
// prerequisite.
//
// The contract Phase B was scoped to deliver (a per-batch irCache
// keyed on (ph.key, "buildir")) is therefore already in place. This
// test makes the invariant assertable so future refactors can't
// silently regress it.
//
// Strategy:
//  1. Build a SmallShape fixture (3 leaves + 2 mid + 1 root = 6
//     packages). Default config enables staticcheck, which expands to
//     ~95 SA* analyzers, many of which list buildir.Analyzer as a
//     Requires prerequisite (via fact_purity, ctrlflow, or directly).
//  2. Reset the run-count counter, force a fresh L0 so every package
//     enters Analyze, run engine.Run.
//  3. Read AnalyzerRunCount("buildir") and assert it equals the number
//     of analysisNodes the run created — which, with a fresh L0 + no
//     test files, is exactly the workspace package count.
//
// The assertion is "==", not "<=": if buildir ran fewer times than
// expected we'd have a different regression (analyzer not wired);
// if it ran more times the dedupe is broken.
func TestBuildirRunsOncePerPackage(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func() engine.RunInput {
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
		}
		l0c, err := l0.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open L0: %v", err)
		}
		cfg := config.NewDefault()
		reg, _, err := registry.Build(cfg)
		if err != nil {
			t.Fatalf("registry.Build: %v", err)
		}
		return engine.RunInput{
			Config:    cfg,
			Registry:  reg,
			Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
			L1:        l1,
			L2:        l2,
			L0:        l0c,
		}
	}

	// Cold run: fresh L0 means every workspace package's analysisNode
	// runs through Analyze. The per-action dedupe inside
	// analysisNode.run makes buildir's Run body fire once per node.
	cache.ResetAnalyzerRunCounts()
	if _, err := engine.Run(ctx, build()); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	got := cache.AnalyzerRunCount("buildir")
	// SmallShape = 3 leaves + 2 mid + 1 root = 6 packages. Every
	// analysisNode runs buildir exactly once (fact_purity / SA*'s
	// transitive Requires path pulls it in).
	const wantPackages = int64(6)
	if got != wantPackages {
		// Dump every analyzer count for diagnostic context — the
		// failure mode is either "dedupe regressed" (got > wantPackages)
		// or "fixture changed" (got != wantPackages but the workspace
		// count shifted). Both are debuggable from the dump.
		t.Errorf("buildir ran %d times across cold SmallShape engine.Run; want %d (per-package dedupe should make this exactly equal to the workspace package count)",
			got, wantPackages)
		counts := cache.AnalyzerRunCounts()
		t.Logf("all analyzer run counts (len=%d):", len(counts))
		for name, n := range counts {
			t.Logf("  %s: %d", name, n)
		}
	}

	// Spot-check related IR-producing prereqs: they should run at most
	// the same number of times as buildir (also per-package dedupe).
	for _, name := range []string{"ctrlflow", "buildssa", "inspect"} {
		if n := cache.AnalyzerRunCount(name); n > wantPackages {
			t.Errorf("prereq %s ran %d times; expected <= %d (per-package dedupe should apply uniformly to every prereq)", name, n, wantPackages)
		}
	}
}
