// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

// observation.go defines the per-action RSS observation source the
// cache's defer block in analysis.go uses to feed [DefaultEstimator].
// W9 inlined a runtime.MemStats.HeapAlloc delta at the call site;
// W10 hoists the source behind the [Sampler] interface so the harness
// + calibration tooling can A/B alternatives without touching the hot
// path.
//
// Why this lives in the scheduler package rather than its own:
//
//   - The cache→scheduler boundary is the only existing seam between
//     "action.exec deferred block" and "estimator Observe". Adding a
//     third package would force callers to import it explicitly; the
//     existing cache.ActionScheduler interface already passes through
//     the scheduler instance.
//   - The Sampler value is *per-action*: a fresh Sample is taken at
//     gate-admit, the deferred block calls Delta to produce the
//     observation. The lifetime matches the per-action goroutine, so
//     no shared state is needed.
//
// Sampler is NOT a sound per-action RSS measurement. All current
// implementations read process-wide counters, so concurrent actions
// contaminate each other's observations. The estimator's
// sliding-window median (observationsBeforeMedian=10 plus a
// 128-sample max) smooths the noise; the static fallbacks in
// [DefaultEstimator] dominate until that window fills.

import (
	"bytes"
	"os"
	"runtime"
	"runtime/metrics"
	"strconv"
	"sync"
)

// Sampler is the per-action observation source. The cache's
// action.exec deferred block calls NewSample at gate-admit time, then
// Delta after the analyzer body returns; the returned bytes value is
// the input to [DefaultEstimator.Observe].
//
// Implementations are NOT required to be sound per-action: the
// readings are typically process-wide, so the observed delta sums in
// concurrent allocations from every goroutine. The interface exists
// so the harness can pick the cheapest source with directionally
// useful signal; the estimator's median treats the inputs as
// signal-of-shape, not ground-truth RSS.
type Sampler interface {
	// Name returns a stable identifier for the sampler, used by the
	// benchmark harness to record which source produced an
	// observation. Examples: "heapalloc", "vmhwm", "runtimemetrics".
	Name() string

	// NewSample captures the sampler's "before" reading and returns
	// an opaque token the matching Delta call consumes. The token's
	// shape is implementation-defined; callers MUST pass the same
	// token to Delta they received from NewSample.
	NewSample() any

	// Delta returns the observed-bytes value for an action whose
	// "before" reading was captured by NewSample. The returned
	// value is the input to [RSSEstimator.Observe]; zero is legal
	// and means "no observation".
	Delta(before any) uint64
}

// ObservationSource is the value the env var
// PLAID_RSS_OBSERVATION takes. "" (the empty string) selects the
// production default, which is VmHWM on Linux and HeapAlloc on
// everything else. The W10 calibration runs use this env var to A/B
// compare sources on the same fixtures; production callers should
// leave it unset.
type ObservationSource string

const (
	// SourceDefault selects VmHWM on Linux and HeapAlloc elsewhere.
	SourceDefault ObservationSource = ""
	// SourceHeapAlloc is the W9 runtime.MemStats.HeapAlloc delta.
	// Process-wide, allocation-not-residency, very noisy under
	// concurrency. Kept for the W10 A/B comparison.
	SourceHeapAlloc ObservationSource = "heapalloc"
	// SourceVmHWM reads /proc/self/status VmHWM (high-water mark
	// of resident-set). Linux-only; monotonic so the delta gives
	// "did this action push the process to a new RSS high?".
	// Process-wide but residency-not-allocation, which is closer
	// to the budget we care about. The W10 default on Linux.
	SourceVmHWM ObservationSource = "vmhwm"
	// SourceRuntimeMetrics reads runtime/metrics
	// /gc/heap/allocs:bytes (monotonic cumulative allocations).
	// Cheaper than ReadMemStats; structurally similar to HeapAlloc
	// but unaffected by GC sweeps. Process-wide.
	SourceRuntimeMetrics ObservationSource = "runtimemetrics"
	// SourceNoop returns 0 for every Delta call. Used by the
	// benchmark harness's "scheduler-attached but observation
	// disabled" mode to isolate gate-overhead from observation
	// overhead.
	SourceNoop ObservationSource = "noop"
)

// EnvVarObservationSource is the env var the production hot path
// reads to override the default observation source.
const EnvVarObservationSource = "PLAID_RSS_OBSERVATION"

// DefaultSampler returns the production sampler. On Linux this is
// [NewVmHWMSampler]; on every other platform it falls back to
// [NewHeapAllocSampler]. The PLAID_RSS_OBSERVATION env var
// overrides the selection — see [SamplerFromEnv].
func DefaultSampler() Sampler {
	return SamplerFromEnv(os.Getenv(EnvVarObservationSource))
}

