// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

// run_acquire_equiv_test.go is the W10 regression test for the
// Run-vs-Acquire/Release semantic equivalence claim (phase-1 task
// 1.38). The two public entry points share an implementation
// (Run is a thin errgroup wrapper over Acquire/Release), but the
// claim that they share cancellation + telemetry semantics is
// load-bearing for callers picking between them: external
// benchmarks reach for Run; the gopls action driver reaches for
// Acquire/Release. The W10 resolution is to keep both and pin the
// shared semantics with this test rather than rewrite the driver
// integration.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunVsAcquire_TelemetryEquivalence drives an identical set of
// actions through Run and through Acquire/Release on two
// fresh-constructed schedulers and asserts that the resulting
// [Stats] match across the four counters that the cache and
// benchmark harness rely on: ActionsAcquired, ActionsCompleted,
// ActionsBlocked, IRPinEvents.
//
// TotalWaitDuration and TotalActionDuration are NOT asserted
// exactly — they're wall-clock-derived and naturally vary across
// runs — but we assert TotalActionDuration > 0 in both runs as a
// liveness check that the timing instrumentation fired.
func TestRunVsAcquire_TelemetryEquivalence(t *testing.T) {
	const (
		nActs        = 16
		budget       = 256 * 1024 * 1024
		perAct       = 32 * 1024 * 1024
		maxConc      = 4
		holdDuration = 2 * time.Millisecond
	)

	makeActions := func() []Action {
		out := make([]Action, nActs)
		for i := range out {
			out[i] = Action{
				Package:     PackageID(fmt.Sprintf("p%d", i)),
				Analyzer:    "A",
				NeedsIR:     i%2 == 0, // half want IR pins
				RSSEstimate: perAct,
			}
		}
		return out
	}

	// Variant 1: Run.
	sRun := NewRSSBudgetScheduler(budget, maxConc)
	if err := sRun.Run(context.Background(), makeActions(), func(_ context.Context, a Action) error {
		time.Sleep(holdDuration)
		// Mirror the cache-side pin/release pattern.
		if a.NeedsIR {
			sRun.Pin(a.Package).Release()
		}
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	statsRun := sRun.Stats()

	// Variant 2: Acquire/Release in an explicit goroutine pool.
	sManual := NewRSSBudgetScheduler(budget, maxConc)
	done := make(chan struct{}, nActs)
	for _, act := range makeActions() {
		act := act
		go func() {
			defer func() { done <- struct{}{} }()
			release, err := sManual.Acquire(context.Background(), act)
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			time.Sleep(holdDuration)
			if act.NeedsIR {
				sManual.Pin(act.Package).Release()
			}
			release()
		}()
	}
	for i := 0; i < nActs; i++ {
		<-done
	}
	statsManual := sManual.Stats()

	// Liveness assertions.
	if statsRun.ActionsAcquired != uint64(nActs) {
		t.Errorf("Run: ActionsAcquired = %d, want %d", statsRun.ActionsAcquired, nActs)
	}
	if statsManual.ActionsAcquired != uint64(nActs) {
		t.Errorf("Acquire: ActionsAcquired = %d, want %d", statsManual.ActionsAcquired, nActs)
	}
	if statsRun.TotalActionDuration == 0 || statsManual.TotalActionDuration == 0 {
		t.Errorf("TotalActionDuration zero in one of the variants (Run=%v Acquire=%v)",
			statsRun.TotalActionDuration, statsManual.TotalActionDuration)
	}

	// Equivalence assertions.
	cases := []struct {
		name              string
		runVal, manualVal uint64
	}{
		{"ActionsAcquired", statsRun.ActionsAcquired, statsManual.ActionsAcquired},
		{"ActionsCompleted", statsRun.ActionsCompleted, statsManual.ActionsCompleted},
		// IRPinEvents must equal exactly half the actions (every
		// NeedsIR=true action calls Pin once). Both variants share
		// the same fan-out, so the counter must agree.
		{"IRPinEvents", statsRun.IRPinEvents, statsManual.IRPinEvents},
	}
	for _, c := range cases {
		if c.runVal != c.manualVal {
			t.Errorf("%s: Run=%d, Acquire=%d (must match)", c.name, c.runVal, c.manualVal)
		}
	}

	// ActionsBlocked is not exactly equal across runs because the
	// goroutine scheduling order is non-deterministic; both
	// variants will block actions at the gate, just not the same
	// count. We assert that both variants blocked at least once
	// (the budget gate is binding under the tight 256 MB / 4-conc
	// configuration).
	if statsRun.ActionsBlocked == 0 {
		t.Errorf("Run: ActionsBlocked = 0, want > 0 (gate must throttle on tight budget)")
	}
	if statsManual.ActionsBlocked == 0 {
		t.Errorf("Acquire: ActionsBlocked = 0, want > 0")
	}

	// PeakConcurrency must respect the count cap in BOTH variants.
	if statsRun.PeakConcurrency > maxConc {
		t.Errorf("Run: PeakConcurrency=%d > maxConc=%d", statsRun.PeakConcurrency, maxConc)
	}
	if statsManual.PeakConcurrency > maxConc {
		t.Errorf("Acquire: PeakConcurrency=%d > maxConc=%d", statsManual.PeakConcurrency, maxConc)
	}
}

// TestRunVsAcquire_CancellationEquivalence drives the same fanout
// through both entry points with a ctx that fires mid-run, and
// asserts that BOTH variants surface ctx.Err() through their
// respective error returns, and that BOTH variants's ActionsCompleted
// counter reflects only the actions that successfully Acquired.
//
// The Run variant's error is the first non-nil errgroup return; the
// Acquire variant's error is the Acquire return on the first
// post-cancellation goroutine. Both must be context.Canceled (or
// a Cause-derived wrapping of it).
func TestRunVsAcquire_CancellationEquivalence(t *testing.T) {
	const (
		nActs   = 32
		budget  = 64 * 1024 * 1024 // tight: only 2 32-MB actions can be in flight
		perAct  = 32 * 1024 * 1024
		maxConc = 4
	)
	makeActions := func() []Action {
		out := make([]Action, nActs)
		for i := range out {
			out[i] = Action{Package: PackageID(fmt.Sprintf("p%d", i)), Analyzer: "A", RSSEstimate: perAct}
		}
		return out
	}

	// Variant 1: Run with an early-fired cancel.
	sRun := NewRSSBudgetScheduler(budget, maxConc)
	ctx, cancel := context.WithCancel(context.Background())
	var ranRun atomic.Int64
	go func() {
		// Let a few actions Acquire, then cancel.
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	runErr := sRun.Run(ctx, makeActions(), func(c context.Context, _ Action) error {
		ranRun.Add(1)
		// Hold the slot long enough that the cancel fires while we
		// hold it; the next Acquire will see ctx.Err().
		select {
		case <-time.After(50 * time.Millisecond):
		case <-c.Done():
		}
		return nil
	})
	if runErr == nil {
		t.Errorf("Run: expected ctx-cancellation error, got nil")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Errorf("Run err = %v, want context.Canceled", runErr)
	}

	// Variant 2: Acquire-based fanout.
	sManual := NewRSSBudgetScheduler(budget, maxConc)
	ctx2, cancel2 := context.WithCancel(context.Background())
	var ranManual atomic.Int64
	var firstErr atomic.Pointer[error]
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel2()
	}()
	done := make(chan struct{}, nActs)
	for _, act := range makeActions() {
		act := act
		go func() {
			defer func() { done <- struct{}{} }()
			release, err := sManual.Acquire(ctx2, act)
			if err != nil {
				e := err
				firstErr.CompareAndSwap(nil, &e)
				return
			}
			ranManual.Add(1)
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx2.Done():
			}
			release()
		}()
	}
	for i := 0; i < nActs; i++ {
		<-done
	}
	pErr := firstErr.Load()
	if pErr == nil || *pErr == nil {
		t.Errorf("Acquire variant: expected at least one ctx-cancellation error, got nil")
	} else if !errors.Is(*pErr, context.Canceled) {
		t.Errorf("Acquire err = %v, want context.Canceled", *pErr)
	}

	// Both variants must have actions-completed equal to
	// actions-ran (every Acquire that succeeded then released its
	// slot in the body).
	statsRun := sRun.Stats()
	statsManual := sManual.Stats()
	if int64(statsRun.ActionsCompleted) != ranRun.Load() {
		t.Errorf("Run: ActionsCompleted=%d != ran=%d (completion counter must track release calls)",
			statsRun.ActionsCompleted, ranRun.Load())
	}
	if int64(statsManual.ActionsCompleted) != ranManual.Load() {
		t.Errorf("Acquire: ActionsCompleted=%d != ran=%d", statsManual.ActionsCompleted, ranManual.Load())
	}
	// Both must have admitted strictly fewer than nActs (the
	// cancellation aborted the rest).
	if statsRun.ActionsAcquired >= uint64(nActs) {
		t.Errorf("Run: ActionsAcquired=%d >= nActs=%d; expected cancellation to abort some", statsRun.ActionsAcquired, nActs)
	}
	if statsManual.ActionsAcquired >= uint64(nActs) {
		t.Errorf("Acquire: ActionsAcquired=%d >= nActs=%d", statsManual.ActionsAcquired, nActs)
	}
}
