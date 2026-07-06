// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// RunInput bundles everything engine.Run needs from the CLI: the
// parsed/validated config, the resolved linter set, the workspace
// identity (module root + build tags + env), and the two on-disk
// caches.
//
// The caller owns the L1/L2 cache lifecycle: engine.Run does NOT
// close them (the underlying clcache.Cache has no Close anyway —
// the on-disk store is fsync'd per write).
type RunInput struct {
	// Config is the parsed + validated config. Required.
	Config *config.Config

	// Registry is the resolved linter set from internal/registry.
	// Required.
	Registry *registry.Registry

	// Workspace identifies the module root and the build environment.
	// Required. Workspace.ModuleRoot must be absolute.
	Workspace subproc.WorkspaceRef

	// L1 is the per-(package, analyzer) cache. Required.
	L1 *clcache.Cache

	// L2 is the gcexportdata cache. Required.
	L2 *clcache.Cache

	// L2BuildEnv / L2GoVersion are folded into the L2 action ID so
	// entries from a different toolchain or platform never collide.
	// Empty selects the runtime's GOOS/GOARCH and runtime.Version().
	L2BuildEnv  string
	L2GoVersion string

	// CacheToolVersion is the cache tool-version key. Empty selects
	// a stable default. A bump invalidates the L1/L2 caches.
	CacheToolVersion string

	// SubprocCache is the optional shared subproc cache (built via
	// subproc.OpenCache). Nil disables subproc-side caching.
	SubprocCache subproc.Cache

	// TargetPatterns is the user-supplied set of go/packages query
	// patterns from the CLI's positional args (e.g.
	// `./pkg/foo/...`, `/abs/path/...`, `bare/import/path/...`).
	// Empty selects the historical "load everything under the
	// module root" behavior (`./...`); a non-empty list narrows
	// the initial workspace load to the union of those patterns.
	// Transitive deps still populate the metadata graph as
	// imports, but only packages selected by the patterns drive
	// analysis.
	TargetPatterns []string

	// L0 is the optional per-(package, analyzer-set) diagnostic
	// cache. When non-nil, runInProcess looks up each workspace
	// package's L0 key before invoking Snapshot.Analyze and serves
	// cached diagnostics on hit; on miss the package goes through
	// the analyzer graph as usual and the result is written back to
	// L0. Nil disables L0 (engine behaves as before L0 was added).
	L0 *l0.Cache

	// AnalyzerRegistry supplies per-analyzer descriptors used to
	// derive the L0 analyzer-set hash. Nil falls back to
	// analyzers.BundledRegistry, which is the production default.
	// Tests pass a custom registry to exercise the W6 contract.
	AnalyzerRegistry *analyzers.Registry

	// Filter is the post-analysis diagnostic filter applied per
	// package as each cold-path package's []*cache.Diagnostic is
	// converted. When non-nil, runInProcess writes the POST-FILTER
	// diagnostic stream to L0 — warm L0 hits then serve directly
	// without re-running the filter. Nil disables filtering (the
	// engine returns raw diagnostics; the CLI used to apply the
	// filter itself in this mode, and tests that pass no Filter
	// continue to get the raw stream).
	Filter *exclusion.Filter

	// analyzeHook is a test-only seam that wraps Snapshot.Analyze.
	// Production callers leave it nil; tests use it to observe
	// which packages reached the analyzer driver, simulate
	// per-package failures, etc.
	analyzeHook analyzeFn

	// extraSubprocRunners is a test-only seam that appends to the
	// subprocess Runner list resolved from in.Registry. Production
	// callers leave it nil; tests use it to inject stubRunner
	// instances so engine-level subproc behavior (e.g. the
	// post-runSubproc filter pass) can be exercised without
	// wiring a real external binary into the registry.
	extraSubprocRunners []subproc.Runner
}

// analyzeFn is the wrapped Snapshot.Analyze signature; see
// RunInput.analyzeHook.
type analyzeFn func(ctx context.Context, snap *cache.Snapshot, pkgs map[metadata.PackageID]*metadata.Package) ([]*cache.Diagnostic, error)

// SetAnalyzeHookForTest is the white-box accessor letting tests
// install an analyzeHook. The function is exported through this
// helper rather than a public field so production callers don't
// accidentally bypass Snapshot.Analyze.
func SetAnalyzeHookForTest(in *RunInput, fn analyzeFn) {
	in.analyzeHook = fn
}

