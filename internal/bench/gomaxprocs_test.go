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

// TestHarness_GOMAXPROCSOverrideAndRestore verifies the Phase 1.8
// sub-path-(b) plumbing: Config.GOMAXPROCS > 0 forces
// runtime.GOMAXPROCS to that value for the duration of Run, restores
// the prior value on exit, and surfaces the active value in
// BenchmarkResult.GOMAXPROCS.
//
// The lazy gate constructors (analysisgate, check.cpulimit, parse_cache,
// symbols) read runtime.GOMAXPROCS(0) at first use; this test asserts
// the runtime value is in place before any of them can observe it, by
// reading the result field which is populated post-application.
func TestHarness_GOMAXPROCSOverrideAndRestore(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if _, _, err := GenerateFixture(dir, SmallShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	priorGOMAXPROCS := runtime.GOMAXPROCS(0)

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		ObservationSource: scheduler.SourceVmHWM,
		SkipCascade:       true,
		SkipWarm:          true,
		GOMAXPROCS:        2,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.GOMAXPROCS != 2 {
		t.Errorf("BenchmarkResult.GOMAXPROCS = %d; want 2", res.GOMAXPROCS)
	}

	// MaxConcurrency defaulting is GOMAXPROCS-derived; with the
	// override active and no explicit MaxConcurrency, the recorded
	// scheduler cap must match the override.
	if res.SchedulerCap != 2 {
		t.Errorf("BenchmarkResult.SchedulerCap = %d; want 2 (defaulted from GOMAXPROCS)", res.SchedulerCap)
	}

	if got := runtime.GOMAXPROCS(0); got != priorGOMAXPROCS {
		t.Errorf("runtime.GOMAXPROCS(0) after Run = %d; want restored prior %d", got, priorGOMAXPROCS)
	}
}

// TestHarness_GOMAXPROCSZeroLeavesRuntimeUntouched asserts the
// default cfg.GOMAXPROCS == 0 path: no runtime mutation, no restore,
// the recorded GOMAXPROCS reflects the host's runtime value.
func TestHarness_GOMAXPROCSZeroLeavesRuntimeUntouched(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if _, _, err := GenerateFixture(dir, SmallShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	priorGOMAXPROCS := runtime.GOMAXPROCS(0)

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		ObservationSource: scheduler.SourceVmHWM,
		SkipCascade:       true,
		SkipWarm:          true,
		// GOMAXPROCS left at 0 (default).
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.GOMAXPROCS != priorGOMAXPROCS {
		t.Errorf("BenchmarkResult.GOMAXPROCS = %d; want host runtime value %d", res.GOMAXPROCS, priorGOMAXPROCS)
	}
	if got := runtime.GOMAXPROCS(0); got != priorGOMAXPROCS {
		t.Errorf("runtime.GOMAXPROCS(0) after Run = %d; want unchanged %d", got, priorGOMAXPROCS)
	}
}

// TestHarness_GOMAXPROCSNegativeRejected asserts the input validation
// for negative GOMAXPROCS — the runtime would treat negative as
// "read current value", which would silently make the override a
// no-op. The harness rejects with a clear error instead.
func TestHarness_GOMAXPROCSNegativeRejected(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Fixture:    dir,
		GOMAXPROCS: -1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Run(ctx, cfg); err == nil {
		t.Fatalf("Run with negative GOMAXPROCS: want error, got nil")
	}
}
