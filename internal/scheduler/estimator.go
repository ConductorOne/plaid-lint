// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

import (
	"runtime"
	"sort"
	"sync"
)

// defaultGOMAXPROCS returns runtime.GOMAXPROCS(0). Wrapped so
// tests can override via the package-private variable below.
func defaultGOMAXPROCS() int { return goMaxProcs() }

// goMaxProcs is the indirection point so the W9 tests can pin
// the cap deterministically. Production callers leave it pointing
// at runtime.GOMAXPROCS.
var goMaxProcs = func() int { return runtime.GOMAXPROCS(0) }

// RSSEstimator projects a per-action RSS estimate from a
// (package, analyzer) pair. The estimator is the source of truth
// for the bytes-axis input to [RSSBudgetScheduler]'s budget gate.
//
// The W9 implementation in [DefaultEstimator] uses a static
// lookup table indexed by the analyzer's NeedsIR bit, augmented
// with a sliding-window median once ≥10 samples are recorded via
// Observe. The W10 benchmark phase replaces the table with
// empirically-measured values from the c1 perf suite; the
// interface lets the swap happen without touching the scheduler.
type RSSEstimator interface {
	// Estimate returns the projected resident-set in bytes for
	// the action. The scheduler treats the value as opaque
	// input; zero is legal and means "budget-neutral".
	Estimate(pkg PackageID, analyzer string, needsIR bool) uint64

	// Observe records the actual peak RSS the action consumed,
	// once the action has completed. Estimators with a learning
	// loop fold the observation into subsequent estimates.
	// Estimators without a learning loop ignore the call.
	Observe(pkg PackageID, analyzer string, needsIR bool, observedBytes uint64)
}

// DefaultEstimator is the W9 production estimator: a static
// lookup table fallback layered with a sliding-window median
// over per-(NeedsIR) buckets. The W10 benchmark phase will refine
// the table; the W9 numbers are an order-of-magnitude estimate
// drawn from the bottleneck synthesis (typical SA-* analyzer on
// a c1-sized package consumes 30-60 MB peak RSS when it
// constructs IR, and 5-10 MB when it does not).
//
// The estimator is concurrency-safe.
type DefaultEstimator struct {
	mu sync.Mutex

	// window holds the most recent N observations, segmented by
	// NeedsIR. The W9 minimum sample count before the median
	// overrides the static fallback is observationsBeforeMedian.
	windowIR    []uint64
	windowNonIR []uint64
}

// Tunables. The static fallbacks were calibrated empirically in W10
// against the in-repo synthetic fixtures (bench_small, bench_medium,
// bench_cascade) using the VmHWM observation source. The corrected
// calibration re-derives the numbers under the full W7+W8
// 102-analyzer workload.
// Pre-W10 values were 8 MB / 48 MB, a pure hand-waving guess; the
// W10 numbers are still rough (sample size is thousands of actions
// on synthetic fixtures, not c1-scale), but they are grounded in
// measurement rather than back-of-envelope.
//
// observationsBeforeMedian remains 10: the sliding-window median
// takes over once each bucket has accumulated ten samples.
// maxWindowSamples remains 128: the largest c1-scale closure is
// ~3000 packages × ~30 NeedsIR analyzers = ~90 k actions; a
// 128-sample window covers ~0.14% of the run, which is the
// "responsive to recent shape change but not jittery" sweet spot.
const (
	// StaticEstimateNonIRBytes is the W10 static fallback for
	// actions with NeedsIR=false (the simple AST-walking analyzers
	// like assign, nilfunc, printf). 12 MB is the W10 first-pass
	// value, retained after the corrected 102-analyzer calibration
	// because the corrected per-action VmHWM peaks are all below
	// 12 MB on the synthetic fixtures (max ~12.0 MB on bench_medium,
	// p95 ~0.6 MB). We pick the higher of (first-pass
	// constant, corrected-calibration peak) — synthetic-fixture
	// VmHWM noise is too low to override 12 MB, and a conservative-
	// pessimistic estimate is correct for a budget gate that
	// over-reserves rather than under-reserves.
	StaticEstimateNonIRBytes uint64 = 12 * 1024 * 1024

	// StaticEstimateIRBytes is the W10 static fallback for actions
	// with NeedsIR=true (the buildir-consuming analyzers, mostly
	// SA-*). 64 MB is the W10 first-pass value, retained after the
	// corrected 102-analyzer calibration: corrected per-action VmHWM
	// peaks for IR-bearing actions are bounded by ~5.5 MB on
	// bench_medium / ~3.4 MB on bench_small — well below 64 MB.
	// We keep the higher number; synthetic VmHWM under-
	// reports c1-scale, and the 30-60 MB IR-action upper estimate
	// remains the better bound for the gate's first ~10 actions.
	StaticEstimateIRBytes uint64 = 64 * 1024 * 1024

	// Internal aliases for backwards compatibility with the W9
	// estimator math. The W9 numbers (8 MB / 48 MB) were a guess.
	staticEstimateNonIR uint64 = StaticEstimateNonIRBytes
	staticEstimateIR    uint64 = StaticEstimateIRBytes

	maxWindowSamples         = 128
	observationsBeforeMedian = 10
)

