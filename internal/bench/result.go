// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import "time"

// BenchmarkResult is the structured output the harness emits. The
// schema is intentionally flat — three named scenarios (cold, warm,
// cascade), each a [ScenarioResult] — so the calibration scripts can
// parse the JSON without recursing.
//
// The harness writes BenchmarkResult to stdout (or the file named by
// --out) as JSON. The schema is versioned by Schema so future
// breaking changes can be detected by the calibration scripts.
type BenchmarkResult struct {
	// Schema is the result-document version. W10 ships v1; bumps
	// should document the shape change.
	Schema int `json:"schema"`

	// Fixture is the absolute path of the fixture the harness ran
	// against. Useful for traceability across machines.
	Fixture string `json:"fixture"`

	// FixtureShape is the descriptor identifying which synthetic
	// shape this fixture came from. Empty for external fixtures
	// (the gate-decision c1 run).
	FixtureShape string `json:"fixture_shape,omitempty"`

	// ObservationSource is the [scheduler.Sampler.Name] of the
	// sampler the harness installed. The value mirrors the
	// PLAID_RSS_OBSERVATION env var (or its default).
	ObservationSource string `json:"observation_source"`

	// Platform / GoVersion / SchedulerCap pin the run's
	// environment. The first two come from runtime; SchedulerCap
	// is the scheduler.RSSBudgetScheduler's constructor argument.
	Platform     string `json:"platform"`
	GoVersion    string `json:"go_version"`
	SchedulerCap int    `json:"scheduler_max_concurrency"`
	BudgetBytes  uint64 `json:"scheduler_budget_bytes"`

	// GOMAXPROCS pins the Go runtime's GOMAXPROCS value the harness
	// ran under, read via runtime.GOMAXPROCS(0) after any --gomaxprocs
	// override was applied. Phase 1.8 sub-path-(b) sweeps use this to
	// tell apart JSON outputs from runs of the same fixture at
	// different worker counts.
	GOMAXPROCS int `json:"gomaxprocs"`

	// AnalyzerCount is the count of analyzers the harness installed
	// (typically len(analyzers.AllBundledAnalyzers())). Recorded
	// for traceability — the W8 set is 95 SA-* + the 5 W6 + 3 W7.
	AnalyzerCount int `json:"analyzer_count"`

	// Cold / Warm / LeafEdit / Cascade are the per-scenario results.
	// Cold is always populated; the others are skipped when the
	// harness flags disable them. LeafEdit is the H-1 (Phase 1.5)
	// scenario: a single-file leaf-package edit measured between
	// warm and cascade so the L1/L2 caches from cold+warm are in
	// place but the leaf-package action graph re-runs. CascadeRuns
	// > 1 (H-2) leaves Cascade populated with the first run's
	// numbers (for backwards-compat) and adds CascadeAggregate.
	Cold     *ScenarioResult `json:"cold,omitempty"`
	Warm     *ScenarioResult `json:"warm,omitempty"`
	LeafEdit *ScenarioResult `json:"leaf_edit,omitempty"`
	Cascade  *ScenarioResult `json:"cascade,omitempty"`

	// CascadeAggregate is the H-2 (Phase 1.5) multi-run cascade
	// payload. Populated whenever the cascade scenario ran (i.e.
	// SkipCascade=false), regardless of CascadeRuns: a single-run
	// invocation produces a CascadeAggregate with one Runs entry
	// whose stats equal Cascade above. Multi-run invocations
	// populate Runs with per-run details and AggregateStats with
	// mean/p95/max/min across the N runs. Backwards compatibility:
	// existing consumers can keep reading Cascade and ignore this
	// field; new consumers MUST prefer CascadeAggregate.Runs when
	// CascadeAggregate.RunCount > 1 because Cascade reflects only
	// the first run.
	CascadeAggregate *CascadeAggregateResult `json:"cascade_aggregate,omitempty"`

	// StaticFallbacks pins the [scheduler.DefaultEstimator] static
	// table values in effect during the run. Recorded so
	// calibration scripts can see what numbers were in play
	// without re-deriving them from the run's source tree.
	StaticFallbacks StaticFallbackSnapshot `json:"static_fallbacks"`

	// ActionPeakObservations is the per-(NeedsIR) sorted list of
	// the largest observation deltas the harness saw across the
	// entire run. Used by the calibration script to recommend new
	// static fallback values. Capped at maxActionPeakObservations
	// per bucket so the JSON output stays bounded.
	ActionPeakObservations PeakObservations `json:"action_peak_observations"`

	// GOMEMLIMITBytes is the Phase 1.6 Lever D output field: the
	// active runtime soft memory limit at the time of the run, read
	// back via debug.SetMemoryLimit(-1) after any --gomemlimit /
	// GOMEMLIMIT env-var resolution. Emitted unconditionally so the
	// JSON output is self-describing — sweep tooling cross-validates
	// this against the requested value. A value of math.MaxInt64
	// (9223372036854775807) means "no limit set" (the Go runtime
	// default when neither --gomemlimit nor GOMEMLIMIT is in
	// effect).
	GOMEMLIMITBytes int64 `json:"gomemlimit_bytes"`

	// MaxInFlightPackages echoes the cluster cap the harness was
	// invoked with (0 = unlimited / baseline). Emitted at the top of
	// BenchmarkResult so Phase 1.8 sub-path-(c'') sweeps can be told
	// apart by JSON alone — the per-scenario GatePeakInFlight is the
	// achieved peak, this field is the requested cap.
	MaxInFlightPackages int `json:"max_in_flight_packages"`

	// StreamingIR is the Phase 1.8 sub-path-(c'') mode flag. True
	// when the bench was invoked with --streaming-ir (which is an
	// opinionated alias for --max-in-flight-packages=1). Emitted so
	// the (c'') verdict memo can discriminate "explicit streaming
	// mode" from "happened to be at N=1 via the numeric flag".
	StreamingIR bool `json:"streaming_ir"`

	// ExcludePatterns is the deduplicated list of user-supplied
	// exclude patterns (from --exclude-glob and --exclude-from)
	// the harness applied. Empty when no exclusion was requested.
	// Recorded for reproducibility — a sweep comparing
	// "full closure" vs "minus generated proto" can be told apart
	// from this field alone.
	ExcludePatterns []string `json:"exclude_patterns,omitempty"`

	// WorkspacePackageCount is the count of workspace packages
	// the cold scenario observed BEFORE exclusion. Equal to the
	// count produced by Snapshot.WorkspacePackages(); recorded so
	// the excluded ratio is interpretable.
	WorkspacePackageCount int `json:"workspace_package_count"`

	// ExcludedPackageCount is the number of workspace packages
	// the harness dropped from the analysis set in the cold
	// scenario. Zero when no excluder is configured (or when no
	// package matched). The same packages are dropped from
	// warm/cascade by construction, so the per-scenario
	// ActionCount drop is the multiplicative effect across the
	// full run.
	ExcludedPackageCount int `json:"excluded_package_count"`

	// ColdWarmDigestMismatch is true when the harness observed a
	// cold↔warm diagnostic digest divergence AND the
	// AllowColdWarmDigestMismatch config flag was set (so the
	// divergence was reported as a flag rather than as an error).
	// Default false. When this flag is true the headline numbers
	// (peak, wall, gate counters) are still trustworthy; only the
	// diagnostic-determinism assertion is in question.
	ColdWarmDigestMismatch bool `json:"cold_warm_digest_mismatch,omitempty"`

	// IdleRSSBytes is the process's current VmRSS in bytes, sampled
	// at end-of-Run after two runtime.GC() passes and a brief
	// madvise-settle sleep. It complements the per-scenario
	// [ScenarioResult.PeakRSSBytes] (VmHWM high-water during a
	// scenario) by reporting idle residency once work has completed.
	//
	// The two are NOT redundant: VmHWM is monotonic over the
	// process lifetime, so PeakRSSBytes captures peak working set
	// (typically dominated by in-fanout per-package state). VmRSS
	// can fall after madvise(MADV_DONTNEED) returns Go-runtime
	// pages to the OS, so IdleRSSBytes captures genuine end-of-Run
	// residency. The delta (PeakRSSBytes − IdleRSSBytes) is mostly
	// HeapIdle − HeapReleased waiting on madvise reclaim.
	//
	// On c1 at end-of-Run: PeakRSSBytes ≈ 23–32 GiB, IdleRSSBytes
	// ≈ 0.5–1 GiB. The gap is what prompted this field.
	//
	// Zero on non-Linux or when /proc/self/status is unreadable.
	IdleRSSBytes int64 `json:"idle_rss_bytes"`
}

