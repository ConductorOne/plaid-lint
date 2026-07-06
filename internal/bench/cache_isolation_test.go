// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// TestHarness_ConsecutiveRuns_SharedGOCACHE is the LEARN-FGL-004
// regression guard. Two consecutive bench.Run invocations against
// the same fixture must each pass their internal cold↔warm
// equivalence check, AND must produce the same cold diagnostic
// digest as each other.
//
// Pre-fix, sharing GOCACHE (or letting the harness inherit an
// operator-set GOCACHE) caused the second Run to see stale
// gcexportdata from the first Run's `go list` subprocess. The
// resulting diagnostic-set drift broke W6 cold↔warm equivalence
// (harness.go) and shifted the cross-Run digest. The fix isolates
// GOCACHE per invocation; this test pins that isolation.
//
// The test fixes both $TMPDIR and $GOCACHE to a shared directory
// for the duration of the test — without those, $TMPDIR randomness
// alone would mask the bug because every Run would get its own
// GOCACHE under a different parent.
func TestHarness_ConsecutiveRuns_SharedGOCACHE(t *testing.T) {
	requireGo(t)
	if testing.Short() {
		t.Skip("skipping consecutive-run regression in -short mode")
	}

	// Shared scratch dir that both runs see as $TMPDIR. The
	// per-invocation L1/L2/GOPLSCACHE/GOCACHE dirs land here; the
	// regression check is that the bench harness still isolates
	// across invocations even when the parent is shared.
	sharedTmp := t.TempDir()
	t.Setenv("TMPDIR", sharedTmp)

	// Shared GOCACHE that both runs see. The fix's job is to
	// override this with a per-invocation directory for the
	// duration of each Run. We assert below that the operator's
	// $GOCACHE is preserved across the call.
	sharedGOCACHE := filepath.Join(sharedTmp, "shared-gocache")
	if err := os.MkdirAll(sharedGOCACHE, 0o755); err != nil {
		t.Fatalf("mkdir shared GOCACHE: %v", err)
	}
	t.Setenv("GOCACHE", sharedGOCACHE)

	fixtureDir := filepath.Join(sharedTmp, "fixture")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if _, _, err := GenerateFixture(fixtureDir, SmallShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	newCfg := func() Config {
		return Config{
			Fixture:           fixtureDir,
			FixtureShape:      SmallShape.Name,
			BudgetBytes:       512 * 1024 * 1024,
			MaxConcurrency:    2,
			ObservationSource: scheduler.SourceVmHWM,
			SkipCascade:       true,
		}
	}

	// Poll GOCACHE in a background goroutine while Run executes
	// so we can capture the in-Run env value and confirm the
	// harness's per-invocation override is actually in effect.
	// A `sync/atomic` value is plenty here — Run is synchronous
	// from the caller's perspective, so the goroutine can exit
	// the moment Run returns.
	type observedGOCACHE struct {
		value atomic.Pointer[string]
	}
	pollGOCACHE := func(stop <-chan struct{}) *observedGOCACHE {
		obs := &observedGOCACHE{}
		go func() {
			tick := time.NewTicker(2 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					v := os.Getenv("GOCACHE")
					if v != sharedGOCACHE {
						copy := v
						obs.value.Store(&copy)
					}
				}
			}
		}()
		return obs
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel1()
	stop1 := make(chan struct{})
	obs1 := pollGOCACHE(stop1)
	res1, err := Run(ctx1, newCfg())
	close(stop1)
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if res1.Cold == nil || res1.Warm == nil {
		t.Fatal("Run 1: cold/warm missing")
	}
	if got := obs1.value.Load(); got == nil {
		t.Errorf("Run 1: never observed a GOCACHE override; expected a plaid-bench-gocache-* path under TMPDIR")
	} else if !strings.HasPrefix(*got, sharedTmp) || !strings.Contains(*got, "plaid-bench-gocache-") {
		t.Errorf("Run 1: observed GOCACHE=%q, want plaid-bench-gocache-* under %q", *got, sharedTmp)
	}

	// The operator's $GOCACHE must survive the Run — Run's defer
	// is supposed to restore it after the per-invocation override.
	if got := os.Getenv("GOCACHE"); got != sharedGOCACHE {
		t.Errorf("GOCACHE not restored after Run 1: got %q, want %q", got, sharedGOCACHE)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel2()
	stop2 := make(chan struct{})
	obs2 := pollGOCACHE(stop2)
	res2, err := Run(ctx2, newCfg())
	close(stop2)
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if res2.Cold == nil || res2.Warm == nil {
		t.Fatal("Run 2: cold/warm missing")
	}
	if got := obs2.value.Load(); got == nil {
		t.Errorf("Run 2: never observed a GOCACHE override; expected a plaid-bench-gocache-* path under TMPDIR")
	} else if !strings.HasPrefix(*got, sharedTmp) || !strings.Contains(*got, "plaid-bench-gocache-") {
		t.Errorf("Run 2: observed GOCACHE=%q, want plaid-bench-gocache-* under %q", *got, sharedTmp)
	}
	if v1, v2 := obs1.value.Load(), obs2.value.Load(); v1 != nil && v2 != nil && *v1 == *v2 {
		t.Errorf("Run 1 and Run 2 saw the same per-invocation GOCACHE override %q; expected different os.MkdirTemp paths", *v1)
	}

	if got := os.Getenv("GOCACHE"); got != sharedGOCACHE {
		t.Errorf("GOCACHE not restored after Run 2: got %q, want %q", got, sharedGOCACHE)
	}

	// Cross-run cold digest equality is the load-bearing assertion.
	// LEARN-FGL-004 documented three distinct cold digests across
	// four runs of an identical workload; this test would have
	// caught that.
	if res1.Cold.DiagnosticDigest != res2.Cold.DiagnosticDigest {
		t.Errorf("cross-run cold digest divergence:\n  run1.cold=%s\n  run2.cold=%s",
			res1.Cold.DiagnosticDigest, res2.Cold.DiagnosticDigest)
	}
}
