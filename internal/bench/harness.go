// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/scheduler"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// Config controls one harness run. Zero values produce the W10
// production defaults: BudgetBytes = DefaultRSSBudgetBytes,
// MaxConcurrency = GOMAXPROCS(0), all three scenarios enabled,
// observation source = platform default.
type Config struct {
	// Fixture is the absolute path of the module root the harness
	// drives. Required.
	Fixture string

	// FixtureShape is the descriptor name for traceability. May be
	// empty for external fixtures.
	FixtureShape string

	// BudgetBytes overrides the RSSBudgetScheduler ceiling. Zero
	// selects scheduler.DefaultRSSBudgetBytes.
	BudgetBytes uint64

	// MaxConcurrency overrides the scheduler's count cap. Zero
	// selects GOMAXPROCS(0). Negative is invalid.
	MaxConcurrency int

	// ObservationSource picks the [scheduler.Sampler]. Empty
	// selects [scheduler.DefaultSampler].
	ObservationSource scheduler.ObservationSource

	// SkipWarm disables the warm scenario (still runs cold).
	SkipWarm bool

	// SkipCascade disables the cascade scenario.
	SkipCascade bool

	// AnalyzerSet selects which analyzers to wire. nil selects
	// the full W7 + W8 root set via
	// analyzers.AllPhase1RootAnalyzers() — the workload every
	// plaid-lint deployment runs. Tests can pin a smaller set
	// for quicker runs.
	AnalyzerSet func() []*settings.Analyzer

	// CacheToolVersion is the L1/L2 cache tool-version key.
	// Empty selects "plaid-lint-w10-bench".
	CacheToolVersion string

	// L2BuildEnv / L2GoVersion are passed to Cache.AttachL2. Empty
	// values default to "linux/arm64/cgo0" / "go1.22".
	L2BuildEnv  string
	L2GoVersion string

	// SchedulerEnabled forces the scheduler-attached path. Zero
	// (the default) enables the scheduler so the W10 budget gate
	// is exercised; tests pin to false to compare unscheduled
	// baselines.
	SchedulerDisabled bool

	// CascadeEditFn overrides the default cascade-mid edit. The
	// default appends a no-op comment to the cascade fixture's
	// mid file; the override is used by tests that want a
	// stronger source-level change. Returns the path that was
	// edited (so the harness can pass it to Invalidate).
	CascadeEditFn func() (string, error)

	// CascadeFile is the path the default CascadeEditFn touches.
	// Required when SkipCascade is false and CascadeEditFn is
	// nil. If empty the harness derives it from FixtureShape
	// (CascadeShape -> mid0/mid0.go).
	CascadeFile string

	// CascadeRuns is the H-2 (Phase 1.5) multi-run cascade
	// count. Zero or one selects the legacy single-run behavior
	// (Cascade in the result is the single run; CascadeAggregate
	// has one entry). Values > 1 run the cascade scenario N times
	// against the same L1/L2 cache state — Option A in the H-2
	// design — and populate CascadeAggregate.AggregateStats with
	// mean/p95/max/min across runs. The harness captures the
	// cascade-file's post-edit bytes after run 1 and writes that
	// snapshot back at the start of runs 2..N so every iteration
	// observes byte-identical source (the default applyCascadeEdit
	// stamps a UnixNano marker into the trailer, which would
	// otherwise diverge across runs). Restoring the file to its
	// pre-run state after Run returns is the caller's
	// responsibility — the CLI handles that via snapshotForRestore.
	CascadeRuns int

	// LeafEditFile is the H-1 (Phase 1.5) leaf-edit target. When
	// non-empty (and SkipLeafEdit is false) the harness runs a
	// new leaf_edit scenario between warm and cascade: append a
	// trailer to LeafEditFile (mirroring CascadeEditFn semantics)
	// then Snapshot.Analyze against the cold+warm L1/L2 caches.
	// The runbook picks a "leaf" — a file in a package with zero
	// transitive importers — so the cascade closure is trivial
	// and the measured wall is the per-leaf scheduler-floor cost
	// the E4 criterion targets. Empty disables the leaf
	// scenario.
	LeafEditFile string

	// LeafEditFn overrides the default leaf-edit. Mirrors
	// CascadeEditFn: when set the harness ignores LeafEditFile
	// and calls LeafEditFn to mutate the source, expecting the
	// returned path to be passed to Invalidate. Tests use this
	// hook to drive the leaf scenario against a synthetic
	// fixture without picking a real leaf file.
	LeafEditFn func() (string, error)

	// SkipLeafEdit disables the leaf_edit scenario even when
	// LeafEditFile/LeafEditFn is set. Mirrors SkipCascade.
	SkipLeafEdit bool

	// CascadePerRunObserver, if non-nil, is invoked at the start
	// of every cascade run (after any per-run file write, before
	// runScenario reads the cascade-file) with the run index and
	// the current cascade-file body. Test-only hook used by the
	// H-2 regression assertion that the source bytes are
	// byte-identical across all N runs; production callers (the
	// CLI) leave it nil.
	CascadePerRunObserver func(runIndex int, cascadeFileBody []byte)

	// OnScenarioDiagnostics, if non-nil, is invoked after each scenario
	// completes with the scenario label ("cold", "warm", "leaf_edit",
	// "cascade") and the canonical-form diagnostic list the harness used
	// to compute the digest. Phase 1.7 Lever J bisection hook; production
	// callers leave it nil.
	OnScenarioDiagnostics func(label string, diags []CanonicalDiagnostic)

	// GOMEMLIMITBytes is the Phase 1.6 Lever D pin: the runtime's
	// active soft-memory-limit value at the time bench.Run is
	// invoked, read back via debug.SetMemoryLimit(-1) by the CLI.
	// The harness does NOT call SetMemoryLimit itself — the CLI is
	// the only sanctioned mutation point. This field exists so the
	// recorded BenchmarkResult.GOMEMLIMITBytes is the runtime's
	// view (cross-validation), not the parsed-flag value alone.
	// Zero or math.MaxInt64 means "no effective limit" (the Go
	// runtime's unlimited default).
	GOMEMLIMITBytes int64

	// Excluder, when non-nil, drops workspace packages from the
	// analysis set before the package map is handed to Analyze.
	// Phase 1.7 sub-path (f): lets the bench measure
	// "what users actually lint" by honoring .golangci.yaml's
	// path-exclusion lists. Filtering is package-level only — a
	// package is either fully analyzed or fully skipped; no
	// file-level Pass.Files trim.
	Excluder *PackageExcluder

	// ShareSystemGOCACHE disables the per-invocation GOCACHE
	// isolation. The default (false) creates a fresh GOCACHE
	// directory for each Run so consecutive invocations can't see
	// stale gcexportdata from each other's go/packages loads — that
	// staleness is what produced the cold↔warm equivalence failure
	// recorded as LEARN-FGL-004. Operators iterating on bench code
	// who want to amortise `go list` cost across runs can set this
	// to true; benchmark output is then non-deterministic across
	// consecutive runs by design.
	ShareSystemGOCACHE bool

	// PhaseTraceFn is the Phase 1.7 sub-path-c C.0 spike hook for
	// emitting phase-boundary timestamps. When non-nil, runScenario
	// calls it with a stable label at each phase boundary it can
	// observe from outside the gopls driver. Boundary labels:
	//   - "<scenario>:start"
	//   - "<scenario>:init_workspace_start"
	//   - "<scenario>:init_workspace_end"
	//   - "<scenario>:metadata_loaded"      (count = total workspace packages, after exclude filter)
	//   - "<scenario>:analyze_start"        (count = packages handed to Analyze)
	//   - "<scenario>:analyze_end"          (count = diagnostic count)
	// The harness cannot observe per-package typecheck or
	// per-action boundaries — those live inside snap.Inner().Analyze
	// and would require touching internal/gopls/cache. C.0's scope
	// is investigation only; the engine is not modified.
	//
	// Default disabled. Production hot path is unaffected when nil.
	PhaseTraceFn func(label string, count int)

	// MaxInFlightPackages enables the Phase 1.7 sub-path-c
	// clustering bias at the outer analysis limiter. Zero (the
	// default) preserves prior behavior (cap = GOMAXPROCS, no per-
	// package affinity). Non-zero caps distinct in-flight packages
	// at the supplied value while keeping the worker cap at
	// GOMAXPROCS.
	MaxInFlightPackages int

	// StreamingIR is the Phase 1.8 sub-path-(c'') mode flag. When
	// true, the harness asserts MaxInFlightPackages == 1 (or sets it
	// to 1 if zero) — the streaming-single-IR configuration that
	// pins distinct in-flight packages to one. The flag is recorded
	// on BenchmarkResult for the Phase 1.8 verdict memo.
	StreamingIR bool

	// AllowColdWarmDigestMismatch demotes the cold↔warm equivalence
	// assertion from a hard error to a flag in BenchmarkResult.
	// Phase 1.7 sub-path-c C.4 surfaced a pre-existing c1-scale
	// determinism issue (cold and warm produce different diagnostic
	// digests at N=0, with no clustering involved); the sweep's
	// headline measurements (peak, walls, clustering counters) are
	// still useful even though digest equivalence cannot be
	// asserted. The default (false) preserves the production hot
	// path's hard-stop contract.
	AllowColdWarmDigestMismatch bool

	// GOMAXPROCS overrides the Go runtime's GOMAXPROCS for the
	// duration of Run. Zero (the default) leaves the runtime value
	// untouched; the host's default applies. Positive values apply
	// via runtime.GOMAXPROCS(N) before the analysis gate / outer
	// limiter / scheduler are constructed, and the prior value is
	// restored on Run exit. The lazy gate constructors in
	// internal/gopls/cache (analysisgate, check.cpulimit, parse_cache,
	// symbols.symbolize) all read runtime.GOMAXPROCS(0) at first use,
	// so setting it here propagates to every per-package limiter
	// without further plumbing. Phase 1.8 sub-path-(b).
	GOMAXPROCS int
}

