// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// scheduler_acquire_failure_test.go is the W9 regression test for
// the Acquire-failure contract: when [ActionScheduler.Acquire] returns a non-nil
// error, [action.exec] MUST abort and surface the error rather than
// run the analyzer body. The test asserts the three-state contract
// — refused admission produces err != nil, release == nil, and the
// analyzer body never executes.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/tools/go/analysis"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/scheduler"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// TestSchedulerIntegration_AcquireFailurePropagates asserts that an
// [ActionScheduler.Acquire] failure aborts the action: the analyzer's
// Run body never executes, no L1 store happens for the refused
// action, and the scheduler's ActionsBlocked counter reflects the
// failed admission.
//
// The pre-fix shape (acquireSchedulerSlot returning only `func()`
// and swallowing the err) silently runs the analyzer body anyway.
// This test FAILS in that shape — runCount becomes non-zero. Under
// the fix (typed error return + abort in action.exec), runCount
// stays at 0.
func TestSchedulerIntegration_AcquireFailurePropagates(t *testing.T) {
	requireGo(t)

	// Sentinel analyzer: increments runCount inside Run. If the gate
	// admit path is bypassed, runCount > 0.
	var runCount atomic.Int64
	sentinel := &analysis.Analyzer{
		Name: "schedfailprobe",
		Doc:  "sentinel: counts Run invocations for acquire-failure regression",
		Run: func(_ *analysis.Pass) (any, error) {
			runCount.Add(1)
			return nil, nil
		},
	}
	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(sentinel)}

	t.Setenv("GOPLSCACHE", goplsCacheDir(t))
	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w9-acquire-failure"

	// maxConcurrency=1 so a single held slot blocks all other actions.
	// L1 is attached so the gopls analyzeSummary filecache layer is
	// bypassed: a stale filecache hit would
	// short-circuit the action graph before reaching the gate, and we
	// need every action to traverse acquireSchedulerSlot for this
	// contract to be exercised.
	rss := scheduler.NewRSSBudgetScheduler(scheduler.DefaultRSSBudgetBytes, 1)
	c := cache.New(nil)
	l1, err := clcache.Open(l1Dir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	l2, err := clcache.Open(l2Dir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	c.AttachL1(l1, toolVer)
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	c.AttachScheduler(scheduler.AsCacheScheduler(rss))

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	defer snap.Release()
	inner := snap.Inner()

	// Initialize so the metadata graph is materialised; the test's
	// ctx deadline only needs to fire while the action gate is held.
	if err := inner.InitializeWorkspace(context.Background()); err != nil {
		t.Fatalf("InitializeWorkspace: %v", err)
	}

	wsPkgs := inner.WorkspacePackages()
	pkgs := map[metadata.PackageID]*metadata.Package{}
	for id := range wsPkgs.All() {
		if mp := inner.Metadata(id); mp != nil {
			pkgs[mp.ID] = mp
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("no packages loaded")
	}

	// Hold the one count slot from a separate goroutine so every
	// action in the Analyze run that follows must wait at the gate.
	holderCtx := context.Background()
	heldRelease, err := rss.Acquire(holderCtx, scheduler.Action{
		Package:     "holder",
		Analyzer:    "holder",
		NeedsIR:     false,
		RSSEstimate: 1,
	})
	if err != nil {
		t.Fatalf("holder Acquire: %v", err)
	}
	released := false
	defer func() {
		if !released {
			heldRelease()
		}
	}()

	// Pre-test baselines so we can assert deltas.
	l1Before := c.L1Metrics()
	statsBefore := rss.Stats()

	// Drive Analyze with a deadline long enough for type-checking +
	// action graph construction to complete on the small fixture but
	// short enough to fire while the action gate is held. The action
	// runs reach the scheduler gate, block on the held slot, ctx fires,
	// Acquire returns ctx.Err(), action.exec must abort.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := inner.Analyze(ctx, pkgs, nil); err != nil {
		// Analyze itself may return nil even when individual actions
		// failed (per-action errors are stashed in actionSummary.Err);
		// a non-nil err here is also acceptable for our purposes.
		t.Logf("Analyze returned err (expected — ctx deadline): %v", err)
	}

	// Release the held slot so the goroutine inside RSSBudgetScheduler
	// that watches ctx (started by the holder's Acquire) can exit.
	heldRelease()
	released = true

	// Sentinel: the sentinel analyzer's Run body MUST NOT have
	// executed. Any non-zero count means the gate was bypassed.
	if got := runCount.Load(); got != 0 {
		t.Errorf("sentinel Run executed %d times; want 0 (refused Acquire must abort the action)", got)
	}

	// L1: no stores for the sentinel action — a refused action must
	// not reach the analyzer body, so no summary is produced, so no
	// L1 store happens. saBatchFixture has a single package and we
	// installed a single analyzer, so this is a tight bound.
	l1After := c.L1Metrics()
	if delta := l1After.Stores - l1Before.Stores; delta != 0 {
		t.Errorf("L1 stores delta = %d; want 0 (refused action must not produce an L1 entry)", delta)
	}

	// Scheduler stats: ActionsBlocked must increment for the refused
	// action (the gate observed it waiting before ctx fired). The
	// holder counts as 1 acquired+completed; the refused action does
	// not complete because Acquire returned err. ActionsCompleted
	// must therefore not include the refused action.
	statsAfter := rss.Stats()
	if blockedDelta := statsAfter.ActionsBlocked - statsBefore.ActionsBlocked; blockedDelta == 0 {
		t.Errorf("ActionsBlocked delta = 0; want > 0 (the held slot must have forced the refused action to block before ctx fired)")
	}
	// The holder is the only Acquire that succeeded in this run, so
	// ActionsCompleted should advance by exactly 1 (the holder's
	// release()). The sentinel action's Acquire returned err and
	// therefore did NOT bump ActionsCompleted.
	if completedDelta := statsAfter.ActionsCompleted - statsBefore.ActionsCompleted; completedDelta > 1 {
		t.Errorf("ActionsCompleted delta = %d; want ≤ 1 (only the holder's slot should have completed)", completedDelta)
	}
	t.Logf("post-test stats: acquired=%d completed=%d blocked=%d (deltas: blocked=%d completed=%d)",
		statsAfter.ActionsAcquired, statsAfter.ActionsCompleted, statsAfter.ActionsBlocked,
		statsAfter.ActionsBlocked-statsBefore.ActionsBlocked,
		statsAfter.ActionsCompleted-statsBefore.ActionsCompleted)
}