// ScenarioResult captures the metrics for one cold/warm/cascade run.
// Every counter is "delta from previous scenario" rather than
// absolute, so the calibration script can compose them without
// double-counting. The exception is WallMs, which is per-scenario
// wall time (the natural unit) and PeakRSSBytes, which is the
// scenario's max VmHWM reading.
type ScenarioResult struct {
	// WallMs is the scenario's wall time in milliseconds.
	WallMs int64 `json:"wall_ms"`

	// PeakRSSBytes is the maximum VmHWM observed during the
	// scenario (zero on non-Linux). The harness samples VmHWM
	// before and after the scenario; the recorded value is the
	// max minus the pre-scenario reading, so concurrent test
	// processes don't inflate the number.
	PeakRSSBytes uint64 `json:"peak_rss_bytes"`

	// DiagnosticCount is the number of diagnostics the Analyze
	// call returned. Must match across cold/warm runs for the
	// run to be valid (the harness asserts this and surfaces a
	// non-zero exit code when it fails).
	DiagnosticCount int `json:"diagnostic_count"`
	// DiagnosticDigest is a SHA-256 over the canonical-form
	// diagnostic stream. Used for the cold↔warm equivalence
	// assertion. The hex digest is reported so logs can compare
	// across runs.
	DiagnosticDigest string `json:"diagnostic_digest"`

	// ActionCount is the count of (analyzer, package) actions
	// the scheduler admitted during the scenario.
	ActionCount uint64 `json:"action_count"`

	// L1Hits / L1Stores / L1Misses are the cache.L1Metrics deltas.
	L1Hits         int64 `json:"l1_hits"`
	L1Stores       int64 `json:"l1_stores"`
	L1Misses       int64 `json:"l1_misses"`
	L1EncodeErrors int64 `json:"l1_encode_errors"`

	// L2Hits / L2Stores / L2Misses are the cache.L2Metrics deltas.
	L2Hits   int64 `json:"l2_hits"`
	L2Stores int64 `json:"l2_stores"`
	L2Misses int64 `json:"l2_misses"`

	// IRPinEvents / IRReleaseEvents are scheduler.Stats deltas.
	// The two must be equal at end-of-scenario (the no-pin-leak
	// contract). The harness asserts this.
	IRPinEvents     uint64 `json:"ir_pin_events"`
	IRReleaseEvents uint64 `json:"ir_release_events"`

	// SchedulerBlocked is the scheduler.Stats.ActionsBlocked delta.
	// Non-zero means the bytes-or-count axis throttled the run.
	SchedulerBlocked uint64 `json:"scheduler_blocked"`

	// SchedulerPeakConcurrency is the high-water mark of in-flight
	// actions during the scenario, sampled at scenario end.
	SchedulerPeakConcurrency uint64 `json:"scheduler_peak_concurrency"`

	// TotalWaitMs / TotalActionMs are the aggregate wait/action
	// times the scheduler recorded across the scenario.
	TotalWaitMs   int64 `json:"total_wait_ms"`
	TotalActionMs int64 `json:"total_action_ms"`

	// MeanObservedRSSNonIR / MeanObservedRSSIR are the mean
	// non-zero observation deltas the harness saw for actions in
	// each NeedsIR bucket. Zero when no observations fired
	// (e.g. NoopSampler). Used by the calibration script as one
	// of three inputs to the recommended static fallback.
	MeanObservedRSSNonIR uint64 `json:"mean_observed_rss_nonir"`
	MeanObservedRSSIR    uint64 `json:"mean_observed_rss_ir"`
	P95ObservedRSSNonIR  uint64 `json:"p95_observed_rss_nonir"`
	P95ObservedRSSIR     uint64 `json:"p95_observed_rss_ir"`

	// ObservationCountNonIR / ObservationCountIR are the counts
	// of non-zero observations recorded into each bucket. Useful
	// for spotting "the sampler returned zero for every action"
	// vs "no NeedsIR actions ran" failure modes.
	ObservationCountNonIR int `json:"observation_count_nonir"`
	ObservationCountIR    int `json:"observation_count_ir"`

	// GCMetrics is the runtime/MemStats + runtime/metrics snapshot
	// taken at scenario end (after Analyze returns, before
	// per-scenario cleanup). Populated for the Phase 1.6 Lever D
	// GOMEMLIMIT sweep so each sweep value can be compared on
	// HeapIdle delta, GC CPU%, etc. The snapshot is process-wide;
	// for cold scenarios with a fresh process it cleanly attributes
	// to the run, for warm/cascade it carries cold-run residual.
	GCMetrics GCMetricsSnapshot `json:"gc_metrics"`

	// Gate* are the Phase 1.7 sub-path-c clustering-gate counters.
	// Zero when clustering is disabled (MaxInFlightPackages == 0).
	// Values are cumulative across all
	// scenarios in the run (the gate is cache-shared); the harness
	// emits them per-scenario for symmetry but they are best read as
	// "totals so far".
	GateClusterAdmits   uint64 `json:"gate_cluster_admits"`
	GateNewPkgAdmits    uint64 `json:"gate_new_pkg_admits"`
	GateFallthroughHits uint64 `json:"gate_fallthrough_hits"`
	GateBlocks          uint64 `json:"gate_blocks"`
	GateWaitMs          int64  `json:"gate_wait_ms"`
	GatePeakInFlight    uint64 `json:"gate_peak_in_flight"`
}

