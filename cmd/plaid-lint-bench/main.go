// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command plaid-lint-bench is the W10 benchmark CLI for the
// plaid-lint engine. It drives [bench.Run] against a fixture and
// emits the structured [bench.BenchmarkResult] as JSON.
//
// Usage:
//
//	plaid-lint-bench --fixture=/path/to/module [--out=result.json]
//
// The CLI is the project-lead's entry point for the W10 calibration
// and the stretch-checkpoint gate-decision run.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/bench"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/quiet"
	"github.com/conductorone/plaid-lint/internal/scheduler"

	"golang.org/x/tools/go/analysis"
)

// excludeGlobs is a repeatable --exclude-glob flag accumulator. The
// flag is wired via flag.Var below so the user can pass it multiple
// times on a single command line.
type excludeGlobs []string

func (e *excludeGlobs) String() string { return strings.Join(*e, ",") }
func (e *excludeGlobs) Set(v string) error {
	if v == "" {
		return nil
	}
	*e = append(*e, v)
	return nil
}

func main() {
	var excludeGlobsFlag excludeGlobs
	var (
		fixturePath     = flag.String("fixture", "", "module root to benchmark (required)")
		fixtureShape    = flag.String("shape", "", "synthetic fixture shape name (bench_small/bench_medium/bench_cascade); leave empty for external fixtures")
		outPath         = flag.String("out", "", "output file (default: stdout)")
		budget          = flag.Uint64("budget-bytes", scheduler.DefaultRSSBudgetBytes, "RSS budget for the scheduler (bytes)")
		maxConc         = flag.Int("max-concurrency", 0, "scheduler max concurrent actions (0 = GOMAXPROCS)")
		observation     = flag.String("observation", "", "RSS observation source: vmhwm (default on linux) / heapalloc / runtimemetrics / noop")
		skipWarm        = flag.Bool("skip-warm", false, "skip the warm scenario")
		skipCascade     = flag.Bool("skip-cascade", false, "skip the cascade scenario")
		cascadeFile     = flag.String("cascade-file", "", "file to touch for the cascade scenario (overrides the bench_cascade default). The default cascade edit appends a comment trailer to this file and does NOT revert it; use --cascade-restore-on-exit to defend against accidental edits to non-disposable paths (e.g. a c1 worktree).")
		cascadeRestore  = flag.Bool("cascade-restore-on-exit", true, "copy --cascade-file before the run and restore it on exit. Defensive default for external fixtures; pass --cascade-restore-on-exit=false to keep the harness-applied edit on disk (useful for follow-up inspection).")
		cascadeRuns     = flag.Int("cascade-runs", 1, "number of cascade scenario iterations (H-2). N=1 preserves single-run behavior; N>1 runs the cascade N times against the same L1/L2 cache (Option A) and emits a cascade_aggregate field with mean/p95/max/min wall and peak-RSS distributions. OQ #6's three-consecutive arm uses N=3.")
		leafEditFile    = flag.String("leaf-edit-file", "", "path of a leaf file (a file in a package with zero transitive importers) to edit for the H-1 leaf_edit scenario. Empty disables the scenario. When set, the harness appends a comment trailer between warm and cascade, runs Snapshot.Analyze with the cold+warm caches in place, and restores the file via the cascade-restore-on-exit machinery. See runbook for selection guidance.")
		skipLeafEdit    = flag.Bool("skip-leaf-edit", false, "skip the leaf_edit scenario even when --leaf-edit-file is set")
		timeout         = flag.Duration("timeout", 30*time.Minute, "overall benchmark deadline")
		disableSched    = flag.Bool("disable-scheduler", false, "run with no scheduler attached (W7/W8 default path baseline)")
		toolVer         = flag.String("tool-version", "", "cache tool-version key (default: plaid-lint-w10-bench)")
		l2BuildEnv      = flag.String("l2-build-env", "", "L2 cache build-env tag (default: linux/arm64/cgo0)")
		l2GoVersion     = flag.String("l2-go-version", "", "L2 cache go-version tag (default: go1.22)")
		gomemlimitRaw   = flag.String("gomemlimit", "", "Phase 1.6 Lever D: set the Go runtime soft memory limit before any analyzer work begins. Accepts the standard Go formatted suffixes (e.g. 8GiB, 10G, 1073741824). When set, the bench calls debug.SetMemoryLimit(N) early in startup, then reports the active value (via debug.SetMemoryLimit(-1)) in the JSON output's top-level gomemlimit_bytes field for cross-validation. Empty (the default) leaves the runtime untouched: the GOMEMLIMIT env var (if set) governs, otherwise the limit is math.MaxInt64.")
		excludeFrom     = flag.String("exclude-from", "", "Phase 1.7 sub-path (f): `path` to a .golangci.yaml file whose path-exclusion lists should be honored. The bench parses linters.exclusions.paths, formatters.exclusions.paths, run.skip-dirs, run.skip-files. NOT supported: per-linter rules with path/path-except, presets, generated:lax/strict semantics, issues.exclude-rules. The union of patterns is matched against each workspace package's LoadDir (relative to --fixture) before Analyze runs; matching packages are skipped wholesale. File-level skip is NOT implemented in this version.")
		shareGOCACHE    = flag.Bool("share-system-gocache", false, "Phase 1.7 fix for LEARN-FGL-004: by default the bench creates a fresh GOCACHE directory per invocation and restores the prior GOCACHE env var on exit, so consecutive runs against the same source tree can't see stale gcexportdata from each other's go/packages.Load calls. Set this flag to true to inherit the operator's $GOCACHE — useful for bench-code iteration (amortises `go list` cost across runs) but produces non-deterministic cold digests across consecutive invocations by design. The W6 cold↔warm equivalence assertion is still enforced within a single Run; only cross-Run determinism is sacrificed.")
		c0PhaseTrace    = flag.String("c0-phase-trace", "", "Phase 1.7 sub-path-c C.0 investigation: `path` to a phase-trace log. When set, runScenario emits a JSONL line per observable phase boundary (scenario start, InitializeWorkspace begin/end, metadata load, Analyze begin/end). The harness cannot observe per-package typecheck / per-action boundaries — those live inside snap.Inner().Analyze. Default disabled. Production hot path is unaffected when unset.")
		maxInFlightPkg  = flag.Int("max-in-flight-packages", 0, "Phase 1.7 sub-path-c C.3 prototype: cap distinct in-flight packages at the outer analysis limiter. Zero (the default) disables clustering: the limiter behaves as the prior channel-semaphore (cap = GOMAXPROCS, no per-package affinity). Non-zero enables the clustering bias: the limiter still caps worker count at GOMAXPROCS but additionally caps distinct in-flight packages at N. A 2-second fall-through admits a new package regardless of the cluster cap when the cap is the sole reason an admission is blocked — required to avoid deadlock under the W9 RSS-budget gate.")
		allowCWMismatch = flag.Bool("allow-cold-warm-digest-mismatch", false, "Phase 1.7 sub-path-c C.4: demote the cold↔warm diagnostic-digest equivalence assertion from a hard error to a flag in BenchmarkResult.cold_warm_digest_mismatch. Required for c1 sweeps because the cold+warm+cascade+leaf shape surfaces a pre-existing c1-scale determinism issue (cold and warm produce different diagnostic digests at N=0, with no clustering involved). Default false preserves the hot-path hard-stop contract.")
		analyzerSubset  = flag.String("analyzer-subset", "", "Phase 1.7 Lever J bisection: restrict the analyzer set installed by the harness. Accepts: empty (default — full W7+W8 root set, 102 analyzers), 'w7-only' (the 7 non-SA W7 roots: assign,nilfunc,nilness,printf,unusedresult,errcheck,ineffassign — SA1000 excluded so SA-* is fully off), 'sa-only' (the 95 staticcheck SA-* checks, no W7 non-SA roots), 'no-ir' (the full root set minus analyzers whose transitive Requires graph reaches buildir/buildssa — leaves inspect-only analyzers), 'ir-only' (the complement: only analyzers that consume IR), or 'names=A,B,C' (explicit comma-separated analyzer names). Bisection-only knob; do NOT use to ship.")
		dumpDiagsDir    = flag.String("dump-diagnostics-dir", "", "Phase 1.7 Lever J bisection: write per-scenario canonical-form diagnostic JSON streams to this directory (one file per scenario, named <label>.json). Used to diff diagnostic output across GOMEMLIMIT values. Empty disables the dump.")
		streamingIR     = flag.Bool("streaming-ir", false, "Phase 1.8 sub-path-(c'') prototype: enable streaming-single-IR mode. Opinionated alias for --max-in-flight-packages=1. Cannot be combined with --max-in-flight-packages=N for N>1 (mutually exclusive). When set, the outer analysis limiter admits exactly one distinct package at a time; the 16-worker pool concentrates entirely on that package's analyzer fanout. Trades cold-tier wall (inner-fanout idles during each package's typecheck phase) for cold-tier peak working set. The 2-second fall-through admits a new package when every in-flight action is W9-budget-blocked.")
		gomaxprocs      = flag.Int("gomaxprocs", 0, "Phase 1.8 sub-path-(b): override the Go runtime's GOMAXPROCS for the duration of the bench. Zero (the default) leaves the host's runtime default untouched. Positive N applies runtime.GOMAXPROCS(N) before any per-package limiter / gate constructor reads the value, and restores the prior setting on Run exit. The analysisgate's workerCap, internal/gopls/cache's check.cpulimit and the outer analysis limiter, parse_cache's group limit, and the scheduler's default cap all derive from runtime.GOMAXPROCS(0) lazily at first use — overriding here propagates to all of them without further plumbing. The (b) sweep attacks the N × per-package-working-set ceiling along the GOMAXPROCS axis; (c) and (c'') attack the same ceiling along the in-flight-packages axis. Recorded as BenchmarkResult.gomaxprocs for forensic.")
	)
	flag.Var(&excludeGlobsFlag, "exclude-glob", "Phase 1.7 sub-path (f): exclude workspace packages whose LoadDir matches the supplied filepath.Match-style `glob`. Repeatable. Patterns with / are matched against the path relative to --fixture; patterns without are matched against the basename. Supports ** for spanning path separators (e.g. **/*.gen.go). Combinable with --exclude-from; effective exclude set is the union.")
	// Phase 1.6 Lever A investigation: --profile-allocs captures
	// heap pprof + memstats + smaps_rollup at each new VmHWM
	// maximum. Opt-in; no production-path effect when not set.
	flag.BoolVar(&profileAllocs, "profile-allocs", false, "Phase 1.6 Lever A: capture heap pprof + memstats + smaps_rollup at each new VmHWM maximum. Writes to --profile-allocs-dir.")
	flag.StringVar(&profileAllocsDir, "profile-allocs-dir", "", "destination directory for --profile-allocs captures (required when --profile-allocs is set)")
	flag.IntVar(&memProfileRate, "memprofile-rate", 512*1024, "runtime.MemProfileRate for --profile-allocs (default 512K matches Phase 1.5 baseline; pass 1 for every allocation, 4096/65536 for intermediate)")
	flag.DurationVar(&peakSampleHz, "peak-sample-cadence", 1*time.Second, "polling cadence for the VmHWM peak watcher; only relevant when --profile-allocs is set")
	wfPhaseCapture := flag.Bool("wf-phase-capture", false, "Phase 1.9 / WF.0 workspace-residency-floor attribution: when set together with --profile-allocs, force a heap pprof + memstats + smaps_rollup capture at each PhaseTraceFn boundary (overrides the new-VmHWM-only trigger), and additionally fire a runtime.GC()-then-capture at the very end of Run for the idle-post-run residency-floor measurement. Requires --profile-allocs. No effect when --profile-allocs is unset.")
	quietFlag := flag.Bool("quiet", false, "Suppress upstream debug-trace lines on stderr (`new node, remapping ...`, `deduplicating ...` from honnef.co/go/tools/unused). Equivalent to LOG_LEVEL=warn. A medium-workspace cold trial emits ~2GB of these per run with the filter off.")
	flag.Parse()

	if *quietFlag || quiet.FromEnv() {
		restore := quiet.Install()
		defer restore()
	}

	if *fixturePath == "" {
		fmt.Fprintln(os.Stderr, "plaid-lint-bench: --fixture is required")
		flag.Usage()
		os.Exit(2)
	}

	// Phase 1.8 sub-path-(c''): --streaming-ir is shorthand for
	// --max-in-flight-packages=1. Reject combinations that contradict
	// (any explicit N>1 with --streaming-ir on). When --streaming-ir
	// is on and --max-in-flight-packages is zero (default), promote
	// it to 1 here so the rest of the bench flow sees the canonical
	// numeric cap. Explicit --max-in-flight-packages=1 +
	// --streaming-ir is allowed and equivalent.
	if *streamingIR {
		switch *maxInFlightPkg {
		case 0:
			*maxInFlightPkg = 1
		case 1:
			// equivalent; keep as-is.
		default:
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --streaming-ir is mutually exclusive with --max-in-flight-packages=%d (must be 0 or 1; --streaming-ir is N=1).\n", *maxInFlightPkg)
			os.Exit(2)
		}
	}
	abs, err := filepath.Abs(*fixturePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: resolve --fixture: %v\n", err)
		os.Exit(2)
	}

	// Phase 1.6 Lever D: apply --gomemlimit before any analyzer work.
	// The result.GOMEMLIMITBytes field below records the value the
	// runtime is actually using, read back via debug.SetMemoryLimit(-1)
	// (a no-op read returning the current limit). When the flag is
	// unset we still record the runtime's view of GOMEMLIMIT so the
	// JSON output is self-describing — env-var GOMEMLIMIT (without
	// --gomemlimit) shows up unchanged in the recorded value.
	if *gomemlimitRaw != "" {
		n, err := parseGoMemSize(*gomemlimitRaw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --gomemlimit=%q: %v\n", *gomemlimitRaw, err)
			os.Exit(2)
		}
		debug.SetMemoryLimit(n)
	}
	gomemlimitActive := debug.SetMemoryLimit(-1)
	if *gomemlimitRaw != "" {
		// Cross-validation: the flag asked for N; the runtime should
		// report N. If they diverge, surface — the sweep data would
		// otherwise be invalid.
		want, _ := parseGoMemSize(*gomemlimitRaw)
		if gomemlimitActive != want {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: GOMEMLIMIT not active: requested %d bytes, runtime reports %d. STOP.\n", want, gomemlimitActive)
			os.Exit(1)
		}
	}

	// Phase 1.7 sub-path (f): assemble the exclude pattern set from
	// --exclude-glob (repeatable) and --exclude-from (one YAML
	// path). The empty case (no flags) produces a nil excluder so
	// the harness's hot path is byte-identical to the unflagged
	// baseline.
	var yamlPaths []string
	if *excludeFrom != "" {
		p, err := bench.LoadExcludePathsFromYAML(*excludeFrom)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --exclude-from=%q: %v\n", *excludeFrom, err)
			os.Exit(2)
		}
		yamlPaths = p
	}
	excluder, err := bench.NewPackageExcluder([]string(excludeGlobsFlag), yamlPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: --exclude-glob: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Phase 1.6 Lever A: start the alloc profiler if --profile-allocs
	// is set. No-op otherwise. The manifest flush is deferred so
	// SIGINT/panic exits still write summary.json.
	if err := startAllocProfiler(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: --profile-allocs: %v\n", err)
		os.Exit(2)
	}
	defer flushAllocProfilerManifest()

	// Honor SIGINT/SIGTERM so a long c1-scale run can be aborted cleanly.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	cfg := bench.Config{
		Fixture:                     abs,
		FixtureShape:                *fixtureShape,
		BudgetBytes:                 *budget,
		MaxConcurrency:              *maxConc,
		ObservationSource:           scheduler.ObservationSource(*observation),
		SkipWarm:                    *skipWarm,
		SkipCascade:                 *skipCascade,
		CacheToolVersion:            *toolVer,
		L2BuildEnv:                  *l2BuildEnv,
		L2GoVersion:                 *l2GoVersion,
		SchedulerDisabled:           *disableSched,
		CascadeFile:                 *cascadeFile,
		CascadeRuns:                 *cascadeRuns,
		LeafEditFile:                *leafEditFile,
		SkipLeafEdit:                *skipLeafEdit,
		GOMEMLIMITBytes:             gomemlimitActive,
		Excluder:                    excluder,
		ShareSystemGOCACHE:          *shareGOCACHE,
		MaxInFlightPackages:         *maxInFlightPkg,
		AllowColdWarmDigestMismatch: *allowCWMismatch,
		StreamingIR:                 *streamingIR,
		GOMAXPROCS:                  *gomaxprocs,
	}

	// Phase 1.7 sub-path-c C.0 phase-trace: open the log file and
	// install a thread-safe writer that records monotonic-clock
	// offsets per phase boundary. Default disabled (no-op when
	// --c0-phase-trace is empty).
	if *c0PhaseTrace != "" {
		tf, terr := os.Create(*c0PhaseTrace)
		if terr != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --c0-phase-trace=%q: %v\n", *c0PhaseTrace, terr)
			os.Exit(2)
		}
		defer tf.Close()
		var traceMu sync.Mutex
		traceStart := time.Now()
		cfg.PhaseTraceFn = func(lbl string, count int) {
			traceMu.Lock()
			defer traceMu.Unlock()
			ts := time.Since(traceStart).Seconds()
			fmt.Fprintf(tf, `{"t_sec":%.3f,"label":%q,"count":%d}`+"\n", ts, lbl, count)
		}
	}

	// Phase 1.9 / WF.0 workspace-residency-floor attribution: when
	// --wf-phase-capture is set, additionally trigger a forced
	// heap-pprof capture at each PhaseTraceFn boundary. Wraps the
	// caller's PhaseTraceFn (if any) so --c0-phase-trace and
	// --wf-phase-capture compose. Requires --profile-allocs to be
	// set (otherwise CapturePhase is a no-op).
	if *wfPhaseCapture {
		if !profileAllocs {
			fmt.Fprintln(os.Stderr, "plaid-lint-bench: --wf-phase-capture requires --profile-allocs")
			os.Exit(2)
		}
		prev := cfg.PhaseTraceFn
		cfg.PhaseTraceFn = func(lbl string, count int) {
			if prev != nil {
				prev(lbl, count)
			}
			// Force-capture the heap at this phase boundary. The
			// label is embedded in the on-disk filename via
			// CapturePhase. We don't GC here — the per-phase
			// capture should reflect the heap as the engine sees
			// it, not the GC-collected steady state. The final
			// idle-post-run capture (below) does GC.
			CapturePhase("trace_"+lbl, false)
		}
	}

	if *dumpDiagsDir != "" {
		if err := os.MkdirAll(*dumpDiagsDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --dump-diagnostics-dir=%q: %v\n", *dumpDiagsDir, err)
			os.Exit(2)
		}
		dir := *dumpDiagsDir
		cfg.OnScenarioDiagnostics = func(label string, diags []bench.CanonicalDiagnostic) {
			path := filepath.Join(dir, label+".json")
			buf, err := json.MarshalIndent(diags, "", "  ")
			if err != nil {
				fmt.Fprintf(os.Stderr, "plaid-lint-bench: marshal diags (%s): %v\n", label, err)
				return
			}
			if err := os.WriteFile(path, append(buf, '\n'), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "plaid-lint-bench: write %s: %v\n", path, err)
			}
		}
	}

	if *analyzerSubset != "" {
		set, err := buildAnalyzerSubset(*analyzerSubset)
		if err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --analyzer-subset=%q: %v\n", *analyzerSubset, err)
			os.Exit(2)
		}
		if len(set) == 0 {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: --analyzer-subset=%q resolved to 0 analyzers\n", *analyzerSubset)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: --analyzer-subset=%q -> %d analyzers\n", *analyzerSubset, len(set))
		captured := set
		cfg.AnalyzerSet = func() []*settings.Analyzer { return captured }
	}

	// If --cascade-restore-on-exit is set (the default) and the run
	// will actually perform a cascade edit, snapshot the target file
	// before bench.Run mutates it. We restore in a defer so SIGINT,
	// panic, and normal-exit paths all get the rollback.
	//
	// H-1 (Phase 1.5) extends the same snapshot/defer pattern to the
	// leaf-edit target: when --leaf-edit-file is set and not skipped,
	// the same path is mutated in place by applyLeafEdit and must be
	// restored on exit.
	if !*skipCascade && *cascadeRestore && cfg.CascadeEditFn == nil {
		target := resolveCascadeFile(cfg)
		if target != "" {
			restore, rerr := snapshotForRestore(target)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "plaid-lint-bench: snapshot cascade file: %v\n", rerr)
				os.Exit(1)
			}
			defer func() {
				if err := restore(); err != nil {
					fmt.Fprintf(os.Stderr, "plaid-lint-bench: restore cascade file %s: %v\n", target, err)
				}
			}()
		}
	}
	if !*skipLeafEdit && *cascadeRestore && cfg.LeafEditFn == nil && cfg.LeafEditFile != "" {
		restore, rerr := snapshotForRestore(cfg.LeafEditFile)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: snapshot leaf-edit file: %v\n", rerr)
			os.Exit(1)
		}
		defer func() {
			if err := restore(); err != nil {
				fmt.Fprintf(os.Stderr, "plaid-lint-bench: restore leaf-edit file %s: %v\n", cfg.LeafEditFile, err)
			}
		}()
	}

	start := time.Now()
	result, err := bench.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: run failed after %s: %v\n", time.Since(start), err)
		os.Exit(1)
	}

	// Phase 1.9 / WF.0 workspace-residency-floor attribution: after
	// Run returns the snapshot has been Closed (its defer ran) and
	// no in-flight work remains. Force a GC and capture the heap;
	// HeapInuse at this point is the irreducible "what's still
	// referenced even with no in-flight scenario" residency floor.
	if *wfPhaseCapture {
		CapturePhase("idle_post_run", true)
		// Second capture after another GC for stability: any
		// finalizers from the first GC may have released more.
		CapturePhase("idle_post_run_gc2", true)
	}

	buf, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: marshal result: %v\n", err)
		os.Exit(1)
	}
	buf = append(buf, '\n')

	if *outPath == "" {
		if _, err := os.Stdout.Write(buf); err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint-bench: write stdout: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*outPath, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint-bench: write %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "plaid-lint-bench: wrote %s in %s\n", *outPath, time.Since(start))
}