// SetExtraSubprocRunnersForTest is the white-box accessor letting
// tests inject subproc Runner stubs alongside the registry-resolved
// set. Each Runner is fanned out by runSubproc exactly like a
// registry-derived Runner, including the post-run filter pass.
func SetExtraSubprocRunnersForTest(in *RunInput, runners []subproc.Runner) {
	in.extraSubprocRunners = runners
}

// RunStats reports the high-level numbers about a Run invocation.
// All fields are optional; zero is "not measured."
type RunStats struct {
	// Wall is the total Run wall clock time.
	Wall time.Duration

	// InProcDiagnostics is the number of diagnostics produced by
	// the in-process analyzer path.
	InProcDiagnostics int

	// SubprocDiagnostics is the number of diagnostics produced by
	// the subprocess wrapper path.
	SubprocDiagnostics int

	// AnalyzerCount is the number of distinct in-process analyzers
	// the engine installed for this Run.
	AnalyzerCount int

	// SubprocRunnerCount is the number of subprocess Runner
	// instances the engine constructed for this Run.
	SubprocRunnerCount int

	// WorkspacePackageCount is the number of packages metadata
	// loaded from the workspace.
	WorkspacePackageCount int
}

// RunOutput is the result of engine.Run.
type RunOutput struct {
	// Diagnostics is the merged in-process + subprocess diagnostic
	// list. Not sorted; callers run output.Sort before printing.
	//
	// Pos.Filename is in canonical form (<importPath>/<basename>) so
	// the cache layer below the engine sees a host-portable string.
	// Printers should reverse the encoding via PkgDirs +
	// canonicalpath.NewResolver before rendering.
	Diagnostics []output.Diagnostic

	// Stats reports the high-level numbers.
	Stats RunStats

	// Warnings is the list of human-readable warning strings. The
	// most common case is ShapeRegistryOnly linters that have no
	// AnalyzerFn wired and no CustomLinterSettings payload — we
	// skip them, but record the skip so the caller can surface it.
	Warnings []string

	// PkgDirs maps each loaded package's PkgPath to its on-disk
	// source directory. Callers reverse-map canonical Pos.Filename
	// values back to absolute paths via canonicalpath.NewResolver
	// before printing. Empty when no workspace packages were loaded.
	PkgDirs map[string]string

	// CacheMetrics is the snapshot of the in-process L0/L1/L2 cache
	// counters at end of Run. Empty when the in-process analyzer
	// path was not exercised. Mirrors the [plaid-metrics] trace
	// emitted under PLAID_METRICS_TRACE=1.
	CacheMetrics CacheMetrics
}

// CacheMetrics aggregates the per-layer cache counters that the
// in-process analyzer path maintains. Populated by Run; nil-valued
// L0 means the L0 cache was not attached for this Run.
type CacheMetrics struct {
	L0 *L0CacheMetrics
	L1 cache.L1Metrics
	L2 cache.L2Metrics
}

// L0CacheMetrics extends l0.MetricsSnapshot with the per-Run derived
// counters the engine tracks alongside the Cache's atomic state
// (root-package L0 hits, dep-override short-circuit hits, post-Run
// dep-fact writes).
type L0CacheMetrics struct {
	l0.MetricsSnapshot
	SkippedPkgs  int64 // packages served from L0 (root-level hits)
	DepHits      int64 // dep-override short-circuits installed pre-Analyze
	DepWrites    int64 // dep fact blobs written post-Analyze
	OverrideHits int64 // delta of cache.L0OverrideHits across this Run
}