// Run executes the harness against cfg and returns a
// [BenchmarkResult].
//
// Run is NOT concurrent-safe. It mutates process-global state —
// settings.AllAnalyzers (restored via defer), the GOPLSCACHE env var
// (overwritten for the duration of the run), and (unless
// cfg.ShareSystemGOCACHE is true) the GOCACHE env var (overwritten
// and restored to its prior value via defer). Single-process CLI use
// only; do not call Run from multiple goroutines. W11 may revisit if
// a test harness needs parallel Run, but that work is out of scope
// here.
func Run(ctx context.Context, cfg Config) (*BenchmarkResult, error) {
	if cfg.Fixture == "" {
		return nil, errors.New("bench.Run: Fixture is required")
	}
	if cfg.CacheToolVersion == "" {
		cfg.CacheToolVersion = "plaid-lint-w10-bench"
	}
	if cfg.L2BuildEnv == "" {
		cfg.L2BuildEnv = "linux/arm64/cgo0"
	}
	if cfg.L2GoVersion == "" {
		cfg.L2GoVersion = "go1.22"
	}
	if cfg.AnalyzerSet == nil {
		cfg.AnalyzerSet = defaultAnalyzerSet
	}
	if cfg.BudgetBytes == 0 {
		cfg.BudgetBytes = scheduler.DefaultRSSBudgetBytes
	}

	// Phase 1.8 sub-path-(b): apply cfg.GOMAXPROCS BEFORE
	// MaxConcurrency defaulting (which reads runtime.GOMAXPROCS(0))
	// and BEFORE any per-package limiter / gate constructor
	// (analysisgate, check.cpulimit, parse_cache, symbols, scheduler)
	// caches the runtime value. Restore the prior value on exit so
	// callers iterating Run don't accidentally inherit a leaked
	// GOMAXPROCS from a prior config.
	if cfg.GOMAXPROCS < 0 {
		return nil, fmt.Errorf("bench.Run: GOMAXPROCS=%d must be >= 0", cfg.GOMAXPROCS)
	}
	if cfg.GOMAXPROCS > 0 {
		prevGOMAXPROCS := runtime.GOMAXPROCS(cfg.GOMAXPROCS)
		defer runtime.GOMAXPROCS(prevGOMAXPROCS)
	}

	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = runtime.GOMAXPROCS(0)
	}

	// Phase 1.8 sub-path-(c''): --streaming-ir is an opinionated
	// alias for --max-in-flight-packages=1. Allow MaxInFlightPackages
	// == 0 (auto-fill) or == 1 (explicit). Anything else is a
	// configuration error: --streaming-ir is by definition N=1.
	if cfg.StreamingIR {
		switch cfg.MaxInFlightPackages {
		case 0:
			cfg.MaxInFlightPackages = 1
		case 1:
			// already explicit; no-op.
		default:
			return nil, fmt.Errorf("bench.Run: StreamingIR is true but MaxInFlightPackages=%d (must be 0 or 1)", cfg.MaxInFlightPackages)
		}
	}

	// LEARN-FGL-006: fail fast if the cascade-file is unreachable.
	// Without this, the first os.ReadFile lands inside applyCascadeEdit
	// 7-15 min into the run, after cold+warm have already burned.
	if !cfg.SkipCascade && cfg.CascadeEditFn == nil {
		if resolved := resolveCascadeFilePath(cfg); resolved != "" {
			if _, err := os.Stat(resolved); err != nil {
				return nil, fmt.Errorf("bench.Run: cannot stat cascade-file %q (resolved as %q): %w", cfg.CascadeFile, resolved, err)
			}
		}
	}

	prev := settings.AllAnalyzers
	settings.AllAnalyzers = cfg.AnalyzerSet()
	defer func() { settings.AllAnalyzers = prev }()

	// Per-run temp cache dirs. The harness intentionally creates
	// fresh L1 / L2 dirs so the cold scenario sees an empty cache.
	l1Dir, err := os.MkdirTemp("", "plaid-bench-l1-")
	if err != nil {
		return nil, fmt.Errorf("mkdir L1: %w", err)
	}
	defer os.RemoveAll(l1Dir)
	l2Dir, err := os.MkdirTemp("", "plaid-bench-l2-")
	if err != nil {
		return nil, fmt.Errorf("mkdir L2: %w", err)
	}
	defer os.RemoveAll(l2Dir)
	goplsCache, err := os.MkdirTemp("", "plaid-bench-gopls-")
	if err != nil {
		return nil, fmt.Errorf("mkdir gopls: %w", err)
	}
	defer os.RemoveAll(goplsCache)
	if err := os.Setenv("GOPLSCACHE", goplsCache); err != nil {
		return nil, err
	}

	// Per-invocation GOCACHE isolation. The bench's L1/L2 dirs are
	// already fresh, but go/packages.Load (invoked indirectly via
	// Snapshot.InitializeWorkspace -> go list) honors the ambient
	// GOCACHE for compiled-package metadata + gcexportdata. When two
	// consecutive bench runs share GOCACHE the second run can see
	// stale entries from the first, which shifts the diagnostic set
	// the analyzers report and breaks cold↔warm equivalence. See
	// LEARN-FGL-004. Default isolates; ShareSystemGOCACHE opts out
	// for bench-code iteration where the few extra minutes of cold
	// `go list` work aren't worth it.
	if !cfg.ShareSystemGOCACHE {
		gocacheDir, err := os.MkdirTemp("", "plaid-bench-gocache-")
		if err != nil {
			return nil, fmt.Errorf("mkdir GOCACHE: %w", err)
		}
		defer os.RemoveAll(gocacheDir)
		prevGOCACHE, hadGOCACHE := os.LookupEnv("GOCACHE")
		if err := os.Setenv("GOCACHE", gocacheDir); err != nil {
			return nil, fmt.Errorf("set GOCACHE: %w", err)
		}
		defer func() {
			if hadGOCACHE {
				_ = os.Setenv("GOCACHE", prevGOCACHE)
			} else {
				_ = os.Unsetenv("GOCACHE")
			}
		}()
	}

	// L1/L2 cache instances are shared across scenarios so warm and
	// cascade can see the cold scenario's stores. The clcache.Cache
	// has no Close method — the underlying flat-file store is
	// fsync'd on every write — so the only cleanup is RemoveAll on
	// the directory, which the defer above handles.
	l1, err := clcache.Open(l1Dir)
	if err != nil {
		return nil, fmt.Errorf("Open L1: %w", err)
	}
	l2, err := clcache.Open(l2Dir)
	if err != nil {
		return nil, fmt.Errorf("Open L2: %w", err)
	}

	result := &BenchmarkResult{
		Schema:            1,
		Fixture:           cfg.Fixture,
		FixtureShape:      cfg.FixtureShape,
		ObservationSource: pickObservationName(cfg.ObservationSource),
		Platform:          runtime.GOOS + "/" + runtime.GOARCH,
		GoVersion:         runtime.Version(),
		SchedulerCap:      cfg.MaxConcurrency,
		BudgetBytes:       cfg.BudgetBytes,
		GOMAXPROCS:        runtime.GOMAXPROCS(0),
		AnalyzerCount:     len(settings.AllAnalyzers),
		StaticFallbacks: StaticFallbackSnapshot{
			NonIRBytes: scheduler.StaticEstimateNonIRBytes,
			IRBytes:    scheduler.StaticEstimateIRBytes,
		},
		GOMEMLIMITBytes:     cfg.GOMEMLIMITBytes,
		MaxInFlightPackages: cfg.MaxInFlightPackages,
		StreamingIR:         cfg.StreamingIR,
	}

	// Scenario state: peaks accumulate across cold/warm/cascade so
	// the calibration script can read a single per-NeedsIR list.
	peaks := newPeakAccumulator()

	rss := scheduler.NewRSSBudgetScheduler(cfg.BudgetBytes, cfg.MaxConcurrency)
	samp := scheduler.SamplerFromEnv(string(cfg.ObservationSource))
	if cfg.SchedulerDisabled {
		// Unscheduled run still uses a recording sampler so the
		// harness can attribute observations to peaks. With the
		// scheduler disabled the sampler is never read by the
		// production hot path; the harness owns the sampler
		// directly in this mode by attaching a recording-only
		// scheduler with a NoopSampler.
		samp = scheduler.NewNoopSampler()
	}
	rec := newRecordingSampler(samp, peaks)
	rss.SetSampler(rec)

	// Each scenario constructs a fresh *cache.Cache (so AttachL1 /
	// AttachL2 / AttachScheduler stay setup-time-only per the cache
	// API contract). Cache-side metrics (L1/L2) are therefore
	// absolute on the scenario's own cache and we subtract zero;
	// scheduler-side metrics are cumulative across scenarios because
	// the scheduler instance is shared, so we subtract the previous
	// scenario's end values.
	prevStats := scheduler.Stats{}
	zeroL1 := cache.L1Metrics{}
	zeroL2 := cache.L2Metrics{}

	coldStats, coldDigest, coldDiags, err := runScenario(ctx, cfg, rss, l1, l2, rec, []string{}, "cold")
	if err != nil {
		return nil, fmt.Errorf("cold: %w", err)
	}
	if cfg.OnScenarioDiagnostics != nil {
		cfg.OnScenarioDiagnostics("cold", coldDiags)
	}
	result.Cold = buildScenarioResult(coldStats, coldDigest, coldDiags, zeroL1, zeroL2, prevStats, rec)
	prevStats = coldStats.sched
	// Surface the workload-shape numbers from cold; warm/cascade
	// share the same workspace package set so the values match by
	// construction (see runScenario).
	result.WorkspacePackageCount = coldStats.workspacePackageCount
	result.ExcludedPackageCount = coldStats.excludedPackageCount
	if cfg.Excluder != nil {
		result.ExcludePatterns = cfg.Excluder.Patterns()
	}

	if !cfg.SkipWarm {
		// Warm scenario: same L1/L2 stores on disk. The harness drops
		// the per-scenario observation counter so warm's mean/p95
		// reflect only the warm-run actions.
		rec.resetScenario()
		warmStats, warmDigest, warmDiags, err := runScenario(ctx, cfg, rss, l1, l2, rec, []string{}, "warm")
		if err != nil {
			return nil, fmt.Errorf("warm: %w", err)
		}
		if cfg.OnScenarioDiagnostics != nil {
			cfg.OnScenarioDiagnostics("warm", warmDiags)
		}
		if warmDigest != coldDigest {
			if !cfg.AllowColdWarmDigestMismatch {
				return nil, fmt.Errorf("cold↔warm diagnostic equivalence failed:\n  cold=%s\n  warm=%s", coldDigest, warmDigest)
			}
			result.ColdWarmDigestMismatch = true
		}
		result.Warm = buildScenarioResult(warmStats, warmDigest, warmDiags, zeroL1, zeroL2, prevStats, rec)
		prevStats = warmStats.sched
	}

	// Leaf-edit scenario (H-1): runs between warm and cascade so
	// the L1/L2 caches from cold+warm are populated. Skipped when
	// LeafEditFile is empty and LeafEditFn is nil, or SkipLeafEdit
	// is set.
	if !cfg.SkipLeafEdit && (cfg.LeafEditFile != "" || cfg.LeafEditFn != nil) {
		editedFile, err := applyLeafEdit(cfg)
		if err != nil {
			return nil, fmt.Errorf("leaf-edit: %w", err)
		}
		rec.resetScenario()
		leafStats, leafDigest, leafDiags, err := runScenario(ctx, cfg, rss, l1, l2, rec, []string{editedFile}, "leaf_edit")
		if err != nil {
			return nil, fmt.Errorf("leaf-edit: %w", err)
		}
		result.LeafEdit = buildScenarioResult(leafStats, leafDigest, leafDiags, zeroL1, zeroL2, prevStats, rec)
		prevStats = leafStats.sched
	}

	if !cfg.SkipCascade {
		// Cascade scenario: edit one file and re-run. H-2 may
		// repeat the run N times (CascadeRuns > 1) to satisfy
		// OQ #6's three-consecutive arm for E5.
		runs := cfg.CascadeRuns
		if runs < 1 {
			runs = 1
		}
		// Multi-run cascade Option A invariant: every iteration
		// must observe byte-identical source for the cascade-file,
		// otherwise the package's L1 cache key (which includes
		// source content) diverges run-to-run. The default
		// applyCascadeEdit appends a UnixNano-stamped trailer, so
		// re-applying the edit each iteration would produce
		// different bytes. Strategy: run 1 applies the edit
		// normally; we capture the resulting post-edit bytes; runs
		// 2..N skip applyCascadeEdit and write the captured
		// post-edit snapshot directly. The Phase 1.5 H-2
		// regression test asserts a SHA-256 of the cascade-file at
		// the start of each run is identical across all N runs.
		cascadeFile := resolveCascadeFilePath(cfg)
		var postEditSnapshot []byte

		agg := &CascadeAggregateResult{Runs: make([]*ScenarioResult, 0, runs)}
		for i := 0; i < runs; i++ {
			var editedFile string
			if i == 0 || cfg.CascadeEditFn != nil || cascadeFile == "" {
				// Run 1, or the caller supplied a custom edit
				// function (in which case the override owns
				// determinism), or we have no resolvable file
				// path to snapshot — fall back to applying the
				// edit every iteration.
				p, err := applyCascadeEdit(cfg)
				if err != nil {
					return nil, fmt.Errorf("cascade edit (run %d): %w", i, err)
				}
				editedFile = p
				if i == 0 && cfg.CascadeEditFn == nil && cascadeFile != "" {
					b, err := os.ReadFile(cascadeFile)
					if err != nil {
						return nil, fmt.Errorf("cascade snapshot (post-edit) %s: %w", cascadeFile, err)
					}
					postEditSnapshot = b
				}
			} else {
				// Runs 2..N: restore-to-post-edit-snapshot so
				// the cascade-file bytes are byte-identical to
				// run 1. No further edit is applied.
				if err := os.WriteFile(cascadeFile, postEditSnapshot, 0o644); err != nil {
					return nil, fmt.Errorf("cascade restore (run %d): %w", i, err)
				}
				editedFile = cascadeFile
			}
			if cfg.CascadePerRunObserver != nil && cascadeFile != "" {
				body, err := os.ReadFile(cascadeFile)
				if err != nil {
					return nil, fmt.Errorf("cascade observer read (run %d): %w", i, err)
				}
				cfg.CascadePerRunObserver(i, body)
			}
			rec.resetScenario()
			cascadeStats, cascadeDigest, cascadeDiags, err := runScenario(ctx, cfg, rss, l1, l2, rec, []string{editedFile}, "cascade")
			if err != nil {
				return nil, fmt.Errorf("cascade (run %d): %w", i, err)
			}
			runResult := buildScenarioResult(cascadeStats, cascadeDigest, cascadeDiags, zeroL1, zeroL2, prevStats, rec)
			agg.Runs = append(agg.Runs, runResult)
			prevStats = cascadeStats.sched
		}
		agg.RunCount = len(agg.Runs)
		agg.AggregateStats = aggregateCascadeRuns(agg.Runs)
		result.Cascade = agg.Runs[0]
		result.CascadeAggregate = agg
	}

	result.ActionPeakObservations = peaks.snapshot()

	// Phase 1.9 WF.0 follow-up: capture true end-of-Run resident
	// bytes (VmRSS) so the JSON output distinguishes "peak working
	// set" (per-scenario VmHWM in PeakRSSBytes) from "idle
	// residency" (VmRSS after work completes + GC + madvise
	// settle). Two GC passes — the first runs finalizers, the
	// second cleans up anything those finalizers freed — followed
	// by a brief sleep so madvise(MADV_DONTNEED) has a chance to
	// return Go-runtime pages to the OS before we read VmRSS.
	runtime.GC()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	result.IdleRSSBytes = int64(readVmRSSBytes())

	return result, nil
}

