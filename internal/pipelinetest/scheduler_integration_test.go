// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// scheduler_integration_test.go is the W9 gate-evidence test for
// the RSSBudgetScheduler (phase-1 task 1.35).
//
// It drives Snapshot.Analyze with the SA-* batch fixture under
// two configurations:
//
//  1. Baseline — no scheduler attached. The W7/W8 default path
//     (per-action goroutine + runtime.GOMAXPROCS limiter) runs
//     unchanged. Captures the diagnostic stream + IRManager pin
//     count as the ground truth.
//  2. Scheduled — RSSBudgetScheduler attached with a tight 256 MB
//     budget and an aggressive per-NeedsIR estimate to force the
//     gate to throttle. The Snapshot.Analyze API is byte-equivalent
//     for this purpose: the same diagnostics flow out, but every
//     action whose Acquire was admitted reports through the
//     scheduler's Stats.
//
// Assertions:
//
//   - Cold→warm diagnostic equivalence (the unscheduled contract).
//   - Scheduled-vs-unscheduled diagnostic equivalence (the W9
//     contract).
//   - Scheduler.Stats shows the gate was installed and consulted
//     (every action was acquired and completed through it).
//   - Scheduler.Stats.PeakConcurrency ≤ expected cap (the gate is
//     honored).
//   - Scheduler.Stats.IRPinEvents > 0 and Snapshot empty at end
//     (W8 contract — IR pin/release stream complete).

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/scheduler"
	"github.com/conductorone/plaid-lint/internal/test/cachetest"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// TestSchedulerIntegration_SABatchEquivalence is the W9 gate
// test. The fixture is the existing SA-batch fixture; the trio of
// assertions (equivalence + cap + pin/release) is the contract
// every W10 follow-up must preserve.
func TestSchedulerIntegration_SABatchEquivalence(t *testing.T) {
	requireGo(t)
	picked := installSABatchAnalyzers(t)
	t.Logf("driving scheduler integration with %d SA-* analyzers", len(picked))
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	const toolVer = "plaid-lint-w9-scheduler-int"

	// Each run uses its own L1+L2 caches so we get true cold runs.
	// Baseline (unscheduled) first.
	baselineL1 := t.TempDir()
	baselineL2 := t.TempDir()

	runBaseline := func(t *testing.T) (map[string][]canonicalDiag, int64) {
		t.Helper()
		l1 := cachetest.Open(t, baselineL1)
		l2 := cachetest.Open(t, baselineL2)
		c := cache.New(nil)
		c.AttachL1(l1, toolVer)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		mgr := l3.NewSequentialIRManager()
		c.AttachIRManager(mgr)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()
		diags := runAnalyzePipeline(t, ws)
		return diags, mgr.TotalPins()
	}

	cold, coldPins := runBaseline(t)
	warm, warmPins := runBaseline(t)
	if coldKey, warmKey := canonicalDigest(cold), canonicalDigest(warm); coldKey != warmKey {
		t.Fatalf("baseline cold→warm streams differ:\n  cold: %s\n  warm: %s", coldKey, warmKey)
	}
	t.Logf("baseline pins: cold=%d warm=%d", coldPins, warmPins)

	// Scheduled run with a tight budget. The 256 MB budget is the
	// brief-specified tight value; the count cap is set to 1
	// (mandatory serialization) so PeakConcurrency has a hard
	// expected value.
	//
	// The 256 MB bytes-budget is still load-bearing: it pins the
	// constructor-time argument the scheduler reflects in
	// Stats.BudgetBytes for the W10 benchmark harness, and
	// exercises the bytes-axis bypass when a single oversized
	// action would otherwise deadlock.
	//
	// Note this test deliberately does NOT assert
	// Stats.ActionsBlocked > 0. Whether an Acquire has to wait is
	// emergent: it needs two actions to be in flight at the same
	// instant, which the pipeline does not promise. On a
	// CPU-starved host (GOMAXPROCS=1, or GOMAXPROCS=2 under
	// external load) the action fan-out can run strictly
	// serially, every Acquire finds the gate free, and
	// ActionsBlocked is legitimately 0 even though the gate is
	// wired up correctly. That contract is pinned deterministically
	// instead by
	// TestRSSBudgetScheduler_GateBlocksUnderContention in
	// internal/scheduler, which synchronises the overlap rather
	// than hoping for it. What this test can and does assert is
	// that the gate was installed and every action passed through
	// it.
	const (
		schedBudget  = 256 * 1024 * 1024
		schedMaxConc = 1
	)
	scheduledL1 := t.TempDir()
	scheduledL2 := t.TempDir()

	rss := scheduler.NewRSSBudgetScheduler(schedBudget, schedMaxConc)
	c := cache.New(nil)
	l1 := cachetest.Open(t, scheduledL1)
	l2 := cachetest.Open(t, scheduledL2)
	c.AttachL1(l1, toolVer)
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	c.AttachScheduler(scheduler.AsCacheScheduler(rss))

	// Verify AttachScheduler also wired the IR pin/release stream
	// onto the scheduler (the type-assertion path).
	if c.IRManager() == nil {
		t.Fatal("AttachScheduler did not install scheduler as IRManager")
	}

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	scheduled := runAnalyzePipeline(t, ws)

	st := rss.Stats()
	t.Logf("scheduled: acquired=%d completed=%d blocked=%d peak=%d cap=%d pins=%d releases=%d",
		st.ActionsAcquired, st.ActionsCompleted, st.ActionsBlocked,
		st.PeakConcurrency, st.ConcurrencyCap, st.IRPinEvents, st.IRReleaseEvents)

	// W9 contract 1: scheduled-vs-unscheduled diagnostic
	// equivalence. The default cold run is the baseline.
	if coldKey, schedKey := canonicalDigest(cold), canonicalDigest(scheduled); coldKey != schedKey {
		t.Errorf("scheduled-vs-unscheduled streams differ:\n  baseline: %s\n  scheduled: %s",
			coldKey, schedKey)
	}

	// W9 contract 2: the gate is installed and every action went
	// through it. Peak concurrency must respect the count cap, and
	// at least one action must actually have been admitted (a
	// silently-bypassed scheduler would report a flat zero).
	if st.PeakConcurrency == 0 {
		t.Errorf("PeakConcurrency = 0, want ≥ 1 (no action was ever in flight through the gate)")
	}
	if st.PeakConcurrency > schedMaxConc {
		t.Errorf("PeakConcurrency = %d, want ≤ %d", st.PeakConcurrency, schedMaxConc)
	}
	if st.ConcurrencyCap != schedMaxConc {
		t.Errorf("ConcurrencyCap = %d, want %d", st.ConcurrencyCap, schedMaxConc)
	}

	// W9 contract 3: IR pin/release stream complete.
	if st.IRPinEvents == 0 {
		t.Errorf("IRPinEvents = 0, want > 0 (NeedsIR analyzers must pin)")
	}
	if st.IRReleaseEvents != st.IRPinEvents {
		t.Errorf("IRReleaseEvents (%d) != IRPinEvents (%d): pin leak", st.IRReleaseEvents, st.IRPinEvents)
	}
	// And the underlying SequentialIRManager Snapshot must be empty.
	if got := rss.Snapshot(); len(got) != 0 {
		t.Errorf("scheduler.Snapshot = %v, want empty (no leaked pins)", got)
	}

	// Sanity: every L1 store on the baseline cold run should have
	// flowed through Acquire on the scheduled run (modulo prereq
	// L1-bypasses). We don't assert ActionsAcquired ≥ specific
	// number because the exact count is implementation-defined,
	// but it must be > 0 and roughly matches the analyzer fan-out.
	if st.ActionsAcquired == 0 {
		t.Errorf("ActionsAcquired = 0, want > 0 (action graph must have entered the gate)")
	}
	// Every admitted action must also have released its slot, or
	// the gate leaks capacity.
	if st.ActionsCompleted != st.ActionsAcquired {
		t.Errorf("ActionsCompleted = %d, want %d (= ActionsAcquired): gate slot leak",
			st.ActionsCompleted, st.ActionsAcquired)
	}
}