// SamplerFromEnv returns the sampler named by raw. The empty string
// selects the platform default. Unknown values fall back to the
// platform default after logging via the no-op path; the harness's
// preflight verifies the env var explicitly before relying on it.
func SamplerFromEnv(raw string) Sampler {
	switch ObservationSource(raw) {
	case SourceHeapAlloc:
		return NewHeapAllocSampler()
	case SourceVmHWM:
		return NewVmHWMSampler()
	case SourceRuntimeMetrics:
		return NewRuntimeMetricsSampler()
	case SourceNoop:
		return NewNoopSampler()
	}
	// Platform default.
	if runtime.GOOS == "linux" {
		return NewVmHWMSampler()
	}
	return NewHeapAllocSampler()
}

// NoopSampler returns 0 for every Delta call. The estimator drops
// zero observations on the floor, so attaching a NoopSampler is
// equivalent to never calling Observe.
type NoopSampler struct{}

// NewNoopSampler returns a Sampler that records nothing. Used by
// the benchmark harness to isolate gate-overhead from observation
// overhead.
func NewNoopSampler() *NoopSampler { return &NoopSampler{} }

// Name implements [Sampler.Name].
func (*NoopSampler) Name() string { return "noop" }

// NewSample implements [Sampler.NewSample].
func (*NoopSampler) NewSample() any { return nil }

// Delta implements [Sampler.Delta].
func (*NoopSampler) Delta(_ any) uint64 { return 0 }

// HeapAllocSampler reads runtime.MemStats.HeapAlloc. This is the W9
// inlined source preserved for A/B comparison. ReadMemStats holds the
// GC stop-the-world lock briefly on every call, which adds ~10-50µs
// per action; on a c1-scale run that's hundreds of millisecond of
// overhead. The number itself is allocation-not-residency, so it
// over-counts short-lived temporaries the GC reclaims before
// the gate is the actual budget input.
type HeapAllocSampler struct{}

// NewHeapAllocSampler returns the W9 HeapAlloc-delta sampler.
func NewHeapAllocSampler() *HeapAllocSampler { return &HeapAllocSampler{} }

// Name implements [Sampler.Name].
func (*HeapAllocSampler) Name() string { return "heapalloc" }