// resolveCascadeFilePath mirrors applyCascadeEdit's path derivation
// so the multi-run loop can snapshot the file once before iterating.
// Returns "" when the caller is using CascadeEditFn (in which case
// the snapshot/restore is the override's responsibility).
func resolveCascadeFilePath(cfg Config) string {
	if cfg.CascadeEditFn != nil {
		return ""
	}
	if cfg.CascadeFile != "" {
		return cfg.CascadeFile
	}
	if cfg.FixtureShape == CascadeShape.Name {
		return filepath.Join(cfg.Fixture, "mid0", "mid0.go")
	}
	return ""
}

// aggregateCascadeRuns computes mean/p95/max/min wall and peak-RSS
// across the supplied per-run results. p95 is the conventional
// nearest-rank percentile (same shape as observationStats.p95). With
// a single run all four positions equal the single value.
func aggregateCascadeRuns(runs []*ScenarioResult) CascadeAggregateStats {
	if len(runs) == 0 {
		return CascadeAggregateStats{}
	}
	walls := make([]int64, 0, len(runs))
	rss := make([]uint64, 0, len(runs))
	var sumWall int64
	var sumRSS uint64
	maxWall, minWall := runs[0].WallMs, runs[0].WallMs
	var maxRSS uint64
	for _, r := range runs {
		walls = append(walls, r.WallMs)
		rss = append(rss, r.PeakRSSBytes)
		sumWall += r.WallMs
		sumRSS += r.PeakRSSBytes
		if r.WallMs > maxWall {
			maxWall = r.WallMs
		}
		if r.WallMs < minWall {
			minWall = r.WallMs
		}
		if r.PeakRSSBytes > maxRSS {
			maxRSS = r.PeakRSSBytes
		}
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	p95idx := (len(walls) * 95) / 100
	if p95idx >= len(walls) {
		p95idx = len(walls) - 1
	}
	return CascadeAggregateStats{
		MeanWallMs:   sumWall / int64(len(runs)),
		P95WallMs:    walls[p95idx],
		MaxWallMs:    maxWall,
		MinWallMs:    minWall,
		MeanPeakRSSB: sumRSS / uint64(len(runs)),
		MaxPeakRSSB:  maxRSS,
	}
}

// scenarioCounters is the bundle of post-scenario counter values
// the result builder turns into a delta.
type scenarioCounters struct {
	wall              time.Duration
	peakRSS           uint64
	diagnosticCount   int
	actionCount       uint64
	l1                cache.L1Metrics
	l2                cache.L2Metrics
	sched             scheduler.Stats
	scenarioStartRSS  uint64
	scenarioEndRSS    uint64
	scenarioStartTime time.Time
	// gc is the runtime.MemStats + GCCPUFraction snapshot at
	// scenario end. Populated for the Phase 1.6 Lever D GOMEMLIMIT
	// sweep deliverable; consumed by buildScenarioResult into the
	// per-scenario GCMetrics field.
	gc GCMetricsSnapshot

	// workspacePackageCount and excludedPackageCount record the
	// pre- and post-exclusion package counts for the scenario.
	// The harness propagates these to BenchmarkResult so callers
	// can see the workload reduction without re-running.
	workspacePackageCount int
	excludedPackageCount  int

	// gate is the cache-shared analysis-gate snapshot at scenario
	// end (Phase 1.7 sub-path-c). Zero when MaxInFlightPackages == 0.
	gate cache.AnalysisGateStats
}

func runScenario(
	ctx context.Context,
	cfg Config,
	rss *scheduler.RSSBudgetScheduler,
	l1, l2 *clcache.Cache,
	rec *recordingSampler,
	invalidatePaths []string,
	label string,
) (scenarioCounters, string, []CanonicalDiagnostic, error) {
	c := cache.New(nil)
	c.AttachL1(l1, cfg.CacheToolVersion)
	// Investigation: temporarily disable L2 via env to isolate
	// whether the cross-flow identity gap is L2-rooted.
	if os.Getenv("PLAID_ADR11_DISABLE_L2") != "1" {
		c.AttachL2(l2, cfg.L2BuildEnv, cfg.L2GoVersion, cfg.CacheToolVersion)
	}
	if !cfg.SchedulerDisabled {
		c.AttachScheduler(newRecordingScheduler(rss, rec))
	}
	if cfg.MaxInFlightPackages > 0 {
		c.SetMaxInFlightPackages(cfg.MaxInFlightPackages)
	}

	ws := workspace.NewWithCache(cfg.Fixture, c)
	defer ws.Close()

	if len(invalidatePaths) > 0 {
		// Re-read the cascade edits so the Invalidate sees them.
		_ = ws.Invalidate(invalidatePaths)
	}

	startStats := rss.Stats()
	startRSS := readVmHWMBytes()
	startWall := time.Now()

	trace := cfg.PhaseTraceFn
	if trace == nil {
		trace = func(string, int) {}
	}
	trace(label+":start", 0)

	snap := ws.Snapshot()
	if snap == nil {
		return scenarioCounters{}, "", nil, errors.New("Snapshot returned nil")
	}
	defer snap.Release()
	inner := snap.Inner()

	trace(label+":init_workspace_start", 0)
	if err := inner.InitializeWorkspace(ctx); err != nil {
		return scenarioCounters{}, "", nil, fmt.Errorf("InitializeWorkspace: %w", err)
	}
	trace(label+":init_workspace_end", 0)

	wsPkgs := inner.WorkspacePackages()
	pkgs := map[metadata.PackageID]*metadata.Package{}
	var excluded, total int
	for id := range wsPkgs.All() {
		mp := inner.Metadata(id)
		if mp == nil {
			continue
		}
		total++
		if cfg.Excluder.ShouldExcludePackage(mp, cfg.Fixture) {
			excluded++
			continue
		}
		pkgs[mp.ID] = mp
	}
	if len(pkgs) == 0 {
		return scenarioCounters{}, "", nil, errors.New("no workspace packages loaded after exclusion")
	}
	trace(label+":metadata_loaded", total)

	trace(label+":analyze_start", len(pkgs))
	diagnostics, err := inner.Analyze(ctx, pkgs, nil)
	if err != nil {
		return scenarioCounters{}, "", nil, fmt.Errorf("Analyze: %w", err)
	}
	trace(label+":analyze_end", len(diagnostics))
	wall := time.Since(startWall)
	endRSS := readVmHWMBytes()

	digest, canonical := canonicalizeDiagnostics(diagnostics)
	endStats := rss.Stats()

	// Phase 1.6 Lever D: snapshot runtime.MemStats + GC CPU
	// fraction at scenario end so the GOMEMLIMIT sweep can compare
	// HeapInuse / HeapIdle / HeapReleased / Sys / NumGC / GC CPU%
	// across sweep values. ReadMemStats stops the world briefly
	// (~microseconds on this heap shape) but we only call it once
	// per scenario, so the cost is negligible vs the scenario wall.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	gcSnap := GCMetricsSnapshot{
		HeapInuse:     ms.HeapInuse,
		HeapIdle:      ms.HeapIdle,
		HeapReleased:  ms.HeapReleased,
		HeapSys:       ms.HeapSys,
		Sys:           ms.Sys,
		NumGC:         ms.NumGC,
		PauseTotalNs:  ms.PauseTotalNs,
		GCCPUFraction: ms.GCCPUFraction,
	}

	counters := scenarioCounters{
		wall:                  wall,
		peakRSS:               rssDelta(startRSS, endRSS),
		diagnosticCount:       len(diagnostics),
		actionCount:           endStats.ActionsAcquired - startStats.ActionsAcquired,
		l1:                    c.L1Metrics(),
		l2:                    c.L2Metrics(),
		sched:                 endStats,
		scenarioStartRSS:      startRSS,
		scenarioEndRSS:        endRSS,
		scenarioStartTime:     startWall,
		gc:                    gcSnap,
		workspacePackageCount: total,
		excludedPackageCount:  excluded,
		gate:                  c.AnalysisGateSnapshot(),
	}
	_ = label
	return counters, digest, canonical, nil
}