// Run is the production engine entry point. It:
//
//  1. Installs the registry's enabled analyzers into the gopls
//     settings carrier (settings.AllAnalyzers).
//  2. Constructs a cache.Cache with L1 / L2 attached.
//  3. Opens a Workspace against in.Workspace.ModuleRoot and loads
//     the metadata.
//  4. Invokes Snapshot.Analyze across every workspace package.
//  5. Converts the resulting []*cache.Diagnostic to
//     []output.Diagnostic via output.FromAnalysis.
//  6. Fans out subprocess Runners (unused / unparam / custom
//     plugins) in parallel via errgroup. Each Runner runs against
//     the same Workspace identity.
//  7. Merges in-process + subprocess diagnostics and returns.
//
// Run mutates process-global state — settings.AllAnalyzers is
// overwritten for the duration of the call and restored on exit.
// Run is therefore NOT concurrent-safe; one Run per process at a
// time, matching the bench harness contract.
func Run(ctx context.Context, in RunInput) (*RunOutput, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}

	start := time.Now()
	out := &RunOutput{}

	// Resolve runnable in-process analyzers + subprocess linters
	// from the registry. Surface ShapeRegistryOnly long-tail
	// linters (no payload, no AnalyzerFn) as warnings rather than
	// errors — those are per-linter follow-up PRs.
	plan := planFromRegistry(in.Registry)
	out.Warnings = append(out.Warnings, plan.warnings...)
	out.Stats.AnalyzerCount = len(plan.analyzers)
	out.Stats.SubprocRunnerCount = len(plan.subproc)

	// 1. Install analyzers globally for the duration of the Run.
	//    Restore on exit so the next Run sees its own state.
	if len(plan.analyzers) > 0 {
		prev := settings.AllAnalyzers
		settings.AllAnalyzers = wrapAnalyzers(plan.analyzers)
		defer func() { settings.AllAnalyzers = prev }()
	}

	// 2. Run in-process analyzers (if any). The subproc fan-out
	//    runs in parallel below.
	var inProcDiags []output.Diagnostic
	var ipResult *runInProcessResult
	if len(plan.analyzers) > 0 {
		var err error
		ipResult, err = runInProcess(ctx, in, plan)
		if err != nil {
			return nil, fmt.Errorf("engine.Run: in-process analyzers: %w", err)
		}
	}
	if ipResult != nil {
		inProcDiags = ipResult.Diags
		out.Stats.WorkspacePackageCount = ipResult.PkgCount
		out.PkgDirs = ipResult.PkgDirs
		out.CacheMetrics = ipResult.CacheMetrics
	}
	out.Stats.InProcDiagnostics = len(inProcDiags)

	// 3. Fan out subprocess runners. errgroup cancels siblings on
	//    the first failure.
	subRunners := plan.subproc
	if len(in.extraSubprocRunners) > 0 {
		subRunners = append(append([]subproc.Runner(nil), subRunners...), in.extraSubprocRunners...)
	}
	subDiags, err := runSubproc(ctx, in, subRunners)
	if err != nil {
		return nil, fmt.Errorf("engine.Run: subprocess linters: %w", err)
	}
	out.Stats.SubprocDiagnostics = len(subDiags)

	// 3.5. Apply the exclusion filter to subprocess diagnostics. The
	// in-process path filters per-package inside runInProcess (the
	// streaming filter); subprocess runners scan the full workspace
	// regardless of the user's target patterns, so without this pass
	// a `plaid-lint run ./pkg/foo/...` invocation leaks diagnostics
	// from outside the target through the subproc path. The L0 fast
	// path is unaffected — L0 stores POST-FILTER per-package streams
	// and never holds subproc output.
	if in.Filter != nil && len(subDiags) > 0 {
		subDiags = in.Filter.Apply(subDiags)
	}

	// Canonicalise subproc Pos.Filename using the same URI → PkgPath
	// map the in-process pass built. Subproc runners scan the same
	// workspace, so files they report on are the same files the in-
	// process loader saw. A subproc diagnostic whose file isn't in the
	// loaded set (e.g. a transitive dep the in-proc loader skipped)
	// stays absolute — same fallback as the in-process path.
	if ipResult != nil && len(ipResult.UriPkg) > 0 && len(subDiags) > 0 {
		canonicalizeDiagnostics(subDiags, ipResult.UriPkg)
	}

	// 4. Merge. Caller sorts before printing.
	merged := make([]output.Diagnostic, 0, len(inProcDiags)+len(subDiags))
	merged = append(merged, inProcDiags...)
	merged = append(merged, subDiags...)
	out.Diagnostics = merged

	out.Stats.Wall = time.Since(start)
	return out, nil
}

