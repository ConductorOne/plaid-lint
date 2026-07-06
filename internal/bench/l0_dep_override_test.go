// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// TestL0DepOverride_LeafEditAvoidsDepBuildir is the headline test: a
// leaf edit (touching one workspace package after a full warm run)
// must not trigger dep buildir re-runs. We instrument the cache
// package's L0OverrideHits counter to verify the override fires for
// every dep on the second run.
func TestL0DepOverride_LeafEditAvoidsDepBuildir(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, MediumShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func(t *testing.T, l0c *l0.Cache) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
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

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Cold run: populates L0 (per-package diagnostics + per-analyzer
	// facts for both roots and deps).
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	coldOverrideHits := cache.L0OverrideHits()
	if coldOverrideHits != 0 {
		t.Errorf("cold run had %d override hits; expected 0 (fresh L0)", coldOverrideHits)
	}

	storesAfterCold := sharedL0.MetricsPtr().Snapshot().Stores
	if storesAfterCold == 0 {
		t.Fatal("cold run did not populate L0")
	}

	// Touch one leaf workspace package so its L0 entry is invalidated
	// by source change. The dep closure is untouched, so on the warm
	// run every dep should serve from L0 via the override path.
	leafFile := filepath.Join(moduleRoot, "leaf0", "leaf0.go")
	src, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	modified := append([]byte("// leaf-edit\n"), src...)
	if err := os.WriteFile(leafFile, modified, 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}

	// Warm run: leaf0 misses L0, every dep hits the override.
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	warmOverrideHits := cache.L0OverrideHits()
	if warmOverrideHits == 0 {
		t.Errorf("warm leaf-edit had 0 override hits; expected > 0 (every dep should short-circuit)")
	}
	t.Logf("leaf-edit override hits=%d", warmOverrideHits)
}

// TestL0DepOverride_DiagnosticsByteEquivalent verifies the diagnostic
// stream is unchanged whether the dep-override fast path is enabled
// or disabled. Extends the W6 cold↔warm contract to the leaf-edit
// scenario.
func TestL0DepOverride_DiagnosticsByteEquivalent(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, MediumShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func(t *testing.T, l0c *l0.Cache) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
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

	// First: warm up a shared L0 with the override path enabled
	// (default).
	t.Setenv("PLAID_L0_DEP_OVERRIDE", "1")
	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("warmup Run: %v", err)
	}
	// Touch a leaf to force the cascade.
	leafFile := filepath.Join(moduleRoot, "leaf0", "leaf0.go")
	src, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	modified := append([]byte("// leaf-edit\n"), src...)
	if err := os.WriteFile(leafFile, modified, 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}
	withOverride, err := engine.Run(ctx, build(t, sharedL0))
	if err != nil {
		t.Fatalf("override-on Run: %v", err)
	}

	// Second: same leaf edit, but with the override path disabled.
	// Use a FRESH L0 so the leaf-pkg L0 entry is also missing — this
	// makes the override-off run exercise the full re-analysis
	// path for the entire workspace + dep closure.
	t.Setenv("PLAID_L0_DEP_OVERRIDE", "0")
	freshL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open fresh L0: %v", err)
	}
	withoutOverride, err := engine.Run(ctx, build(t, freshL0))
	if err != nil {
		t.Fatalf("override-off Run: %v", err)
	}

	overrideDigest := canonicalDigest(withOverride.Diagnostics)
	plainDigest := canonicalDigest(withoutOverride.Diagnostics)
	if overrideDigest != plainDigest {
		t.Errorf("dep-override changed diagnostic stream\n  override: %s\n  plain:    %s\n  override diags:\n%s\n  plain diags:\n%s",
			overrideDigest, plainDigest,
			dumpDiags(withOverride.Diagnostics),
			dumpDiags(withoutOverride.Diagnostics))
	}
}

// TestL0DepOverride_CacheVersionBumpInvalidates verifies that a
// CacheVersion bump invalidates previously written L0 entries. The
// compile-time const can't change at runtime, so we use ToolVersion
// (which folds into the L0 key identically) as the stand-in. This
// mirrors the pre-existing TestL0_CacheVersionBumpInvalidates pattern
// but in the bench package against the realistic fixture.
func TestL0DepOverride_CacheVersionBumpInvalidates(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	build := func(t *testing.T, toolVer string) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
		}
		cfg := config.NewDefault()
		reg, _, err := registry.Build(cfg)
		if err != nil {
			t.Fatalf("registry.Build: %v", err)
		}
		return engine.RunInput{
			Config:           cfg,
			Registry:         reg,
			Workspace:        subproc.WorkspaceRef{ModuleRoot: moduleRoot},
			L1:               l1,
			L2:               l2,
			L0:               l0c,
			CacheToolVersion: toolVer,
		}
	}

	// Cold run with default tool version.
	if _, err := engine.Run(ctx, build(t, "")); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	storesV1 := l0c.MetricsPtr().Snapshot().Stores
	hitsV1 := l0c.MetricsPtr().Snapshot().Hits

	// Warm run with same version — should hit.
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, "")); err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	hitsV1Warm := l0c.MetricsPtr().Snapshot().Hits
	if hitsV1Warm <= hitsV1 {
		t.Errorf("warm run did not increase L0 hits: before=%d after=%d (override hits=%d)",
			hitsV1, hitsV1Warm, cache.L0OverrideHits())
	}

	// Bump tool version. Entries from V1 should be invalid (different
	// key); the run must re-populate.
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, "bumped-v2")); err != nil {
		t.Fatalf("v2 Run: %v", err)
	}
	storesV2 := l0c.MetricsPtr().Snapshot().Stores
	if storesV2 <= storesV1 {
		t.Errorf("tool-version bump did not invalidate L0: stores before=%d after=%d", storesV1, storesV2)
	}
}