func buildScenarioResult(s scenarioCounters, digest string, _ []CanonicalDiagnostic, prevL1 cache.L1Metrics, prevL2 cache.L2Metrics, prevStats scheduler.Stats, rec *recordingSampler) *ScenarioResult {
	r := &ScenarioResult{
		WallMs:                   DurationMs(s.wall),
		PeakRSSBytes:             s.peakRSS,
		DiagnosticCount:          s.diagnosticCount,
		DiagnosticDigest:         digest,
		ActionCount:              s.actionCount,
		L1Hits:                   s.l1.Hits - prevL1.Hits,
		L1Stores:                 s.l1.Stores - prevL1.Stores,
		L1Misses:                 s.l1.Misses - prevL1.Misses,
		L1EncodeErrors:           s.l1.EncodeFailures - prevL1.EncodeFailures,
		L2Hits:                   s.l2.Hits - prevL2.Hits,
		L2Stores:                 s.l2.Stores - prevL2.Stores,
		L2Misses:                 s.l2.Misses - prevL2.Misses,
		IRPinEvents:              s.sched.IRPinEvents - prevStats.IRPinEvents,
		IRReleaseEvents:          s.sched.IRReleaseEvents - prevStats.IRReleaseEvents,
		SchedulerBlocked:         s.sched.ActionsBlocked - prevStats.ActionsBlocked,
		SchedulerPeakConcurrency: s.sched.PeakConcurrency,
		TotalWaitMs:              DurationMs(s.sched.TotalWaitDuration - prevStats.TotalWaitDuration),
		TotalActionMs:            DurationMs(s.sched.TotalActionDuration - prevStats.TotalActionDuration),
	}
	nonIR, ir := rec.scenarioStats()
	r.MeanObservedRSSNonIR = nonIR.mean
	r.P95ObservedRSSNonIR = nonIR.p95
	r.ObservationCountNonIR = nonIR.count
	r.MeanObservedRSSIR = ir.mean
	r.P95ObservedRSSIR = ir.p95
	r.ObservationCountIR = ir.count
	r.GCMetrics = s.gc
	r.GateClusterAdmits = s.gate.ClusterAdmits
	r.GateNewPkgAdmits = s.gate.NewPkgAdmits
	r.GateFallthroughHits = s.gate.FallthroughHits
	r.GateBlocks = s.gate.Blocks
	r.GateWaitMs = DurationMs(s.gate.WaitTotal)
	r.GatePeakInFlight = s.gate.PeakInFlight
	return r
}

