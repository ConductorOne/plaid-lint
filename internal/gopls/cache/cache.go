// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/imports"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol/command"
	"github.com/conductorone/plaid-lint/internal/gopls/util/memoize"
	"github.com/conductorone/plaid-lint/internal/l3"
)

// ballast is a 100MB unused byte slice that exists only to reduce garbage
// collector CPU in small workspaces and at startup.
//
// The redesign of gopls described at https://go.dev/blog/gopls-scalability
// moved gopls to a model where it has a significantly smaller heap, yet still
// allocates many short-lived data structures during parsing and type checking.
// As a result, for some workspaces, particularly when opening a low-level
// package, the steady-state heap may be a small fraction of total allocation
// while rechecking the workspace, paradoxically causing the GC to consume much
// more CPU. For example, in one benchmark that analyzes the starlark
// repository, the steady-state heap was ~10MB, and the process of diagnosing
// the workspace allocated 100-200MB.
//
// The reason for this paradoxical behavior is that GC pacing
// (https://tip.golang.org/doc/gc-guide#GOGC) causes the collector to trigger
// at some multiple of the steady-state heap size, so a small steady-state heap
// causes GC to trigger sooner and more often when allocating the ephemeral
// structures.
//
// Allocating a 100MB ballast avoids this problem by ensuring a minimum heap
// size. The value of 100MB was chosen to be proportional to the in-memory
// cache in front the filecache package, and the throughput of type checking.
// Gopls already requires hundreds of megabytes of RAM to function.
//
// Note that while other use cases for a ballast were made obsolete by
// GOMEMLIMIT, ours is not. GOMEMLIMIT helps in cases where you have a
// containerized service and want to optimize its latency and throughput by
// taking advantage of available memory. However, in our case gopls is running
// on the developer's machine alongside other applications, and can have a wide
// range of memory footprints depending on the size of the user's workspace.
// Setting GOMEMLIMIT to too low a number would make gopls perform poorly on
// large repositories, and setting it to too high a number would make gopls a
// badly behaved tenant. Short of calibrating GOMEMLIMIT based on the user's
// workspace (an intractible problem), there is no way for gopls to use
// GOMEMLIMIT to solve its GC CPU problem.
//
// Because this allocation is large and occurs early, there is a good chance
// that rather than being recycled, it comes directly from the OS already
// zeroed, and since it is never accessed, the memory region may avoid being
// backed by pages of RAM. But see
// https://groups.google.com/g/golang-nuts/c/66d0cItfkjY/m/3NvgzL_sAgAJ
//
// For more details on this technique, see:
// https://blog.twitch.tv/en/2019/04/10/go-memory-ballast-how-i-learnt-to-stop-worrying-and-love-the-heap/
var ballast = make([]byte, 100*1e6)

// New Creates a new cache for gopls operation results, using the given file
// set, shared store, and session options.
//
// Both the fset and store may be nil, but if store is non-nil so must be fset
// (and they must always be used together), otherwise it may be possible to get
// cached data referencing token.Pos values not mapped by the FileSet.
func New(store *memoize.Store) *Cache {
	index := atomic.AddInt64(&cacheIndex, 1)

	if store == nil {
		store = &memoize.Store{}
	}

	c := &Cache{
		id:         strconv.FormatInt(index, 10),
		store:      store,
		memoizedFS: newMemoizedFS(),
		modCache: &sharedModCache{
			caches: make(map[string]*imports.DirInfoCache),
			timers: make(map[string]*refreshTimer),
		},
	}
	return c
}