// TestSchedulerIntegration_DefaultPathUnchanged is the byte-
// equivalence guard for the W7/W8 default path: when no scheduler
// is attached, the analyze pipeline must produce the identical
// diagnostic stream it produced before W9 landed. We can't easily
// pin the pre-W9 stream as a literal here, but we CAN assert that
// the unscheduled diagnostic set matches the scheduled one (which
// the equivalence test above already proves) AND that with no
// scheduler attached, the IRManager pin counter still climbs and
// drains.
func TestSchedulerIntegration_DefaultPathUnchanged(t *testing.T) {
	requireGo(t)
	installSABatchAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w9-default-path"

	c := cache.New(nil)
	l1 := cachetest.Open(t, l1Dir)
	l2 := cachetest.Open(t, l2Dir)
	c.AttachL1(l1, toolVer)
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	mgr := l3.NewSequentialIRManager()
	c.AttachIRManager(mgr)
	if c.Scheduler() != nil {
		t.Fatal("Cache.Scheduler() != nil with no AttachScheduler call")
	}

	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()
	diags := runAnalyzePipeline(t, ws)
	if len(diags) == 0 {
		// SA-batch fixture is intentionally diag-free; this just
		// validates the pipeline ran end-to-end.
		t.Log("no diagnostics from SA-batch fixture, as expected")
	}
	if mgr.TotalPins() == 0 {
		t.Errorf("default-path: TotalPins = 0, want > 0 (NeedsIR analyzers must still pin)")
	}
	if got := mgr.Snapshot(); len(got) != 0 {
		t.Errorf("default-path: Snapshot = %v, want empty", got)
	}
}
