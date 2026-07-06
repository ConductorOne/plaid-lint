// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRSSBudgetScheduler_AcquireRelease pins the basic Acquire/Release
// contract: an action is admitted, a release function is returned,
// calling it twice is a no-op, and Stats reports the counts.
func TestRSSBudgetScheduler_AcquireRelease(t *testing.T) {
	s := NewRSSBudgetScheduler(1<<20, 4)
	act := Action{Package: "p", Analyzer: "A", RSSEstimate: 1024}

	rel, err := s.Acquire(context.Background(), act)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if rel == nil {
		t.Fatal("Acquire returned nil release")
	}
	if got := s.Stats(); got.ActionsAcquired != 1 || got.ActionsCompleted != 0 {
		t.Errorf("Stats post-acquire = %+v, want Acquired=1 Completed=0", got)
	}
	rel()
	// Idempotent release.
	rel()
	if got := s.Stats(); got.ActionsAcquired != 1 || got.ActionsCompleted != 1 {
		t.Errorf("Stats post-release = %+v, want Acquired=1 Completed=1", got)
	}
}

// TestRSSBudgetScheduler_ConcurrencyCapBytes pins the bytes-axis
// budget gate: with a 256 MB budget and 8 actions each estimated at
// 128 MB, only two can be in-flight at once. The test asserts the
// observed PeakConcurrency never exceeds 2.
func TestRSSBudgetScheduler_ConcurrencyCapBytes(t *testing.T) {
	const (
		budget  uint64 = 256 * 1024 * 1024
		perAct  uint64 = 128 * 1024 * 1024
		nActs          = 8
		maxConc        = 32 // intentionally high so the cap is bytes-driven
	)
	s := NewRSSBudgetScheduler(budget, maxConc)

	var inFlight atomic.Int64
	var maxObserved atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < nActs; i++ {
		wg.Add(1)
		act := Action{
			Package:     PackageID(fmt.Sprintf("p%d", i)),
			Analyzer:    "A",
			RSSEstimate: perAct,
		}
		go func() {
			defer wg.Done()
			rel, err := s.Acquire(context.Background(), act)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			n := inFlight.Add(1)
			for {
				cur := maxObserved.Load()
				if n <= cur || maxObserved.CompareAndSwap(cur, n) {
					break
				}
			}
			// Hold the slot briefly so the cap is observable.
			time.Sleep(5 * time.Millisecond)
			inFlight.Add(-1)
			rel()
		}()
	}
	wg.Wait()

	if got := maxObserved.Load(); got > 2 {
		t.Errorf("max observed concurrency = %d, want ≤ 2 under bytes budget", got)
	}
	st := s.Stats()
	if st.ActionsAcquired != nActs {
		t.Errorf("ActionsAcquired = %d, want %d", st.ActionsAcquired, nActs)
	}
	if st.ActionsCompleted != nActs {
		t.Errorf("ActionsCompleted = %d, want %d", st.ActionsCompleted, nActs)
	}
	if st.ActionsBlocked == 0 {
		t.Errorf("ActionsBlocked = 0, want > 0 (bytes gate must have throttled at least one Acquire)")
	}
	if st.PeakConcurrency > 2 {
		t.Errorf("Stats.PeakConcurrency = %d, want ≤ 2", st.PeakConcurrency)
	}
}

// TestRSSBudgetScheduler_OversizedActionAdmitted asserts the
// deadlock-avoidance rule: a single action whose estimate exceeds
// the entire budget is still admitted (the bytes-gate bypass when
// inFlight==0).
func TestRSSBudgetScheduler_OversizedActionAdmitted(t *testing.T) {
	s := NewRSSBudgetScheduler(1024, 4)
	act := Action{Package: "p", Analyzer: "huge", RSSEstimate: 16 * 1024}

	done := make(chan struct{})
	go func() {
		rel, err := s.Acquire(context.Background(), act)
		if err != nil {
			t.Errorf("Acquire: %v", err)
		}
		rel()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Acquire of oversized action deadlocked")
	}
}

// TestRSSBudgetScheduler_CountCap asserts the count-axis cap (max
// concurrency) is binding when the bytes-axis budget is disabled
// (budgetBytes=0).
func TestRSSBudgetScheduler_CountCap(t *testing.T) {
	const cap = 3
	const nActs = 12
	s := NewRSSBudgetScheduler(0, cap) // bytes-budget disabled

	var inFlight atomic.Int64
	var maxObserved atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < nActs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel, err := s.Acquire(context.Background(), Action{
				Package:  PackageID(fmt.Sprintf("p%d", i)),
				Analyzer: "A",
			})
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			n := inFlight.Add(1)
			for {
				cur := maxObserved.Load()
				if n <= cur || maxObserved.CompareAndSwap(cur, n) {
					break
				}
			}
			time.Sleep(3 * time.Millisecond)
			inFlight.Add(-1)
			rel()
		}(i)
	}
	wg.Wait()

	if got := maxObserved.Load(); got > cap {
		t.Errorf("max observed concurrency = %d, want ≤ %d", got, cap)
	}
	if got := s.Stats().PeakConcurrency; got > cap {
		t.Errorf("Stats.PeakConcurrency = %d, want ≤ %d", got, cap)
	}
}