// A Cache holds content that is shared across multiple gopls sessions.
type Cache struct {
	id string

	// store holds cached calculations.
	//
	// TODO(rfindley): at this point, these are not important, as we've moved our
	// content-addressable cache to the file system (the filecache package). It
	// is unlikely that this shared cache provides any shared value. We should
	// consider removing it, replacing current uses with a simpler futures cache,
	// as we've done for e.g. type-checked packages.
	store *memoize.Store

	// memoizedFS holds a shared file.Source that caches reads.
	//
	// Reads are invalidated when *any* session gets a didChangeWatchedFile
	// notification. This is fine: it is the responsibility of memoizedFS to hold
	// our best knowledge of the current file system state.
	*memoizedFS

	// modCache holds the shared goimports state for GOMODCACHE directories
	modCache *sharedModCache

	// l2 is the optional plaid-lint content-addressed L2 cache used to
	// store and look up dep-side gcexportdata blobs. Nil disables the L2
	// fast path; getImportPackage then falls through to the gopls
	// filecache + on-demand type-check path. See W5.
	l2          *clcache.Cache
	l2BuildEnv  string
	l2GoVersion string
	l2ToolVer   string
	// l2Metrics records hit / miss counts for diagnostics and tests; never
	// affects behaviour.
	l2Metrics l2Metrics

	// l1 is the optional plaid-lint content-addressed L1 cache used to
	// store and look up per-(package, analyzer) diagnostics+facts. Nil
	// disables the L1 fast path; action.exec then runs the analyzer
	// unconditionally. See W6.
	l1        *clcache.Cache
	l1ToolVer string
	l1Metrics l1Metrics
	// l1Registry is consulted by the action-exec L1 fast path to look
	// up per-analyzer descriptors (ConfigSalt, AnalyzerVersion,
	// ResultCodec, ConsumedAsResult fallback). Nil means "use
	// analyzers.BundledRegistry". Tests may attach their own registry
	// via AttachL1WithRegistry.
	l1Registry *analyzers.Registry

	// irManager coordinates L3 IR pin/release lifecycle across the
	// action graph. When non-nil, action.exec calls Pin(pkgID) for
	// NeedsIR=true descriptors that miss L1 and pair it with a defer
	// Release. Nil treats the field as a NoopIRManager (no
	// bookkeeping, no overhead). The W9 scheduler will attach a real
	// IRManager via AttachIRManager.
	irManager l3.IRManager

	// scheduler is the optional W9 coordinator: action.exec
	// consults scheduler.Acquire before running each analyzer's
	// Run body and defers the returned release function. Nil
	// preserves the W7/W8 byte-equivalent path (no extra gating
	// beyond the existing runtime.GOMAXPROCS limiter). The W9
	// production attachment is a *scheduler.RSSBudgetScheduler,
	// which typically also implements l3.IRManager — see
	// AttachScheduler for the dual install.
	scheduler ActionScheduler

	// maxInFlightPackages is the Phase 1.7 sub-path-c clustering cap on
	// distinct in-flight packages at the outer analysis limiter. Zero
	// (the default) disables clustering: the limiter behaves as the
	// prior channel-semaphore (cap = GOMAXPROCS, no per-package
	// affinity). Set via SetMaxInFlightPackages before View creation.
	maxInFlightPackages int

	// viewCount tracks how many Views have been created against this
	// Cache. Used to enforce AttachL2's setup-time contract: AttachL2 must
	// be called before any View is constructed.
	viewCount atomic.Int64

	// analysisGate is lazily constructed on first Snapshot.Analyze
	// call from this Cache's Views. The gate is shared across
	// concurrent Analyze invocations from the same Cache so the
	// clustering bias is consistent across them. nil until first use;
	// access via getOrCreateAnalysisGate.
	gateMu       sync.Mutex
	analysisGate *analysisGate
}

// L2Metrics is a snapshot of the L2 hit/miss/store counters.
type L2Metrics struct {
	Hits   int64
	Misses int64
	Stores int64
	Errors int64
	// Skipped counts L2 stores that were elided because the target
	// entry already existed on disk. Surfaced separately from Stores so
	// the warm-mode "re-write the same blob" cost is
	// observable without re-running the profile.
	Skipped int64
}

