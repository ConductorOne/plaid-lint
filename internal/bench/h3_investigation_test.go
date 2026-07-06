// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build h3

package bench

// h3_investigation_test.go is the H-3 (Phase 1.5) divergence
// disambiguator. It drives bench.Run with each of the three RSS
// observation sources (vmhwm / heapalloc / runtimemetrics) three
// times in sequence against the same synthetic medium fixture and
// dumps the (source, iteration) -> {wall, action_count, digest}
// matrix to test stdout. The test does NOT assert any equivalence
// because the question being asked is "are the sources internally
// consistent run-to-run, and are they equivalent cross-source?".
// It runs under -tags=h3 only so CI doesn't pay the cost.

import (
	"context"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// TestH3_ObservationSourceDivergence is the H-3 investigation
// driver. Run with `go test -tags=h3 -run TestH3_Observation` to
// emit the divergence matrix. The test is build-tagged so the
// normal `go test ./...` path does not pay the cost (the matrix
// takes ~10 s on bench_medium even with skip-cascade).
//
// The test is intentionally read-only: it prints the matrix to test
// stdout (via t.Logf) but does not assert. The expected output
// shape:
//
//	source         iter  cold_ms  cold_ac  cold_digest  warm_ms  warm_ac  warm_digest
//	vmhwm          0     ...      ...      <12hex>      ...      ...      <12hex>
//	vmhwm          1     ...      ...      <same>       ...      ...      <same>
//	vmhwm          2     ...      ...      <same>       ...      ...      <same>
//	heapalloc      0     ...      ...      <same>       ...      ...      <same>
//	...
//	runtimemetrics 0     ...      ...      <?>          ...      ...      <?>
//
// Hypothesis disambiguation: if runtimemetrics digest differs from
// vmhwm/heapalloc consistently across all three iterations →
// measurement side effect (hypothesis b). If it varies between
// iterations → cache leakage (hypothesis a).
//
// This is an exploratory test, not a regression guard. The action
// happens in the t.Logf output, not the assert.
func TestH3_ObservationSourceDivergence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping H-3 divergence sweep in -short mode")
	}
	requireGo(t)

	dir := t.TempDir()
	if _, _, err := GenerateFixture(dir, MediumShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	sources := []scheduler.ObservationSource{
		scheduler.SourceVmHWM,
		scheduler.SourceHeapAlloc,
		scheduler.SourceRuntimeMetrics,
	}
	const iterations = 3

	type row struct {
		source     scheduler.ObservationSource
		iter       int
		coldWall   int64
		coldAC     uint64
		coldDigest string
		coldDC     int
		warmWall   int64
		warmAC     uint64
		warmDigest string
		warmDC     int
	}
	var rows []row

	for _, src := range sources {
		for i := 0; i < iterations; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			cfg := Config{
				Fixture:           dir,
				FixtureShape:      MediumShape.Name,
				BudgetBytes:       512 * 1024 * 1024,
				MaxConcurrency:    2,
				ObservationSource: src,
				SkipCascade:       true,
			}
			res, err := Run(ctx, cfg)
			cancel()
			if err != nil {
				t.Errorf("Run %s iter %d: %v", src, i, err)
				continue
			}
			r := row{source: src, iter: i}
			r.coldWall = res.Cold.WallMs
			r.coldAC = res.Cold.ActionCount
			r.coldDigest = shortDigest(res.Cold.DiagnosticDigest)
			r.coldDC = res.Cold.DiagnosticCount
			if res.Warm != nil {
				r.warmWall = res.Warm.WallMs
				r.warmAC = res.Warm.ActionCount
				r.warmDigest = shortDigest(res.Warm.DiagnosticDigest)
				r.warmDC = res.Warm.DiagnosticCount
			}
			rows = append(rows, r)
		}
	}

	t.Logf("H-3 divergence matrix (medium fixture, 3 iters/source):")
	t.Logf("source            iter cold_ms cold_ac cold_digest cold_dc warm_ms warm_ac warm_digest warm_dc")
	for _, r := range rows {
		t.Logf("%-16s  %4d  %6d %7d %-11s %7d %6d %7d %-11s %7d",
			r.source, r.iter,
			r.coldWall, r.coldAC, r.coldDigest, r.coldDC,
			r.warmWall, r.warmAC, r.warmDigest, r.warmDC,
		)
	}

	// Hypothesis check: report whether runtimemetrics produces the
	// same digest as vmhwm. This is the load-bearing question the
	// c1 report Finding 1 raised.
	vmDigests := map[scheduler.ObservationSource]map[string]int{}
	for _, r := range rows {
		if _, ok := vmDigests[r.source]; !ok {
			vmDigests[r.source] = map[string]int{}
		}
		vmDigests[r.source][r.coldDigest]++
	}
	t.Logf("Unique cold digests per source:")
	for src, ds := range vmDigests {
		t.Logf("  %s: %v", src, ds)
	}

	acByIter := map[scheduler.ObservationSource]map[int]uint64{}
	for _, r := range rows {
		if _, ok := acByIter[r.source]; !ok {
			acByIter[r.source] = map[int]uint64{}
		}
		acByIter[r.source][r.iter] = r.coldAC
	}
	t.Logf("Cold action_count per (source, iter):")
	for src, m := range acByIter {
		t.Logf("  %s: %v", src, m)
	}
}

func shortDigest(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