// GCMetricsSnapshot captures runtime.MemStats and runtime/metrics GC
// CPU fraction at the end of a scenario. Populated by the harness for
// the Phase 1.6 Lever D GOMEMLIMIT sweep deliverable. All byte fields
// are the raw runtime.MemStats values at scenario end (NOT deltas) —
// the sweep cares about the steady-state shape of the heap at the
// chosen GOMEMLIMIT value, not the delta vs the previous scenario.
type GCMetricsSnapshot struct {
	// HeapInuse is bytes in in-use spans (live + dead-not-yet-swept).
	HeapInuse uint64 `json:"heap_inuse_bytes"`
	// HeapIdle is bytes in idle (unused) spans. The Phase 1.6 Lever
	// D hypothesis is that GOMEMLIMIT trims this number by triggering
	// scavenge before the run grows it further.
	HeapIdle uint64 `json:"heap_idle_bytes"`
	// HeapReleased is bytes of physical memory returned to the OS.
	// Subset of HeapIdle.
	HeapReleased uint64 `json:"heap_released_bytes"`
	// HeapSys is bytes of heap memory obtained from the OS (= HeapInuse
	// + HeapIdle including HeapReleased).
	HeapSys uint64 `json:"heap_sys_bytes"`
	// Sys is total bytes obtained from the OS (heap, stack, mspan,
	// mcache, GC overhead, etc).
	Sys uint64 `json:"sys_bytes"`
	// NumGC is the number of completed GC cycles at scenario end.
	NumGC uint32 `json:"num_gc"`
	// PauseTotalNs is the cumulative nanoseconds spent in
	// stop-the-world GC pauses at scenario end. Used as the
	// PauseTotalNs-based GC CPU% fallback when the runtime/metrics
	// path is unavailable.
	PauseTotalNs uint64 `json:"pause_total_ns"`
	// GCCPUFraction is the fraction of process CPU time used by GC
	// since program start (runtime.MemStats.GCCPUFraction, 0..1).
	// This is the primary "GC % of CPU" datum for the Phase 1.6
	// Lever D sweep stop-trigger (Phase 2 stops if > 0.40).
	GCCPUFraction float64 `json:"gc_cpu_fraction"`
}