// l2Metrics is the atomic counter pair used internally; L2Metrics is the
// exported snapshot type.
type l2Metrics struct {
	hits    atomic.Int64
	misses  atomic.Int64
	stores  atomic.Int64
	errors  atomic.Int64
	skipped atomic.Int64
}

func (m *l2Metrics) snapshot() L2Metrics {
	return L2Metrics{
		Hits:    m.hits.Load(),
		Misses:  m.misses.Load(),
		Stores:  m.stores.Load(),
		Errors:  m.errors.Load(),
		Skipped: m.skipped.Load(),
	}
}

// AttachL2 installs the optional content-addressed L2 cache on c. After this
// call, type-checking lookups for dep packages will first consult l2; on a
// hit, the gcexportdata blob is decoded into the snapshot's master FileSet.
// On a miss, the existing gopls filecache + checkPackageForImport path is
// used unchanged.
//
// buildEnv, goVersion and toolVer are folded into the L2 action ID via
// L2Entry.BuildEnv / GoVersion / ToolVersion so that cache entries from a
// different toolchain or platform never collide. Pass stable strings
// (e.g. "linux/amd64/cgo0", "go1.26", "plaid-lint-0.1").
//
// AttachL2 is intentionally a post-construction setter rather than a New
// parameter so the gopls fork retains a parameter-free New(memoize.Store)
// signature (its upstream surface). The W5 task wires this only from the
// workspace package; downstream code that constructs a bare Cache works
// unchanged.
//
// AttachL2 must be called once, before any View or Snapshot is created
// against this Cache. Calling AttachL2 after Views exist is a programmer
// error; runtime replacement of the L2 store is not supported and the
// behavior is undefined (cached snapshots may continue to reference the
// previous store). The function panics when called after the first
// NewView to surface the mistake immediately.
func (c *Cache) AttachL2(l2 *clcache.Cache, buildEnv, goVersion, toolVer string) {
	if c.viewCount.Load() > 0 {
		panic("cache.AttachL2 called after View creation")
	}
	c.l2 = l2
	c.l2BuildEnv = buildEnv
	c.l2GoVersion = goVersion
	c.l2ToolVer = toolVer
}

// L2Metrics returns a snapshot of the L2 hit/miss/store counters.
func (c *Cache) L2Metrics() L2Metrics {
	return c.l2Metrics.snapshot()
}

// L1Metrics is a snapshot of the L1 hit/miss/store counters.
type L1Metrics struct {
	Hits   int64
	Misses int64
	Stores int64
	Errors int64
	// EncodeFailures counts L1 stores skipped because the analyzer's
	// ResultCodec.Encode returned an error. Surfaced separately from
	// Errors so the operationally-unusual case (a Result that won't
	// round-trip) is visible without conflating with read-side decode
	// or fact-validation errors.
	EncodeFailures int64
	// Skipped counts L1 stores that were elided because the target entry
	// already existed on disk. Mirrors L2Metrics.Skipped: on the
	// leaf-edit cascade scenario the gopls analysis driver re-runs many
	// dep actions whose L1 entries are already present, and re-encoding
	// + re-writing the same blob is pure waste.
	Skipped int64
	// Per-scope hit / miss counters (Phase A). Provide
	// visibility into how often SyntaxOnly entries hit on cascade-
	// affected packages — the headline lever for the L1 hit-rate
	// improvement. SyntaxOnlyHits + SyntaxOnlyMisses <= Hits +
	// Misses; the difference is the FullTypeGraph / unregistered
	// path.
	SyntaxOnlyHits      int64
	SyntaxOnlyMisses    int64
	FullTypeGraphHits   int64
	FullTypeGraphMisses int64
}

// l1Metrics mirrors l2Metrics: atomic counters internally, snapshot
// exposed via L1Metrics.
type l1Metrics struct {
	hits           atomic.Int64
	misses         atomic.Int64
	stores         atomic.Int64
	errors         atomic.Int64
	encodeFailures atomic.Int64
	skipped        atomic.Int64
	// Per-scope hit / miss counters (Phase A).
	syntaxOnlyHits      atomic.Int64
	syntaxOnlyMisses    atomic.Int64
	fullTypeGraphHits   atomic.Int64
	fullTypeGraphMisses atomic.Int64
}