// validateInput checks the required RunInput invariants. Callers
// see a clear error rather than a downstream NPE.
func validateInput(in RunInput) error {
	if in.Config == nil {
		return errors.New("engine.Run: RunInput.Config is required")
	}
	if in.Registry == nil {
		return errors.New("engine.Run: RunInput.Registry is required")
	}
	if in.Workspace.ModuleRoot == "" {
		return errors.New("engine.Run: RunInput.Workspace.ModuleRoot is required")
	}
	if in.L1 == nil {
		return errors.New("engine.Run: RunInput.L1 is required")
	}
	if in.L2 == nil {
		return errors.New("engine.Run: RunInput.L2 is required")
	}
	return nil
}

// runInProcess opens a Workspace, loads metadata, and drives
// Snapshot.Analyze across every workspace package. When RunInput.L0
// is non-nil, packages whose L0 entry hits return cached diagnostics
// and never reach the analyzer driver — closing the warm-mode gap.
// The returned diagnostics are already in
// output.Diagnostic form.
// runInProcessResult bundles runInProcess's outputs. Adds the per-Run
// URI → PkgPath map (used to canonicalise subproc diagnostics with
// the same mapping the in-process path used) and the PkgPath → on-
// disk-directory map (returned to callers for reverse-mapping at
// render time).
type runInProcessResult struct {
	Diags        []output.Diagnostic
	PkgCount     int
	UriPkg       map[protocol.DocumentURI]string
	PkgDirs      map[string]string
	CacheMetrics CacheMetrics
}

