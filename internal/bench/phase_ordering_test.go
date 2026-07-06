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

// TestPhaseOrdering_NoTypecheckDuringAnalyze pins the load-bearing
// invariant of the barrier: once the batch transitions from load
// phase to analyze phase, no typesyncmu writer (gcimporter
// declare; L2-read; etc.) fires. The barrier's structural claim is
// that all typechecks complete BEFORE any analyze starts, so the
// analyze phase observes a frozen *types.Scope graph.
//
// typesyncmu.LockCallsDuringAnalyze counts Lock calls observed while
// the global analyzePhaseCount > 0. With the flag ON by default,
// the count MUST be zero across a full engine.Run on the cascade-
// shaped fixture.
func TestPhaseOrdering_NoTypecheckDuringAnalyze(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	in := newRunInputForPhaseOrdering(t, moduleRoot)

	cache.ResetTypesyncCounters()
	if _, err := engine.Run(ctx, in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, _, lockDuringAnalyze := cache.TypesyncCounters()
	if lockDuringAnalyze != 0 {
		t.Errorf("typesyncmu.Lock fired %d times during analyze phase; want 0 (barrier invariant)", lockDuringAnalyze)
	}
}

// TestPhaseOrdering_RLockElidedDuringAnalyze pins the wall-win
// mechanism: once the load phase closes, RLock callers should hit the
// no-op path (skipped) instead of acquiring mu. typesyncmu's
// rlockSkipped counter increments on every elided RLock; rlockHeld
// increments on every acquired RLock.
//
// The barrier closes loadPhaseCount to zero after every typecheck
// signals, so analyzer-Run RLock calls should observe count=0 and
// elide. Under the cascade workload the rlockSkipped count should
// dominate rlockHeld; a hard threshold is fragile (cross-batch
// interleavings can vary), so we assert the more robust shape
// "skipped > 0 AND skipped >= held / 2" — under flag=ON we expect
// skipped to vastly exceed held, but at minimum half is a generous
// floor that catches the regression where the barrier silently fails
// to close.
func TestPhaseOrdering_RLockElidedDuringAnalyze(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	in := newRunInputForPhaseOrdering(t, moduleRoot)

	cache.ResetTypesyncCounters()
	if _, err := engine.Run(ctx, in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	held, skipped, _, _ := cache.TypesyncCounters()
	if skipped == 0 {
		t.Errorf("typesyncmu RLock skipped=%d; want > 0 (barrier never closed load phase)", skipped)
	}
	if skipped < held/2 {
		t.Errorf("typesyncmu RLock skipped=%d held=%d; want skipped >= held/2 (barrier closed too late)", skipped, held)
	}
	t.Logf("typesyncmu RLock counters: held=%d skipped=%d (elision rate=%.2f%%)",
		held, skipped, 100*float64(skipped)/float64(held+skipped))
}

// TestPhaseOrdering_NoDeadlock_CascadeFixture is the explicit hedge
// against the deadlock recurring. The barrier waits for every
// node to signal typecheck-done before opening; the prior attempt
// deadlocked because preds couldn't typecheck until succs finished
// analyze. The input-based cacheKey and early unfinishedSuccs
// decrement together break that wait cycle.
//
// Strategy: run the cascade workload 5× sequentially with a hard
// deadline. If any iteration hangs the test fails on context timeout.
// The deadline must be generous enough to absorb cold-mode noise
// (~30s per iteration on CascadeShape) but tight enough to surface a
// real deadlock — 180s for 5 iterations gives ~36s each, well above
// the observed 30s ceiling.
func TestPhaseOrdering_NoDeadlock_CascadeFixture(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		in := newRunInputForPhaseOrdering(t, moduleRoot)
		if _, err := engine.Run(ctx, in); err != nil {
			t.Fatalf("Run iter=%d: %v", i, err)
		}
	}
}

// newRunInputForPhaseOrdering constructs a fresh RunInput with per-
// run L1/L2/L0 caches. The fresh caches force engine.Run through the
// cache-miss path so the barrier actually fires (a fully-warm hit
// path bypasses typecheck entirely).
func newRunInputForPhaseOrdering(t *testing.T, moduleRoot string) engine.RunInput {
	t.Helper()
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