// pickObservationName resolves the configured ObservationSource to
// the [scheduler.Sampler.Name] the production code installed.
func pickObservationName(src scheduler.ObservationSource) string {
	return scheduler.SamplerFromEnv(string(src)).Name()
}

// defaultAnalyzerSet returns settings.NewAnalyzer-wrapped pointers
// for the full W7 + W8 root analyzer set: the W7 root analyzers
// (assign, nilfunc, nilness, printf, unusedresult, errcheck,
// ineffassign, SA1000) unioned with every staticcheck SA-* mass-wired
// in W8, deduplicating SA1000. This is the workload every plaid-
// lint deployment runs; calibrating against a smaller set was the W10
// first-pass bug.
func defaultAnalyzerSet() []*settings.Analyzer {
	all := analyzers.AllPhase1RootAnalyzers()
	out := make([]*settings.Analyzer, 0, len(all))
	for _, a := range all {
		out = append(out, settings.NewAnalyzer(a))
	}
	return out
}

// canonicalDiag mirrors the pipelinetest canonical form so the
// harness can compute a stable digest for cold↔warm equivalence.
type canonicalDiag struct {
	Source   string
	Code     string
	Message  string
	Filename string
	Line     uint32
	Column   uint32
}

// CanonicalDiagnostic is the dump-time view of one diagnostic. It
// extends canonicalDiag with FullPath so the Phase 1.7 Lever J
// bisection driver can attribute diagnostics to packages without
// touching the digest-affecting canonicalDiag form.
type CanonicalDiagnostic struct {
	Source   string `json:"Source"`
	Code     string `json:"Code"`
	Message  string `json:"Message"`
	Filename string `json:"Filename"`
	Line     uint32 `json:"Line"`
	Column   uint32 `json:"Column"`
	FullPath string `json:"full_path"`
}