func runInProcess(ctx context.Context, in RunInput, plan *runPlan) (*runInProcessResult, error) {
	// cache.L0OverrideHits is a process-global cumulative counter;
	// snapshot at entry so the per-Run delta is reported to callers
	// even when other Runs (or test fixtures) bumped it earlier.
	overrideHitsStart := cache.L0OverrideHits()

	c := cache.New(nil)
	c.AttachL1(in.L1, resolvedCacheToolVersion(in))
	c.AttachL2(in.L2, resolvedBuildEnv(in), resolvedGoVersion(in), resolvedCacheToolVersion(in))

	opts := settings.DefaultOptions()
	// Thread `run.tests` into the loader. Nil (= unset) keeps the
	// default of true, matching golangci-lint v2's behavior; an
	// explicit &false in the config opts the workspace loader out of
	// loading `_test.go` files.
	opts.Tests = in.Config.Run.AnalyzeTests
	ws := workspace.NewWithCacheAndOptions(in.Workspace.ModuleRoot, c, opts)
	defer ws.Close()

	snap := ws.Snapshot()
	if snap == nil {
		return nil, errors.New("workspace returned nil snapshot")
	}
	defer func() { _ = snap.Release() }()

	inner := snap.Inner()
	if err := inner.InitializeWorkspaceWithPatterns(ctx, in.TargetPatterns); err != nil {
		return nil, fmt.Errorf("initialize workspace: %w", err)
	}

	wsPkgs := inner.WorkspacePackages()
	pkgs := map[metadata.PackageID]*metadata.Package{}
	for id := range wsPkgs.All() {
		mp := inner.Metadata(id)
		if mp == nil {
			continue
		}
		pkgs[mp.ID] = mp
	}
	if len(pkgs) == 0 {
		return &runInProcessResult{}, nil
	}

	// Build the URI → PkgPath map once. It is reused both for the
	// post-filter canonicalisation pass below and (via the caller)
	// for canonicalising subproc diagnostics that scan the same
	// workspace.
	uriPkg := uriPkgPathMap(pkgs)
	pkgDirs := pkgDirsFor(pkgs)

	// L0 fast path. For each workspace package, compute the L0 key;
	// hit → reconstitute output.Diagnostic from the cached blob and
	// drop the package from the Analyze input set. Miss → keep it in
	// pkgsToAnalyze. Cache writes happen post-Analyze (only for
	// packages whose analysis succeeded).
	pkgsToAnalyze, l0Hits, l0Keys, cachedDiags, lc := splitByL0(ctx, in, plan, inner, pkgs)

	// Dep-override fast path. When the L0 cache holds entries
	// for the dep closure of pkgsToAnalyze, install synthetic
	// analyzeSummary overrides so analysisNode.runCached short-
	// circuits each dep's action graph. Also install the post-
	// analysis callback that captures fact blobs the engine writes
	// back to L0 — on cold first runs (no overrides to install) we
	// still install the callback so the next run has data to serve.
	var depKeys map[metadata.PackageID]clcache.ActionID
	var depHits int
	var capture *depOverrideCapture
	if depOverrideEnabled() && in.L0 != nil && lc != nil && len(pkgsToAnalyze) > 0 {
		depKeys, depHits = installDepOverrides(ctx, in, plan, inner, pkgsToAnalyze, lc)
		capture = newDepOverrideCapture()
		inner.SetNodeAnalyzedCallback(capture.callback())
		defer inner.SetNodeAnalyzedCallback(nil)
	}

	var freshDiags []*cache.Diagnostic
	if len(pkgsToAnalyze) > 0 {
		var err error
		freshDiags, err = invokeAnalyze(ctx, in, inner, pkgsToAnalyze)
		if err != nil {
			return nil, fmt.Errorf("analyze: %w", err)
		}
	}

	// Stream-filter the fresh (cold-path) diagnostics per package and
	// cache the POST-FILTER stream to L0. Warm L0 hits served above
	// bypass this path entirely — they are already post-filter on disk
	// (L0 is a per-package, post-exclusion cache).
	//
	// The streaming filter applies every stage (target-dirs, nolint,
	// staticcheck-default-disabled, library-version-skew, paths,
	// paths-except, generated-file detection, rules, uniq-by-line)
	// per package as we partition, so we never build the
	// "all-diagnostics-in-memory" peak that earlier profiling flagged.
	freshConverted := partitionAndFilter(in.Filter, pkgs, pkgsToAnalyze, freshDiags)

	// Canonicalise Pos.Filename on the post-filter stream so L0 stores
	// (and the engine returns) the host-portable <importPath>/<basename>
	// form. The exclusion filter inside partitionAndFilter has already
	// consumed the absolute path (generated-file detection, nolint
	// ranges, paths regex). After this point Pos.Filename is canonical
	// for every workspace-owned file; cgo / synthetic / vendored-dep
	// paths whose URI didn't match any loaded package fall through
	// unchanged.
	for id := range freshConverted {
		canonicalizeDiagnostics(freshConverted[id], uriPkg)
	}

	if in.L0 != nil {
		writeL0Entries(in.L0, l0Keys, freshConverted, pkgsToAnalyze, capture)
	}

	// Dep writes: persist captured per-dep fact blobs so warm
	// runs can serve them via the override fast path. Roots are
	// excluded — they're handled by writeL0Entries above. Skipped
	// silently when the capture wasn't installed (override path
	// disabled or L0 unavailable).
	var depWrites int
	if in.L0 != nil && capture != nil {
		rootIDs := make(map[metadata.PackageID]struct{}, len(pkgsToAnalyze))
		for id := range pkgsToAnalyze {
			rootIDs[id] = struct{}{}
		}
		// Build a dep key map if installDepOverrides wasn't called or
		// didn't pre-populate depKeys (e.g. cold first run: no L0
		// hits, but we still want to populate L0 for the next run).
		if depKeys == nil && lc != nil {
			depKeys = computeAllDepKeys(ctx, lc, pkgsToAnalyze)
		}
		depWrites = writeDepL0Entries(in.L0, depKeys, capture, rootIDs)
	}

	// Snapshot per-layer metrics. The trace path below re-uses the
	// same snapshot the caller receives via CacheMetrics, so the
	// optional PLAID_METRICS_TRACE output and the --metrics-json
	// file are consistent.
	cm := CacheMetrics{
		L1: c.L1Metrics(),
		L2: c.L2Metrics(),
	}
	if in.L0 != nil {
		l0m := in.L0.MetricsPtr().Snapshot()
		cm.L0 = &L0CacheMetrics{
			MetricsSnapshot: l0m,
			SkippedPkgs:     int64(l0Hits),
			DepHits:         int64(depHits),
			DepWrites:       int64(depWrites),
			OverrideHits:    cache.L0OverrideHits() - overrideHitsStart,
		}
	}
	if os.Getenv("PLAID_METRICS_TRACE") == "1" {
		fmt.Fprintf(os.Stderr, "[plaid-metrics] L1 hits=%d misses=%d stores=%d skipped=%d errors=%d\n",
			cm.L1.Hits, cm.L1.Misses, cm.L1.Stores, cm.L1.Skipped, cm.L1.Errors)
		fmt.Fprintf(os.Stderr, "[plaid-metrics] L2 hits=%d misses=%d stores=%d skipped=%d errors=%d\n",
			cm.L2.Hits, cm.L2.Misses, cm.L2.Stores, cm.L2.Skipped, cm.L2.Errors)
		if cm.L0 != nil {
			fmt.Fprintf(os.Stderr, "[plaid-metrics] L0 hits=%d misses=%d stores=%d errors=%d skipped_pkgs=%d dep_hits=%d dep_writes=%d override_hits=%d\n",
				cm.L0.Hits, cm.L0.Misses, cm.L0.Stores, cm.L0.Errors,
				cm.L0.SkippedPkgs, cm.L0.DepHits, cm.L0.DepWrites, cm.L0.OverrideHits)
		}
	}

	// Merge cached + fresh diagnostics. Both are already post-filter:
	// cached came from a prior cold run that filtered before writing
	// L0, and freshConverted just went through partitionAndFilter.
	freshTotal := 0
	for _, ds := range freshConverted {
		freshTotal += len(ds)
	}
	merged := make([]output.Diagnostic, 0, len(cachedDiags)+freshTotal)
	merged = append(merged, cachedDiags...)
	for _, ds := range freshConverted {
		merged = append(merged, ds...)
	}
	return &runInProcessResult{
		Diags:        merged,
		PkgCount:     len(pkgs),
		UriPkg:       uriPkg,
		PkgDirs:      pkgDirs,
		CacheMetrics: cm,
	}, nil
}