// CascadeAggregateResult is the H-2 multi-run cascade payload. Runs
// holds N per-run ScenarioResult entries (in execution order) and
// AggregateStats holds wall/RSS distributions across the runs. When
// the harness is invoked with CascadeRuns=1 (the default), Runs has
// length 1 and AggregateStats reports that run's wall/RSS in every
// position (mean == p95 == max == min == the single value).
//
// Cache-state semantics across the N runs: Option A in the H-2
// design. All N cascade runs share the same L1/L2 cache state — the
// first cascade run invalidates the edited file's entries, runs
// 2..N hit warm. This measures "steady-state cascade cost" which is
// what OQ #6's three-consecutive arm cares about. The harness
// restores the cascade-file from its original snapshot before each
// run so all N runs start from the same source bytes; only cache
// state carries forward.
type CascadeAggregateResult struct {
	// RunCount is the number of cascade runs executed (== len(Runs)).
	RunCount int `json:"run_count"`

	// Runs is the per-run ScenarioResult slice, in execution order.
	// Run 0 is the first cascade invocation (whose fields are also
	// mirrored to BenchmarkResult.Cascade for backwards-compat).
	Runs []*ScenarioResult `json:"runs"`

	// AggregateStats summarises wall/peak-RSS across Runs.
	AggregateStats CascadeAggregateStats `json:"aggregate_stats"`
}

// CascadeAggregateStats is the wall/RSS distribution across the N
// cascade runs. Computed from CascadeAggregateResult.Runs.
type CascadeAggregateStats struct {
	MeanWallMs    int64  `json:"mean_wall_ms"`
	P95WallMs     int64  `json:"p95_wall_ms"`
	MaxWallMs     int64  `json:"max_wall_ms"`
	MinWallMs     int64  `json:"min_wall_ms"`
	MeanPeakRSSB  uint64 `json:"mean_peak_rss_bytes"`
	MaxPeakRSSB   uint64 `json:"max_peak_rss_bytes"`
}

// Duration converts a time.Duration into an int64 millisecond count.
// Hoisted so the harness uses the same conversion everywhere.
func DurationMs(d time.Duration) int64 { return d.Milliseconds() }

// StaticFallbackSnapshot pins the in-effect static fallback values
// for the scheduler's [DefaultEstimator] at the time of the run.
// Recorded so a calibration script reading this JSON can see what
// numbers the run was using without re-deriving them from the
// source.
type StaticFallbackSnapshot struct {
	NonIRBytes uint64 `json:"non_ir_bytes"`
	IRBytes    uint64 `json:"ir_bytes"`
}

// PeakObservations is the highest-magnitude per-bucket observation
// deltas the harness recorded across the entire run. Capped per
// bucket so the JSON output stays bounded; the cap is
// maxActionPeakObservations.
type PeakObservations struct {
	NonIR []uint64 `json:"non_ir"`
	IR    []uint64 `json:"ir"`
}

const maxActionPeakObservations = 32