// NewDefaultEstimator returns a fresh estimator with an empty
// observation window. Production callers attach this to the
// scheduler at construction; the W10 benchmark harness may
// re-attach a custom RSSEstimator on the fly.
func NewDefaultEstimator() *DefaultEstimator {
	return &DefaultEstimator{
		windowIR:    make([]uint64, 0, maxWindowSamples),
		windowNonIR: make([]uint64, 0, maxWindowSamples),
	}
}

// Estimate implements [RSSEstimator.Estimate]. The static fallback
// is used until the per-bucket window has at least
// observationsBeforeMedian samples; thereafter the median of the
// window is returned. The pkg and analyzer parameters are
// currently unused (W10 may key on them).
func (e *DefaultEstimator) Estimate(_ PackageID, _ string, needsIR bool) uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	window := e.windowNonIR
	fallback := staticEstimateNonIR
	if needsIR {
		window = e.windowIR
		fallback = staticEstimateIR
	}
	if len(window) < observationsBeforeMedian {
		return fallback
	}
	return medianBytes(window)
}

// Observe implements [RSSEstimator.Observe]. Recent observations
// shift older ones out of the window once maxWindowSamples is
// reached.
func (e *DefaultEstimator) Observe(_ PackageID, _ string, needsIR bool, observedBytes uint64) {
	if observedBytes == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if needsIR {
		e.windowIR = appendWindow(e.windowIR, observedBytes)
	} else {
		e.windowNonIR = appendWindow(e.windowNonIR, observedBytes)
	}
}

func appendWindow(window []uint64, v uint64) []uint64 {
	if len(window) >= maxWindowSamples {
		// Drop the oldest sample.
		copy(window, window[1:])
		window = window[:len(window)-1]
	}
	return append(window, v)
}

// medianBytes returns the median of a sorted-on-the-fly copy of
// in. The input slice is not modified.
func medianBytes(in []uint64) uint64 {
	if len(in) == 0 {
		return 0
	}
	cp := make([]uint64, len(in))
	copy(cp, in)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	// Even length: average the two midpoints (rounding down).
	return (cp[mid-1] + cp[mid]) / 2
}

// CascadeEstimate is the metadata-graph-driven projection: given
// a set of root packages and their full
// transitive closure, return the total bytes the scheduler should
// expect to need to admit all actions on those packages.
//
// W9 implementation is a pure summation across the supplied
// per-package counts; the scheduler does NOT cap closure size
// (closure is the input, the
// concurrency cap is the output). The function exists so
// callers (the W10 benchmark, the progress reporter) can compare
// the projected cascade footprint against the configured budget
// and report a warning if the closure would exceed it.
//
// perPackageBytes is the per-package projected RSS. NeedsIRRatio
// is the fraction of analyzers per package that consume IR
// (typically the BundledRegistry NeedsIR split, ~50/95 ≈ 0.53 in
// W8's set). The W9 caller computes both by walking
// MetadataGraph + BundledRegistry.
type CascadeEstimate struct {
	PackageCount         int
	PerPackageBytes      uint64
	NeedsIRPackageCount  int
	ProjectedPeakBytes   uint64
	ExceedsBudget        bool
}

// EstimateCascade computes a [CascadeEstimate] for the given input.
// budgetBytes is the scheduler's configured ceiling; pass 0 to
// skip the ExceedsBudget computation. The projection is intentionally
// simple: packageCount × perPackageBytes, with no overlapping-fanin
// discount. The W10 phase calibrates the constants; W9 ships the
// shape.
func EstimateCascade(packageCount int, perPackageBytes uint64, needsIRPackageCount int, budgetBytes uint64) CascadeEstimate {
	out := CascadeEstimate{
		PackageCount:        packageCount,
		PerPackageBytes:     perPackageBytes,
		NeedsIRPackageCount: needsIRPackageCount,
		ProjectedPeakBytes:  uint64(packageCount) * perPackageBytes,
	}
	if budgetBytes > 0 && out.ProjectedPeakBytes > budgetBytes {
		out.ExceedsBudget = true
	}
	return out
}