// splitByL0 partitions pkgs into the set that should still be sent to
// the analyzer driver (misses) and the set whose diagnostics were
// served from L0. Returns the analyze set, the hit count (for the
// metrics trace), the per-package L0 keys (used for post-Analyze
// writes), the cached diagnostic stream, and the l0Context (reused by
// the dep-override path when non-nil).
func splitByL0(
	ctx context.Context,
	in RunInput,
	plan *runPlan,
	inner *cache.Snapshot,
	pkgs map[metadata.PackageID]*metadata.Package,
) (map[metadata.PackageID]*metadata.Package, int, map[metadata.PackageID]clcache.ActionID, []output.Diagnostic, *l0Context) {
	pkgsToAnalyze := make(map[metadata.PackageID]*metadata.Package, len(pkgs))
	keys := make(map[metadata.PackageID]clcache.ActionID, len(pkgs))
	var cached []output.Diagnostic
	hits := 0

	if in.L0 == nil {
		// L0 disabled: every package goes through Analyze.
		for id, mp := range pkgs {
			pkgsToAnalyze[id] = mp
		}
		return pkgsToAnalyze, 0, keys, nil, nil
	}

	reg := in.AnalyzerRegistry
	if reg == nil {
		reg = analyzers.BundledRegistry
	}
	lc, err := newL0Context(ctx, inner, plan,
		reg,
		in.Filter.ConfigDigest(),
		resolvedCacheToolVersion(in),
		resolvedBuildEnv(in),
		resolvedGoVersion(in),
	)
	if err != nil || lc == nil {
		// Couldn't derive L0 context — treat every package as a miss.
		for id, mp := range pkgs {
			pkgsToAnalyze[id] = mp
		}
		return pkgsToAnalyze, 0, keys, nil, nil
	}

	for id, mp := range pkgs {
		key, ok := lc.keyFor(ctx, mp)
		if !ok {
			pkgsToAnalyze[id] = mp
			continue
		}
		keys[id] = key
		entry, err := in.L0.Get(key)
		if err != nil || entry == nil {
			pkgsToAnalyze[id] = mp
			continue
		}
		hits++
		cached = append(cached, entry.Diagnostics...)
	}
	return pkgsToAnalyze, hits, keys, cached, lc
}

// invokeAnalyze routes the Analyze call through the optional
// analyzeHook (for tests) or directly to inner.Analyze.
func invokeAnalyze(
	ctx context.Context,
	in RunInput,
	inner *cache.Snapshot,
	pkgs map[metadata.PackageID]*metadata.Package,
) ([]*cache.Diagnostic, error) {
	if in.analyzeHook != nil {
		return in.analyzeHook(ctx, inner, pkgs)
	}
	return inner.Analyze(ctx, pkgs, nil)
}