// NewSample implements [Sampler.NewSample]. Returns the HeapAlloc
// reading as a uint64 boxed in any.
func (*HeapAllocSampler) NewSample() any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// Delta implements [Sampler.Delta].
func (*HeapAllocSampler) Delta(before any) uint64 {
	b, ok := before.(uint64)
	if !ok {
		return 0
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > b {
		return ms.HeapAlloc - b
	}
	return 0
}

// VmHWMSampler reads /proc/self/status VmHWM, the kernel's
// monotonically-increasing high-water mark for the process's
// resident-set size. Linux-only (returns 0 on other platforms;
// callers should use the platform default selector instead).
//
// Why VmHWM and not VmRSS:
//
//   - VmHWM is monotonic: it never goes down. The delta therefore
//     answers "did this action push the process to a new RSS peak?",
//     which is the budget question.
//   - VmRSS fluctuates with reclaim; its delta over an action window
//     is dominated by other goroutines' GC activity rather than the
//     action itself.
//
// Caveats:
//
//   - Still process-wide: a NeedsIR analyzer that drives the peak
//     gets all the credit, even if half the bytes came from another
//     concurrent analyzer.
//   - Only changes when the kernel notices a new peak; an action
//     that runs entirely below the running peak observes 0 (which
//     the estimator drops). The static fallback continues to
//     dominate the median until enough actions push new peaks.
//   - File read overhead is ~5µs on tmpfs. Acceptable for per-action
//     sampling. Caches the parser result so the read is single-pass.
type VmHWMSampler struct{}

// NewVmHWMSampler returns the W10 default Linux sampler.
func NewVmHWMSampler() *VmHWMSampler { return &VmHWMSampler{} }

// Name implements [Sampler.Name].
func (*VmHWMSampler) Name() string { return "vmhwm" }

// NewSample implements [Sampler.NewSample].
func (*VmHWMSampler) NewSample() any { return readVmHWMBytes() }

// Delta implements [Sampler.Delta].
func (*VmHWMSampler) Delta(before any) uint64 {
	b, ok := before.(uint64)
	if !ok {
		return 0
	}
	after := readVmHWMBytes()
	if after > b {
		return after - b
	}
	return 0
}

// readVmHWMBytes returns /proc/self/status VmHWM in bytes, or 0 if
// the file cannot be read or parsed. Linux-only; on every other
// platform /proc/self/status either doesn't exist or has a different
// format.
//
// The function is intentionally allocation-light: a single
// os.ReadFile + bytes.Split over the result, no regexp. The status
// file is small (~1 kB on a typical process), so reading the whole
// thing is faster than tailoring a stream-parse.
func readVmHWMBytes() uint64 {
	buf, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	const prefix = "VmHWM:"
	for len(buf) > 0 {
		// Find the next newline-terminated line.
		var line []byte
		if nl := bytes.IndexByte(buf, '\n'); nl >= 0 {
			line = buf[:nl]
			buf = buf[nl+1:]
		} else {
			line = buf
			buf = nil
		}
		if !bytes.HasPrefix(line, []byte(prefix)) {
			continue
		}
		// Line shape: "VmHWM:\t   1234 kB".
		rest := bytes.TrimSpace(line[len(prefix):])
		// Strip trailing " kB" / " B" / " mB" suffix.
		var multiplier uint64 = 1024 // default to kB; the kernel always emits kB for VmHWM
		if i := bytes.IndexByte(rest, ' '); i >= 0 {
			unit := bytes.TrimSpace(rest[i+1:])
			switch string(bytes.ToLower(unit)) {
			case "kb":
				multiplier = 1024
			case "mb":
				multiplier = 1024 * 1024
			case "b":
				multiplier = 1
			default:
				// Unknown unit; default to kB and keep going.
			}
			rest = rest[:i]
		}
		n, err := strconv.ParseUint(string(bytes.TrimSpace(rest)), 10, 64)
		if err != nil {
			return 0
		}
		return n * multiplier
	}
	return 0
}

// RuntimeMetricsSampler reads
// runtime/metrics "/gc/heap/allocs:bytes": cumulative allocations
// since process start. Monotonic, so the delta is allocation-only
// (not residency). Cheaper than ReadMemStats — runtime/metrics does
// not take the GC lock — at the cost of being allocation-shaped
// rather than RSS-shaped.
//
// We use the cumulative "allocs" counter rather than the
// "/memory/classes/heap/objects:bytes" gauge because the gauge is
// affected by GC reclaim mid-action; the delta of a gauge under
// concurrent work is sign-confused.
//
// Initialized lazily on first NewSample so processes that never
// instantiate one avoid the runtime/metrics descriptor lookup.
//
// KNOWN LIMITATION (Phase 1.5 H-3, c1 report Finding 1). On the c1
// W10 cold benchmark (5,026 packages, 102 analyzers) this sampler
// produced cold.action_count = 88,672 vs vmhwm / heapalloc =
// 270,992, with a different diagnostic_digest. Synthetic-fixture
// evidence (bench_medium × 3 sources × 3 iterations, all identical
// digest + action_count) rules out small-fixture cache leakage.
// The c1-scale divergence mechanism is unidentified; the synthetic
// test cannot reproduce it. Until a c1-scale re-run identifies the
// mechanism, production callers should not select this source for
// c1-scale workloads; the platform default (vmhwm on Linux) is the
// safer choice. See `TestH3_ObservationSourceDivergence` in
// internal/bench for the matrix.
type RuntimeMetricsSampler struct {
	once    sync.Once
	samples []metrics.Sample
}

// NewRuntimeMetricsSampler returns a sampler backed by
// runtime/metrics. Cheap to construct; the descriptor lookup
// happens lazily.
func NewRuntimeMetricsSampler() *RuntimeMetricsSampler {
	return &RuntimeMetricsSampler{}
}

// Name implements [Sampler.Name].
func (*RuntimeMetricsSampler) Name() string { return "runtimemetrics" }

// metricNameHeapAllocsBytes is the runtime/metrics key for the
// cumulative heap-allocation byte counter. Hoisted to a const so the
// allocator-style lookup runs once.
const metricNameHeapAllocsBytes = "/gc/heap/allocs:bytes"

// ensureSamples lazily prepares the metrics.Sample slice on first
// use. Safe to call from multiple goroutines.
func (s *RuntimeMetricsSampler) ensureSamples() {
	s.once.Do(func() {
		s.samples = []metrics.Sample{{Name: metricNameHeapAllocsBytes}}
	})
}

// NewSample implements [Sampler.NewSample].
func (s *RuntimeMetricsSampler) NewSample() any {
	s.ensureSamples()
	// runtime/metrics.Read writes into the supplied slice; we can't
	// share the per-sampler slice across concurrent NewSample/Delta
	// calls without locking, so we materialize a fresh slice each
	// call. The descriptor name is the only shared state.
	local := []metrics.Sample{{Name: metricNameHeapAllocsBytes}}
	metrics.Read(local)
	if local[0].Value.Kind() != metrics.KindUint64 {
		return uint64(0)
	}
	return local[0].Value.Uint64()
}

// Delta implements [Sampler.Delta].
func (s *RuntimeMetricsSampler) Delta(before any) uint64 {
	b, ok := before.(uint64)
	if !ok {
		return 0
	}
	s.ensureSamples()
	local := []metrics.Sample{{Name: metricNameHeapAllocsBytes}}
	metrics.Read(local)
	if local[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	after := local[0].Value.Uint64()
	if after > b {
		return after - b
	}
	return 0
}