func (m *l1Metrics) snapshot() L1Metrics {
	return L1Metrics{
		Hits:                m.hits.Load(),
		Misses:              m.misses.Load(),
		Stores:              m.stores.Load(),
		Errors:              m.errors.Load(),
		EncodeFailures:      m.encodeFailures.Load(),
		Skipped:             m.skipped.Load(),
		SyntaxOnlyHits:      m.syntaxOnlyHits.Load(),
		SyntaxOnlyMisses:    m.syntaxOnlyMisses.Load(),
		FullTypeGraphHits:   m.fullTypeGraphHits.Load(),
		FullTypeGraphMisses: m.fullTypeGraphMisses.Load(),
	}
}

// AttachL1 installs the optional content-addressed L1 cache on c. After
// this call, each (analyzer, package) action will first consult l1; on a
// hit, the cached diagnostics + facts are restored without running the
// analyzer's body. On a miss, the analyzer runs and its result is
// written back to l1.
//
// toolVer is folded into the L1 action ID via L1Entry.ToolVersion so
// cache entries from a different plaid-lint binary never collide. Pass
// a stable string (e.g. "plaid-lint-0.1+go1.26").
//
// Like AttachL2, AttachL1 is a setup-time-only contract: it must be
// called before any View or Snapshot is created against this Cache.
// Calling AttachL1 after Views exist panics.
func (c *Cache) AttachL1(l1 *clcache.Cache, toolVer string) {
	c.AttachL1WithRegistry(l1, toolVer, nil)
}

// AttachL1WithRegistry is like AttachL1 but also installs a custom
// analyzer descriptor registry. Pass nil for registry to use
// analyzers.BundledRegistry (the production default). Tests use this
// hook to register synthetic analyzers, or to assert behaviour with a
// pruned descriptor set.
func (c *Cache) AttachL1WithRegistry(l1 *clcache.Cache, toolVer string, registry *analyzers.Registry) {
	if c.viewCount.Load() > 0 {
		panic("cache.AttachL1 called after View creation")
	}
	c.l1 = l1
	c.l1ToolVer = toolVer
	c.l1Registry = registry
}

// L1Metrics returns a snapshot of the L1 hit/miss/store counters.
func (c *Cache) L1Metrics() L1Metrics {
	return c.l1Metrics.snapshot()
}

// AttachIRManager installs an L3 IRManager on c. After this call, the
// per-(analyzer, package) action graph in analysis.go calls
// IRManager.Pin / Pin.Release around the Run body of every analyzer
// whose AnalyzerDescriptor declares NeedsIR=true. The default
// behavior (nil IRManager) is equivalent to attaching a
// [l3.NoopIRManager]: pin/release calls are skipped entirely.
//
// AttachIRManager is the W8 hook for the W9-W10 scheduler. The
// default W8 production attachment is a [l3.SequentialIRManager],
// which counts pins for observability but does no scheduling — the
// action-DAG behaviour stays byte-equivalent to the W7 baseline.
//
// Like AttachL1 / AttachL2, AttachIRManager is a setup-time-only
// contract: it must be called before any View or Snapshot is created
// against this Cache. Calling AttachIRManager after Views exist
// panics.
func (c *Cache) AttachIRManager(m l3.IRManager) {
	if c.viewCount.Load() > 0 {
		panic("cache.AttachIRManager called after View creation")
	}
	c.irManager = m
}

// IRManager returns the IRManager attached to c, or nil when none
// has been attached. Callers that need a guaranteed-non-nil manager
// should range through analysis.go's resolveIRManager helper instead.
func (c *Cache) IRManager() l3.IRManager {
	return c.irManager
}

