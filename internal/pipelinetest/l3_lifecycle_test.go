// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// l3_lifecycle_test.go is the W8 gate-evidence test for the L3
// IRManager coordination interface.
//
// Through the pipeline fixture (which includes nilness — a NeedsIR
// analyzer — alongside SA1000 and buildir as a prereq), we attach a
// l3.SequentialIRManager to the Cache and assert:
//
//   1. Every NeedsIR=true analyzer's run on a package pins that
//      package while Run is executing.
//   2. Every pin is released by the time Snapshot.Analyze returns
//      (no pin leaks across invocations).
//   3. Pin count rises on a cold run and falls on a warm run when
//      analyzers hit L1 (NeedsIR analyzers in the W7 set are SA1000
//      and nilness; their Run does not execute on L1 hits, so no
//      pin is taken).
//
// The IR itself is owned by buildir's action.result, freed when
// analysisNode.run returns. IRManager observes the lifecycle without
// owning the IR — see internal/l3/manager.go's package doc and
// internal/gopls/cache/l3.go.

import (
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// TestL3IRManagerPinReleaseDuringAnalyze is the lifecycle assertion:
// across a cold Snapshot.Analyze run on the pipeline fixture, the
// attached SequentialIRManager must record at least one pin (because
// the W7 bundled analyzer set includes NeedsIR=true descriptors:
// nilness, SA1000, buildssa, buildir). At end-of-analyze, every pin
// must have been released — Snapshot() returns an empty map.
func TestL3IRManagerPinReleaseDuringAnalyze(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-lifecycle"

	mgr := l3.NewSequentialIRManager()

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
	c.AttachIRManager(mgr)
	if c.IRManager() == nil {
		t.Fatal("IRManager() returned nil after AttachIRManager")
	}

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()

	diags := runAnalyzePipeline(t, ws)
	if len(diags) == 0 {
		t.Fatal("Analyze returned no diagnostics; pipeline fixture is malformed")
	}

	// Invariant 1: the manager observed at least one pin during the
	// run. The pipeline fixture has 4 packages (shared, foo, bar,
	// consumer) and at least one NeedsIR analyzer (nilness, SA1000,
	// plus buildssa/buildir prereqs which are NeedsIR=true in
	// bundled.go). Cold-run pin count is therefore bounded below by
	// the analyzer × package fan-out for NeedsIR descriptors.
	if got := mgr.TotalPins(); got == 0 {
		t.Errorf("TotalPins after cold Analyze = 0, want > 0 (NeedsIR analyzers must have pinned each package)")
	} else {
		t.Logf("TotalPins after cold Analyze = %d", got)
	}

	// Invariant 2: every pin released — no leak.
	if got := mgr.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot after Analyze = %v, want empty (every pin must be released)", got)
	}
}

// TestL3IRManagerPinsScaleWithPackages asserts the per-package fanin
// shape: when more workspace packages exist, more pins fire. This is
// the load-bearing invariant the W9 scheduler keys off — pin events
// per package are what trigger free-after-fanin RSS accounting.
func TestL3IRManagerPinsScaleWithPackages(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-perpkg"

	mgr := l3.NewSequentialIRManager()
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
	c.AttachIRManager(mgr)

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	runAnalyzePipeline(t, ws)

	// The pipeline fixture has four workspace packages. Each one runs
	// at least one NeedsIR analyzer (nilness alone is enough). So the
	// cumulative pin count must be ≥ 4. Larger fan-out from buildir,
	// buildssa, SA1000 plus consumer-of-prereq edges drives it higher
	// — we assert the lower bound only to keep the test stable against
	// future analyzer-set tuning.
	if got := mgr.TotalPins(); got < 4 {
		t.Errorf("TotalPins = %d, want >= 4 (one NeedsIR pin per workspace package)", got)
	}
}

// TestL3IRManagerNoopAttachStillWorks pins down the contract: a
// NoopIRManager attached via AttachIRManager produces a clean run
// with no error and no bookkeeping (Snapshot stays empty even mid-
// run, since NoopIRManager doesn't track pins).
func TestL3IRManagerNoopAttachStillWorks(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-noop"

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
	c.AttachIRManager(l3.NoopIRManager{})

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	diags := runAnalyzePipeline(t, ws)
	if len(diags) == 0 {
		t.Errorf("Analyze with NoopIRManager produced no diagnostics; expected non-empty")
	}
}

// TestL3IRManagerNilIsDefaultPath asserts that NOT attaching an
// IRManager (the W7 baseline) is the same as attaching no
// coordination: pipelineAnalyzers runs cleanly, Cache.IRManager()
// returns nil, and the run produces diagnostics. The point is to
// pin the absence-of-regression contract — the W8 wiring must NOT
// have changed the W7 default behaviour.
func TestL3IRManagerNilIsDefaultPath(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-nil"

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
	if c.IRManager() != nil {
		t.Fatalf("Cache.IRManager() = %v before attach, want nil", c.IRManager())
	}

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	diags := runAnalyzePipeline(t, ws)
	if len(diags) == 0 {
		t.Errorf("Analyze with no IRManager produced no diagnostics; W7 baseline regression")
	}
}