// writeL0Entries writes one L0 entry per analyzed package. byPkg
// already holds the POST-FILTER diagnostic stream per PackageID
// (produced by partitionAndFilter, which routes diagnostics through
// the streaming Filter before they reach this function). Packages
// that produced zero diagnostics still get an empty entry so warm
// runs hit cleanly on next invocation.
//
// When capture is non-nil, the per-analyzer fact blobs from each
// analyzed package's analysisNode are folded into the L0 entry so
// subsequent runs can serve the package via the dep-override
// fast path. capture==nil falls back to the earlier schema (no
// Actions map).
func writeL0Entries(
	cacheL0 *l0.Cache,
	keys map[metadata.PackageID]clcache.ActionID,
	byPkg map[metadata.PackageID][]output.Diagnostic,
	pkgsAnalyzed map[metadata.PackageID]*metadata.Package,
	capture *depOverrideCapture,
) {
	if len(pkgsAnalyzed) == 0 {
		return
	}
	for id := range pkgsAnalyzed {
		key, ok := keys[id]
		if !ok {
			continue
		}
		diags := byPkg[id]
		if diags == nil {
			diags = []output.Diagnostic{}
		}
		entry := &l0.Entry{
			PackageID:   string(id),
			Diagnostics: diags,
		}
		if capture != nil {
			if nd, ok := capture.rootCapturedNodeFor(id); ok {
				entry.Compiles = nd.Compiles
				entry.Actions = make(map[string]l0.ActionFacts, len(nd.Actions))
				for a, data := range nd.Actions {
					entry.Actions[cache.StableNameForAnalyzer(a)] = l0.ActionFacts{
						Facts:     data.Facts,
						FactsHash: data.FactsHash,
						Err:       data.Err,
					}
				}
			}
		}
		_ = cacheL0.Put(key, entry)
	}
}

// computeAllDepKeys derives the L0 key for every dep in the closure of
// roots. Used when the override-install path didn't pre-compute keys
// (e.g. cold first run with the override enabled: no entries to look
// up, but we still want to populate L0 for next time).
func computeAllDepKeys(
	ctx context.Context,
	lc *l0Context,
	roots map[metadata.PackageID]*metadata.Package,
) map[metadata.PackageID]clcache.ActionID {
	deps := collectDepClosure(lc, roots)
	out := make(map[metadata.PackageID]clcache.ActionID, len(deps))
	for id, mp := range deps {
		k, ok := lc.keyFor(ctx, mp)
		if ok {
			out[id] = k
		}
	}
	return out
}

// partitionAndFilter converts the flat []*cache.Diagnostic returned by
// the analyzer driver into a per-PackageID post-filter map. Each
// analyzed package's diagnostics are streamed through filter.Stream so
// the cold path never accumulates the entire workspace's diagnostic
// set in one buffer (avoiding an RSS regression). Diagnostics that don't map
// to a workspace package are routed through Stream.AddBatch so they
// still get filtered before reaching the caller.
//
// The returned map keys are exactly pkgsAnalyzed. A package that
// produced zero diagnostics maps to an empty slice — that's the
// "warm-hits-on-zero-diagnostic-packages" invariant. The "ownerless"
// bucket is stored under the empty PackageID key; the caller flattens
// it into the output stream but does NOT write it to L0 (L0 is
// per-workspace-package only).
//
// When filter is nil the partition still runs (so the L0 layout is
// the same) but no diagnostic is dropped — equivalent to the
// pre-streaming behavior.
func partitionAndFilter(
	filter *exclusion.Filter,
	allPkgs map[metadata.PackageID]*metadata.Package,
	pkgsAnalyzed map[metadata.PackageID]*metadata.Package,
	fresh []*cache.Diagnostic,
) map[metadata.PackageID][]output.Diagnostic {
	byPkg := make(map[metadata.PackageID][]output.Diagnostic, len(pkgsAnalyzed)+1)
	for id := range pkgsAnalyzed {
		byPkg[id] = []output.Diagnostic{}
	}
	if len(fresh) == 0 {
		return byPkg
	}

	// URI → PackageID over the FULL pkg set so a diagnostic whose URI
	// belongs to a satisfied (L0-hit) package isn't lost. The owner
	// may still be filtered out of pkgsAnalyzed below if it wasn't
	// analyzed this run — those diagnostics are then attributed to
	// the empty-string "ownerless" bucket so they still surface but
	// aren't double-counted in L0.
	uriOwner := uriPkgMap(allPkgs)

	// Group converted diagnostics by owner (no filter yet).
	raw := make(map[metadata.PackageID][]output.Diagnostic, len(pkgsAnalyzed)+1)
	for _, d := range fresh {
		conv := output.FromAnalysis(d)
		owner, ok := uriOwner[uriFromFilename(conv.Pos.Filename)]
		if !ok {
			raw[""] = append(raw[""], conv)
			continue
		}
		if _, eligible := pkgsAnalyzed[owner]; !eligible {
			// The diagnostic's URI maps to a workspace package, but
			// that package wasn't analyzed this run (it must have hit
			// L0 — in which case its cached diagnostics already cover
			// this URI). Route to the ownerless bucket so the caller
			// still sees the diagnostic without double-caching.
			raw[""] = append(raw[""], conv)
			continue
		}
		raw[owner] = append(raw[owner], conv)
	}

	// Stream-filter each package's diagnostics, then the ownerless
	// batch. nil filter is the no-op pass-through.
	stream := filter.NewStream()
	defer stream.Finish()
	for id := range pkgsAnalyzed {
		kept := stream.AddPackage(string(id), raw[id])
		// Preserve the "empty slice means zero diagnostics" invariant
		// for L0 writes.
		if kept == nil {
			kept = []output.Diagnostic{}
		}
		byPkg[id] = kept
	}
	if owls, ok := raw[""]; ok && len(owls) > 0 {
		byPkg[""] = stream.AddBatch(owls)
	}
	return byPkg
}

