// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

// gate_contention_test.go pins the "budget gate is binding"
// contract — Stats.ActionsBlocked climbs when actions contend —
// deterministically, on both admission axes.
//
// The W9 integration test used to assert this emergently: it ran
// the real analyze pipeline under a cap of 1 and expected the
// action fan-out to overlap in time. That is not a property the
// pipeline guarantees. When the host is CPU-starved (GOMAXPROCS=1,
// or GOMAXPROCS=2 with external load) the pipeline can run its
// actions strictly one at a time, every Acquire finds the gate
// free, and ActionsBlocked is legitimately 0.
//
// Here the overlap is constructed instead of hoped for: a holder
// takes the only capacity, every waiter is observed parked at the
// gate before the holder lets go, and the blocked count is then an
// exact equality rather than a "> 0".

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// waitForGateWaiters blocks until n acquire calls are parked in the
// gate's cond, and fails the test if that does not happen promptly.
// It is the synchronisation primitive that makes the contention
// assertions below deterministic: once it returns, every waiter has
// already recorded itself as blocked.
func waitForGateWaiters(t *testing.T, g *budgetGate, n uint64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if got := g.waiters(); got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d gate waiters, got %d", n, g.waiters())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestRSSBudgetScheduler_GateBlocksUnderContention asserts that a
// contended gate throttles, on the count axis and on the bytes
// axis, with an exact ActionsBlocked count.
func TestRSSBudgetScheduler_GateBlocksUnderContention(t *testing.T) {
	const nWaiters = 4

	tests := []struct {
		name string
		// budget/maxConc configure the scheduler; holderEst and
		// waiterEst are the per-action RSS estimates. Each case
		// is arranged so the holder alone saturates the axis
		// under test.
		budget     uint64
		maxConc    int
		holderEst  uint64
		waiterEst  uint64
		wantPeakLE uint64
	}{
		{
			// Count axis: one slot, bytes gate disabled.
			name:       "count",
			budget:     0,
			maxConc:    1,
			wantPeakLE: 1,
		},
		{
			// Bytes axis: the count cap is deliberately slack, so
			// only the 256 MB budget can throttle. The holder pins
			// 192 MB and every waiter wants 128 MB, so no waiter
			// fits until the holder releases. Once it does, two
			// 128 MB waiters fit at a time and no more.
			name:       "bytes",
			budget:     256 * 1024 * 1024,
			maxConc:    32,
			holderEst:  192 * 1024 * 1024,
			waiterEst:  128 * 1024 * 1024,
			wantPeakLE: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewRSSBudgetScheduler(tc.budget, tc.maxConc)

			// The holder saturates the axis under test and does not
			// let go until every waiter is provably parked.
			holderRelease, err := s.Acquire(context.Background(), Action{
				Package:     "holder",
				Analyzer:    "A",
				RSSEstimate: tc.holderEst,
			})
			if err != nil {
				t.Fatalf("holder Acquire: %v", err)
			}

			var wg sync.WaitGroup
			for i := 0; i < nWaiters; i++ {
				wg.Add(1)
				act := Action{
					Package:     PackageID(fmt.Sprintf("p%d", i)),
					Analyzer:    "A",
					RSSEstimate: tc.waiterEst,
				}
				go func() {
					defer wg.Done()
					rel, err := s.Acquire(context.Background(), act)
					if err != nil {
						t.Errorf("waiter Acquire: %v", err)
						return
					}
					rel()
				}()
			}

			waitForGateWaiters(t, s.gate, nWaiters)
			holderRelease()
			wg.Wait()

			st := s.Stats()
			// Exactly the waiters blocked: the holder was admitted
			// into an empty gate, and each waiter waited once (a
			// waiter that re-parks after a broadcast is still a
			// single blocked Acquire).
			if st.ActionsBlocked != nWaiters {
				t.Errorf("ActionsBlocked = %d, want %d (holder saturated the gate before any waiter arrived)",
					st.ActionsBlocked, nWaiters)
			}
			if st.ActionsAcquired != nWaiters+1 {
				t.Errorf("ActionsAcquired = %d, want %d", st.ActionsAcquired, nWaiters+1)
			}
			if st.ActionsCompleted != nWaiters+1 {
				t.Errorf("ActionsCompleted = %d, want %d", st.ActionsCompleted, nWaiters+1)
			}
			if st.TotalWaitDuration == 0 {
				t.Errorf("TotalWaitDuration = 0, want > 0 (blocked Acquires must accumulate wait time)")
			}
			if st.PeakConcurrency > tc.wantPeakLE {
				t.Errorf("PeakConcurrency = %d, want ≤ %d", st.PeakConcurrency, tc.wantPeakLE)
			}
			if got := s.gate.waiters(); got != 0 {
				t.Errorf("gate waiters = %d after drain, want 0", got)
			}
		})
	}
}