func canonicalizeDiagnostics(diags []*cache.Diagnostic) (string, []CanonicalDiagnostic) {
	out := make([]CanonicalDiagnostic, 0, len(diags))
	for _, d := range diags {
		out = append(out, CanonicalDiagnostic{
			Source:   string(d.Source),
			Code:     d.Code,
			Message:  d.Message,
			Filename: filepath.Base(d.URI.Path()),
			Line:     d.Range.Start.Line,
			Column:   d.Range.Start.Character,
			FullPath: d.URI.Path(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		if out[i].Filename != out[j].Filename {
			return out[i].Filename < out[j].Filename
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Message < out[j].Message
	})
	// Digest hashes only the original 6 fields (excluding FullPath) so
	// the cold↔warm equivalence assertion is unaffected by adding the
	// FullPath field. The dump callback receives the full slice.
	digestForm := make([]canonicalDiag, len(out))
	for i, d := range out {
		digestForm[i] = canonicalDiag{
			Source:   d.Source,
			Code:     d.Code,
			Message:  d.Message,
			Filename: d.Filename,
			Line:     d.Line,
			Column:   d.Column,
		}
	}
	buf, _ := json.Marshal(digestForm)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), out
}

func rssDelta(start, end uint64) uint64 {
	if end <= start {
		return 0
	}
	return end - start
}

// readVmHWMBytes reads /proc/self/status VmHWM in bytes; mirrors the
// scheduler package's private helper. We duplicate the parse here
// rather than export it from the scheduler package because the
// scheduler's helper is hot-path optimised (a few allocations per
// action); the harness reads VmHWM exactly twice per scenario so a
// simpler implementation suffices.
func readVmHWMBytes() uint64 {
	buf, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range splitLines(buf) {
		const prefix = "VmHWM:"
		if !hasPrefixBytes(line, prefix) {
			continue
		}
		rest := trimSpaceBytes(line[len(prefix):])
		// Strip trailing " kB" suffix (kernel always emits kB for VmHWM).
		i := indexByte(rest, ' ')
		var num []byte
		if i >= 0 {
			num = trimSpaceBytes(rest[:i])
		} else {
			num = rest
		}
		n, ok := parseUint(num)
		if !ok {
			return 0
		}
		return n * 1024
	}
	return 0
}

// readVmRSSBytes reads /proc/self/status VmRSS in bytes. Unlike
// VmHWM (the high-water mark, which never decrements), VmRSS is the
// process's current resident set size and falls as the kernel
// reclaims pages released via madvise(MADV_DONTNEED). Used by Run
// to populate BenchmarkResult.IdleRSSBytes once at end-of-Run.
func readVmRSSBytes() uint64 {
	buf, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range splitLines(buf) {
		const prefix = "VmRSS:"
		if !hasPrefixBytes(line, prefix) {
			continue
		}
		rest := trimSpaceBytes(line[len(prefix):])
		i := indexByte(rest, ' ')
		var num []byte
		if i >= 0 {
			num = trimSpaceBytes(rest[:i])
		} else {
			num = rest
		}
		n, ok := parseUint(num)
		if !ok {
			return 0
		}
		return n * 1024
	}
	return 0
}

// splitLines / hasPrefixBytes / trimSpaceBytes / indexByte / parseUint
// are tiny helpers the harness uses to avoid pulling in the strconv
// package's string-conversion overhead at observation-read time. The
// scheduler package's hot-path version of readVmHWMBytes uses bytes
// + strconv directly; the harness's mileage is too low to matter
// either way, but we keep the contract identical.
func splitLines(b []byte) [][]byte {
	out := [][]byte{}
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func hasPrefixBytes(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if b[i] != p[i] {
			return false
		}
	}
	return true
}

func trimSpaceBytes(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// recordingSampler wraps another sampler and records every Delta
// return value bucketed by NeedsIR. The harness uses this for its
// per-scenario mean/p95 plus the global peak-observation list.
//
// The action-level NeedsIR bit is NOT visible at the Sampler API
// boundary (the production hot path threads it through
// ActionScheduler.Observe, not through the Sampler). The recording
// sampler therefore observes only the Delta values; the scheduler's
// estimator.Observe is the one that sees NeedsIR. We carry NeedsIR
// across the boundary by piggybacking it on the cache.ScheduledAction
// the cache passes to Observe; see [scheduler.cacheAdapter.Observe].
// The recording sampler subscribes to the scheduler's Observe stream
// via an extra hook in [cacheAdapter] so it sees both Delta-returns
// and the matching NeedsIR bit.
type recordingSampler struct {
	inner scheduler.Sampler

	mu sync.Mutex

	// scenario-scope buckets, reset between cold/warm/cascade.
	scenarioNonIR []uint64
	scenarioIR    []uint64

	peaks *peakAccumulator
}

func newRecordingSampler(inner scheduler.Sampler, peaks *peakAccumulator) *recordingSampler {
	return &recordingSampler{inner: inner, peaks: peaks}
}

// Name implements scheduler.Sampler.
func (r *recordingSampler) Name() string { return r.inner.Name() }

// NewSample implements scheduler.Sampler.
func (r *recordingSampler) NewSample() any { return r.inner.NewSample() }

// Delta implements scheduler.Sampler. The recorded value goes into
// the global peaks list immediately; the per-bucket scenario stats
// are populated by [Record] (called from the cache adapter's Observe
// path, which has the NeedsIR bit).
func (r *recordingSampler) Delta(before any) uint64 {
	v := r.inner.Delta(before)
	return v
}

// record adds v to the per-scenario bucket selected by needsIR and
// to the global peak accumulator. The recording sampler does NOT
// see Observe calls directly (the cache adapter routes them
// straight to the estimator); the harness installs a wrapping
// estimator below to capture the per-NeedsIR view.
func (r *recordingSampler) record(needsIR bool, v uint64) {
	if v == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if needsIR {
		r.scenarioIR = append(r.scenarioIR, v)
	} else {
		r.scenarioNonIR = append(r.scenarioNonIR, v)
	}
	r.peaks.add(needsIR, v)
}

func (r *recordingSampler) resetScenario() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scenarioNonIR = nil
	r.scenarioIR = nil
}

type observationStats struct {
	count int
	mean  uint64
	p95   uint64
}

func (r *recordingSampler) scenarioStats() (nonIR, ir observationStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return statsOf(r.scenarioNonIR), statsOf(r.scenarioIR)
}

func statsOf(xs []uint64) observationStats {
	if len(xs) == 0 {
		return observationStats{}
	}
	cp := make([]uint64, len(xs))
	copy(cp, xs)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var sum uint64
	for _, v := range cp {
		sum += v
	}
	mean := sum / uint64(len(cp))
	p95idx := (len(cp) * 95) / 100
	if p95idx >= len(cp) {
		p95idx = len(cp) - 1
	}
	return observationStats{count: len(cp), mean: mean, p95: cp[p95idx]}
}

// peakAccumulator keeps the K largest observations per bucket across
// the entire run. K is maxActionPeakObservations.
type peakAccumulator struct {
	mu       sync.Mutex
	nonIR    []uint64
	ir       []uint64
}

func newPeakAccumulator() *peakAccumulator {
	return &peakAccumulator{
		nonIR: make([]uint64, 0, maxActionPeakObservations),
		ir:    make([]uint64, 0, maxActionPeakObservations),
	}
}

func (p *peakAccumulator) add(needsIR bool, v uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	target := &p.nonIR
	if needsIR {
		target = &p.ir
	}
	*target = insertSortedDesc(*target, v, maxActionPeakObservations)
}

func (p *peakAccumulator) snapshot() PeakObservations {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PeakObservations{
		NonIR: append([]uint64(nil), p.nonIR...),
		IR:    append([]uint64(nil), p.ir...),
	}
}

func insertSortedDesc(xs []uint64, v uint64, cap int) []uint64 {
	if len(xs) < cap {
		xs = append(xs, v)
		sort.Slice(xs, func(i, j int) bool { return xs[i] > xs[j] })
		return xs
	}
	// xs is full and sorted descending. v is a peak iff > xs[cap-1].
	if v <= xs[cap-1] {
		return xs
	}
	xs[cap-1] = v
	sort.Slice(xs, func(i, j int) bool { return xs[i] > xs[j] })
	return xs
}

// applyLeafEdit performs the H-1 leaf-edit. It mirrors
// applyCascadeEdit semantically (append a unique comment trailer so
// the file's mtime + content hash both change) but targets
// cfg.LeafEditFile rather than the cascade-mid. The caller passes
// the returned path to Invalidate. Required preconditions are
// checked by Run before this is called.
func applyLeafEdit(cfg Config) (string, error) {
	if cfg.LeafEditFn != nil {
		return cfg.LeafEditFn()
	}
	path := cfg.LeafEditFile
	if path == "" {
		return "", errors.New("LeafEditFile empty and LeafEditFn nil")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	trailer := fmt.Sprintf("\n// bench-leaf-edit: %d\n", time.Now().UnixNano())
	body = append(body, []byte(trailer)...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// applyCascadeEdit performs the default cascade-mid edit. It appends
// a unique comment line to the cascade-mid file so the file's mtime
// + content hash both change. The caller passes the returned path to
// Invalidate.
func applyCascadeEdit(cfg Config) (string, error) {
	if cfg.CascadeEditFn != nil {
		return cfg.CascadeEditFn()
	}
	path := cfg.CascadeFile
	if path == "" {
		// Derive from FixtureShape: CascadeShape's mid is mid0/mid0.go.
		if cfg.FixtureShape == CascadeShape.Name {
			path = filepath.Join(cfg.Fixture, "mid0", "mid0.go")
		}
	}
	if path == "" {
		return "", errors.New("CascadeFile empty and FixtureShape != bench_cascade; supply CascadeEditFn or CascadeFile")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	// Append a unique trailer so the file content hash is bumped.
	trailer := fmt.Sprintf("\n// bench-cascade-edit: %d\n", time.Now().UnixNano())
	body = append(body, []byte(trailer)...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