// TestRSSBudgetScheduler_RunBatch asserts the Run entry point
// applies the same gate to a synthetic action set and propagates
// per-exec errors.
func TestRSSBudgetScheduler_RunBatch(t *testing.T) {
	s := NewRSSBudgetScheduler(1<<20, 4)
	actions := make([]Action, 20)
	for i := range actions {
		actions[i] = Action{
			Package:     PackageID(fmt.Sprintf("p%d", i)),
			Analyzer:    "A",
			RSSEstimate: 1024,
		}
	}
	var executed atomic.Int64
	sentinel := errors.New("sentinel")
	err := s.Run(context.Background(), actions, func(_ context.Context, a Action) error {
		executed.Add(1)
		if a.Package == "p3" {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("Run err = %v, want %v", err, sentinel)
	}
	if executed.Load() == 0 {
		t.Errorf("no actions executed")
	}
}

// TestRSSBudgetScheduler_ContextCancel asserts a cancelled context
// breaks a blocked Acquire promptly.
func TestRSSBudgetScheduler_ContextCancel(t *testing.T) {
	// Single-slot scheduler so the second Acquire blocks.
	s := NewRSSBudgetScheduler(0, 1)
	rel1, err := s.Acquire(context.Background(), Action{Package: "p1"})
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	defer rel1()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	rel2, err := s.Acquire(ctx, Action{Package: "p2"})
	elapsed := time.Since(start)
	if err == nil {
		rel2()
		t.Fatal("Acquire 2 returned nil err on cancelled context")
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("Acquire 2 took %v after cancel, want prompt return", elapsed)
	}
}

// TestRSSBudgetScheduler_IRPinEvents asserts the scheduler's Pin
// path increments the IR-pin counter and the Snapshot derivation
// for releases reaches the expected steady state.
func TestRSSBudgetScheduler_IRPinEvents(t *testing.T) {
	s := NewRSSBudgetScheduler(0, 4)

	p1 := s.Pin("pkg-a")
	p2 := s.PinWithAnalyzer("pkg-a", "SA4017")
	p3 := s.Pin("pkg-b")

	st := s.Stats()
	if st.IRPinEvents != 3 {
		t.Errorf("IRPinEvents = %d, want 3", st.IRPinEvents)
	}
	if st.IRReleaseEvents != 0 {
		t.Errorf("IRReleaseEvents = %d, want 0 (no releases yet)", st.IRReleaseEvents)
	}

	p1.Release()
	p2.Release()
	st = s.Stats()
	if st.IRReleaseEvents != 2 {
		t.Errorf("after 2 releases, IRReleaseEvents = %d, want 2", st.IRReleaseEvents)
	}

	p3.Release()
	st = s.Stats()
	if st.IRReleaseEvents != 3 {
		t.Errorf("after all releases, IRReleaseEvents = %d, want 3", st.IRReleaseEvents)
	}
	if got := s.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot = %v, want empty", got)
	}
}

// TestDefaultEstimator_StaticFallback pins the static lookup
// fallback shape: before observationsBeforeMedian samples, the
// estimator returns staticEstimateNonIR / staticEstimateIR.
func TestDefaultEstimator_StaticFallback(t *testing.T) {
	e := NewDefaultEstimator()
	if got := e.Estimate("p", "A", false); got != staticEstimateNonIR {
		t.Errorf("non-IR fallback = %d, want %d", got, staticEstimateNonIR)
	}
	if got := e.Estimate("p", "A", true); got != staticEstimateIR {
		t.Errorf("IR fallback = %d, want %d", got, staticEstimateIR)
	}
}

// TestDefaultEstimator_MedianOverridesAfterSamples asserts the
// sliding-window median takes over once enough observations are
// recorded.
func TestDefaultEstimator_MedianOverridesAfterSamples(t *testing.T) {
	e := NewDefaultEstimator()
	for i := 0; i < observationsBeforeMedian; i++ {
		// Spread of 10..100 MB; median is 50 MB.
		e.Observe("p", "A", true, uint64((i+1)*10*1024*1024))
	}
	got := e.Estimate("p", "A", true)
	if got == staticEstimateIR {
		t.Errorf("estimator still returning static fallback after %d observations", observationsBeforeMedian)
	}
	if got < 40*1024*1024 || got > 60*1024*1024 {
		t.Errorf("median estimate = %d MB, want ~50 MB", got/(1024*1024))
	}
}

// TestCascadeEstimate covers the simple summation projection.
func TestCascadeEstimate(t *testing.T) {
	out := EstimateCascade(100, 32*1024*1024, 40, 5*1024*1024*1024)
	if out.PackageCount != 100 {
		t.Errorf("PackageCount = %d, want 100", out.PackageCount)
	}
	if out.ProjectedPeakBytes != 100*32*1024*1024 {
		t.Errorf("ProjectedPeakBytes = %d, want %d", out.ProjectedPeakBytes, 100*32*1024*1024)
	}
	if out.ExceedsBudget {
		t.Errorf("ExceedsBudget = true, want false (3.2 GB < 5 GB budget)")
	}

	// 200 packages × 32 MB = 6.4 GB exceeds 5 GB budget.
	out2 := EstimateCascade(200, 32*1024*1024, 80, 5*1024*1024*1024)
	if !out2.ExceedsBudget {
		t.Errorf("ExceedsBudget = false, want true (6.4 GB > 5 GB)")
	}
}