// resolveCascadeFile mirrors bench.applyCascadeEdit's path-derivation
// logic so the CLI knows which file the default cascade edit will
// touch. Returns "" if no path can be resolved (in which case
// bench.Run will return an error before mutating anything).
func resolveCascadeFile(cfg bench.Config) string {
	if cfg.CascadeFile != "" {
		return cfg.CascadeFile
	}
	if cfg.FixtureShape == bench.CascadeShape.Name {
		return filepath.Join(cfg.Fixture, "mid0", "mid0.go")
	}
	return ""
}

// parseGoMemSize parses a Go-style memory-size string into a byte
// count for Phase 1.6 Lever D's --gomemlimit flag. The accepted shape
// matches debug.SetMemoryLimit's documented input form so callers can
// write 8GiB / 10G / 1073741824 interchangeably. Returns an error on
// negative values, fractional digits, or unrecognised suffixes. A
// missing or empty suffix is treated as bytes.
//
// Recognised suffixes (case-sensitive, matching go runtime conventions):
//
//   - B / "" — bytes
//   - KiB / KB / K — kibibytes (1<<10) ; both KB and K are aliases for KiB to match GOMEMLIMIT parsing
//   - MiB / MB / M — mebibytes (1<<20)
//   - GiB / GB / G — gibibytes (1<<30)
//   - TiB / TB / T — tebibytes (1<<40)
//
// Note that the Go runtime treats KB/MB/GB as 1024-based (the IEC
// "Ki/Mi/Gi" units) for GOMEMLIMIT parsing per
// pkg.go.dev/runtime#hdr-Environment_Variables; this function follows
// the same rule.
func parseGoMemSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty value")
	}
	// Strip the longest matching suffix.
	type unit struct {
		suffix string
		shift  uint
	}
	units := []unit{
		{"TiB", 40}, {"GiB", 30}, {"MiB", 20}, {"KiB", 10},
		{"TB", 40}, {"GB", 30}, {"MB", 20}, {"KB", 10},
		{"T", 40}, {"G", 30}, {"M", 20}, {"K", 10},
		{"B", 0},
	}
	var num string
	var shift uint
	matched := false
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num = strings.TrimSpace(s[:len(s)-len(u.suffix)])
			shift = u.shift
			matched = true
			break
		}
	}
	if !matched {
		num = s
		shift = 0
	}
	if num == "" {
		return 0, fmt.Errorf("no numeric component in %q", s)
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", num, err)
	}
	// Overflow check before the shift.
	if shift > 0 && n > uint64(math.MaxInt64)>>shift {
		return 0, fmt.Errorf("value %s overflows int64", s)
	}
	v := int64(n << shift)
	if v < 0 {
		return 0, fmt.Errorf("value %s overflows int64", s)
	}
	return v, nil
}