// runSubproc fans out one goroutine per Runner via errgroup, merges
// results behind a mutex, and returns once all goroutines finish.
// Any single failure cancels siblings via gctx.
func runSubproc(ctx context.Context, in RunInput, runners []subproc.Runner) ([]output.Diagnostic, error) {
	if len(runners) == 0 {
		return nil, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	// Cap concurrency at the number of runners — typically tiny (2
	// or 3) — so we don't over-spawn for a long-tail-only registry.
	if n := runtime.GOMAXPROCS(0); n > 0 && n < len(runners) {
		g.SetLimit(n)
	}

	var mu sync.Mutex
	merged := make([]output.Diagnostic, 0)

	for _, r := range runners {
		r := r // capture
		g.Go(func() error {
			diags, err := r.Run(gctx, in.Config, in.Workspace)
			if err != nil {
				return fmt.Errorf("%s: %w", r.Name(), err)
			}
			if len(diags) > 0 {
				mu.Lock()
				merged = append(merged, diags...)
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return merged, nil
}

// wrapAnalyzers turns the deduped []*analysis.Analyzer plan into the
// settings.Analyzer carrier the gopls cache reads from
// settings.AllAnalyzers.
func wrapAnalyzers(plan []analyzerEntry) []*settings.Analyzer {
	// Deduplicate by analyzer pointer — registry.Resolved fan-out
	// (e.g. staticcheck) can emit one row per analyzer; the same
	// pointer may appear twice if two linter names alias the same
	// SA-* table. settings.AllAnalyzers must contain unique pointers.
	seen := make(map[uintptr]bool, len(plan))
	out := make([]*settings.Analyzer, 0, len(plan))
	for _, e := range plan {
		if e.analyzer == nil {
			continue
		}
		key := analyzerKey(e.analyzer)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, settings.NewAnalyzer(e.analyzer))
	}
	return out
}

// resolvedCacheToolVersion returns the configured tool version or
// the package default. Stable across runs.
func resolvedCacheToolVersion(in RunInput) string {
	if in.CacheToolVersion != "" {
		return in.CacheToolVersion
	}
	return defaultCacheToolVersion
}

// resolvedBuildEnv returns a stable build-env string for L2 keying.
func resolvedBuildEnv(in RunInput) string {
	if in.L2BuildEnv != "" {
		return in.L2BuildEnv
	}
	return runtime.GOOS + "/" + runtime.GOARCH + "/cgo0"
}

// resolvedGoVersion returns the L2 go-version key.
func resolvedGoVersion(in RunInput) string {
	if in.L2GoVersion != "" {
		return in.L2GoVersion
	}
	return runtime.Version()
}

// defaultCacheToolVersion is bumped whenever an analyzer set or
// engine behavior change requires invalidating cached entries.
const defaultCacheToolVersion = "plaid-lint-engine-v1"
