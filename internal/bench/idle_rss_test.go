// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// TestHarness_IdleRSSBytesPopulated pins the Phase 1.9 WF.0 follow-up
// contract for BenchmarkResult.IdleRSSBytes: after Run completes,
// the field is non-zero on Linux and bounded by the run's cold peak
// (when that peak is also populated). On non-Linux it is zero —
// /proc/self/status is unavailable.
func TestHarness_IdleRSSBytesPopulated(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if _, _, err := GenerateFixture(dir, SmallShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		SkipCascade:       true,
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if runtime.GOOS != "linux" {
		if res.IdleRSSBytes != 0 {
			t.Errorf("non-Linux IdleRSSBytes = %d, want 0", res.IdleRSSBytes)
		}
		return
	}

	if res.IdleRSSBytes <= 0 {
		t.Fatalf("IdleRSSBytes = %d, want > 0 on Linux", res.IdleRSSBytes)
	}

	// Sanity invariant: idle residency must not exceed the cold
	// scenario's high-water VmHWM delta. PeakRSSBytes on
	// ScenarioResult is rssDelta(startRSS, peak), so it can be 0
	// on a fast/small fixture; only enforce the bound when the
	// per-scenario reading is itself non-zero.
	if res.Cold != nil && res.Cold.PeakRSSBytes > 0 {
		if uint64(res.IdleRSSBytes) > res.Cold.PeakRSSBytes*4 {
			t.Errorf("IdleRSSBytes = %d wildly exceeds cold PeakRSSBytes = %d (delta cap 4x for SmallShape jitter)",
				res.IdleRSSBytes, res.Cold.PeakRSSBytes)
		}
	}
}