// buildAnalyzerSubset resolves the --analyzer-subset CLI flag into a
// concrete []*settings.Analyzer. Phase 1.7 Lever J bisection knob; the
// production default (an empty flag) leaves cfg.AnalyzerSet=nil and the
// harness installs the full W7+W8 root set via defaultAnalyzerSet.
func buildAnalyzerSubset(spec string) ([]*settings.Analyzer, error) {
	spec = strings.TrimSpace(spec)
	full := analyzers.AllPhase1RootAnalyzers()
	var picked []*analysis.Analyzer
	switch {
	case spec == "w7-only":
		// W7 non-SA roots: assign, nilfunc, nilness, printf, unusedresult,
		// errcheck, ineffassign. SA1000 is in AllBundledAnalyzers too but
		// we explicitly drop it here so the SA-* category is fully off.
		for _, a := range full {
			if a == nil {
				continue
			}
			if strings.HasPrefix(a.Name, "SA") {
				continue
			}
			picked = append(picked, a)
		}
	case spec == "sa-only":
		for _, a := range full {
			if a == nil {
				continue
			}
			if !strings.HasPrefix(a.Name, "SA") {
				continue
			}
			picked = append(picked, a)
		}
	case spec == "no-ir":
		for _, a := range full {
			if a == nil {
				continue
			}
			if analyzers.AnalyzerRequiresIR(a) {
				continue
			}
			picked = append(picked, a)
		}
	case spec == "ir-only":
		for _, a := range full {
			if a == nil {
				continue
			}
			if !analyzers.AnalyzerRequiresIR(a) {
				continue
			}
			picked = append(picked, a)
		}
	case strings.HasPrefix(spec, "names="):
		want := make(map[string]bool)
		for _, name := range strings.Split(strings.TrimPrefix(spec, "names="), ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			want[name] = true
		}
		if len(want) == 0 {
			return nil, errors.New("names= subset is empty")
		}
		seen := make(map[string]bool)
		for _, a := range full {
			if a == nil {
				continue
			}
			if want[a.Name] {
				picked = append(picked, a)
				seen[a.Name] = true
			}
		}
		var missing []string
		for n := range want {
			if !seen[n] {
				missing = append(missing, n)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("analyzers not in the root set: %s", strings.Join(missing, ","))
		}
	default:
		return nil, fmt.Errorf("unknown subset spec %q (expected w7-only, sa-only, no-ir, ir-only, or names=A,B,C)", spec)
	}
	out := make([]*settings.Analyzer, 0, len(picked))
	for _, a := range picked {
		out = append(out, settings.NewAnalyzer(a))
	}
	return out, nil
}

// snapshotForRestore reads the current contents + mode of path and
// returns a closure that restores them. The closure is idempotent
// against a missing file (returns nil) and against a path-write
// failure (returns the error to the caller). Callers should defer
// the returned closure.
//
// snapshotForRestore is intended for the cascade-edit footgun the
// harness's default CascadeEditFn introduces: a comment trailer is
// appended to the target and never reverted. Disposable synthetic
// fixtures don't care; the c1 worktree path strongly does.
func snapshotForRestore(path string) (func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Nothing to restore; the harness will fail with its own
			// error when it tries to read the file.
			return func() error { return nil }, nil
		}
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	mode := stat.Mode().Perm()
	return func() error {
		return os.WriteFile(path, body, mode)
	}, nil
}