// AttachScheduler installs an ActionScheduler on c. After this
// call, action.exec consults s.Acquire before invoking each
// analyzer's Run body and defers the returned release function
// once Run returns. If s also implements l3.IRManager, it is
// installed as the cache's IRManager too — the W9
// RSSBudgetScheduler does this, so a single attach call wires
// both the concurrency-cap gate and the pin/release stream.
//
// AttachScheduler is the opt-in entry point for the W9 RSS-aware
// coordinator. The default (no scheduler attached) leaves
// behavior byte-equivalent to the W7/W8 baseline.
//
// Like AttachL1 / AttachL2 / AttachIRManager, AttachScheduler is
// a setup-time-only contract: it must be called before any View
// or Snapshot is created against this Cache. Calling it after
// Views exist panics.
func (c *Cache) AttachScheduler(s ActionScheduler) {
	if c.viewCount.Load() > 0 {
		panic("cache.AttachScheduler called after View creation")
	}
	c.scheduler = s
	if mgr, ok := s.(l3.IRManager); ok {
		c.irManager = mgr
	}
}

// Scheduler returns the ActionScheduler attached to c, or nil
// when none has been attached. External callers (the W10
// benchmark harness, the progress reporter) consume this through
// the *scheduler.Scheduler type assertion since the cache
// interface only exposes Acquire.
func (c *Cache) Scheduler() ActionScheduler {
	return c.scheduler
}

// SetMaxInFlightPackages configures the Phase 1.7 sub-path-c
// clustering cap on distinct in-flight packages at the outer
// analysis limiter. n == 0 (the default) disables clustering: the
// limiter behaves identically to the prior channel-semaphore
// (cap = GOMAXPROCS(0), no per-package affinity). n > 0 enables
// clustering: the limiter still caps worker count at GOMAXPROCS(0)
// but additionally caps distinct in-flight packages at n.
//
// Like the other Attach* setters this is setup-time-only and must
// be called before any View or Snapshot is created against this
// Cache. Calling after View creation panics.
func (c *Cache) SetMaxInFlightPackages(n int) {
	if c.viewCount.Load() > 0 {
		panic("cache.SetMaxInFlightPackages called after View creation")
	}
	if n < 0 {
		panic("cache.SetMaxInFlightPackages: n must be >= 0")
	}
	c.maxInFlightPackages = n
}

// MaxInFlightPackages returns the configured clustering cap. Zero
// means clustering is disabled.
func (c *Cache) MaxInFlightPackages() int {
	return c.maxInFlightPackages
}

// AnalysisGateSnapshot returns the gate's cumulative stats since
// the gate was first constructed (lazily, on first Analyze call).
// Returns the zero AnalysisGateStats when no gate has been
// instantiated yet. The bench harness exposes these in its
// trace output.
func (c *Cache) AnalysisGateSnapshot() AnalysisGateStats {
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	if c.analysisGate == nil {
		return AnalysisGateStats{}
	}
	return c.analysisGate.Snapshot()
}

// getOrCreateAnalysisGate returns the cache-shared analysis gate,
// constructing it on first use. The gate is shared across
// concurrent Analyze calls from the same Cache so the clustering
// bias is consistent.
func (c *Cache) getOrCreateAnalysisGate() *analysisGate {
	c.gateMu.Lock()
	defer c.gateMu.Unlock()
	if c.analysisGate == nil {
		c.analysisGate = newAnalysisGate(
			runtime.GOMAXPROCS(0),
			c.maxInFlightPackages,
			defaultGateFallthroughT,
		)
	}
	return c.analysisGate
}

var cacheIndex, sessionIndex, viewIndex int64

func (c *Cache) ID() string                     { return c.id }
func (c *Cache) MemStats() map[reflect.Type]int { return c.store.Stats() }

// FileStats returns information about the set of files stored in the cache.
// It is intended for debugging only.
func (c *Cache) FileStats() (stats command.FileStats) {
	stats.Total, stats.Largest, stats.Errs = c.fileStats()
	return
}