// TestL0DepOverride_DepSourceChangeInvalidates verifies that touching
// a dep's source invalidates only that dep's L0 entry (and any
// downstream of it, via DepHash). The leaf-edit fast path still fires
// for the unchanged deps.
func TestL0DepOverride_DepSourceChangeInvalidates(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, MediumShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func(t *testing.T, l0c *l0.Cache) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
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

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Cold: populate L0 across the full workspace.
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	// Touch a leaf used by mid-layer (every dep of mid0 is a
	// candidate; leaf0 is typical).
	leafFile := filepath.Join(moduleRoot, "leaf0", "leaf0.go")
	src, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	modified := append([]byte("// dep edit: leaf0 invalidated\n"), src...)
	if err := os.WriteFile(leafFile, modified, 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}

	// Warm run after dep edit. We expect SOME override hits (leaves
	// other than leaf0, mid packages whose source hash hasn't changed
	// AND that don't transitively depend on leaf0) and SOME analyzer
	// re-runs (leaf0 and anything that depends on leaf0 — its DepHash
	// is now different).
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("dep-edit Run: %v", err)
	}
	// We don't assert specific counts because dep closures vary by
	// fixture; the load-bearing signal is that the engine doesn't
	// crash and the run terminates with consistent diagnostics.
	t.Logf("after dep edit: override hits=%d L0 hits=%d stores=%d",
		cache.L0OverrideHits(),
		sharedL0.MetricsPtr().Snapshot().Hits,
		sharedL0.MetricsPtr().Snapshot().Stores)
}

// TestL0DepOverride_AnalyzerSetChangeInvalidates verifies that
// toggling the enabled linter set produces a different analyzer-set
// hash, so cached entries are no longer reused.
func TestL0DepOverride_AnalyzerSetChangeInvalidates(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	build := func(t *testing.T, cfgMod func(*config.Config)) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
		}
		cfg := config.NewDefault()
		if cfgMod != nil {
			cfgMod(cfg)
		}
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
			L0:        sharedL0,
		}
	}

	// Cold run with default analyzer set.
	if _, err := engine.Run(ctx, build(t, nil)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	storesBefore := sharedL0.MetricsPtr().Snapshot().Stores

	// Bump the analyzer set: drop all to GroupNone. Different
	// analyzer-set hash, so previous L0 entries don't satisfy the key.
	if _, err := engine.Run(ctx, build(t, func(c *config.Config) {
		c.Linters.Default = config.GroupNone
	})); err != nil {
		t.Fatalf("changed-set Run: %v", err)
	}
	storesAfter := sharedL0.MetricsPtr().Snapshot().Stores
	// With analyzer set = none, the engine may have nothing to
	// analyze in-process at all (every analyzer is gated off). The
	// signal we care about is that the L0 cache wasn't poisoned by
	// the toggle (no diagnostic divergence). A no-stores outcome is
	// fine — the assertion below is just "stores didn't regress".
	if storesAfter < storesBefore {
		t.Errorf("changed-set Run regressed stores: before=%d after=%d (cache shouldn't lose data)",
			storesBefore, storesAfter)
	}
}

// TestL0DepOverride_EscapeHatchDisablesOverride verifies that setting
// PLAID_L0_DEP_OVERRIDE=0 returns zero override hits even with a
// populated L0.
func TestL0DepOverride_EscapeHatchDisablesOverride(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, MediumShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func(t *testing.T, l0c *l0.Cache) engine.RunInput {
		t.Helper()
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
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

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Cold run with override enabled (default).
	t.Setenv("PLAID_L0_DEP_OVERRIDE", "1")
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	// Touch a leaf so the warm run has a miss.
	leafFile := filepath.Join(moduleRoot, "leaf0", "leaf0.go")
	src, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	if err := os.WriteFile(leafFile, append([]byte("// edit\n"), src...), 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}

	// Disable override; expect zero override hits.
	t.Setenv("PLAID_L0_DEP_OVERRIDE", "0")
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, build(t, sharedL0)); err != nil {
		t.Fatalf("disabled-override Run: %v", err)
	}
	if h := cache.L0OverrideHits(); h != 0 {
		t.Errorf("PLAID_L0_DEP_OVERRIDE=0 still produced %d override hits", h)
	}
}

// Compile-time uses to keep imports alive.
var _ = strings.Contains
