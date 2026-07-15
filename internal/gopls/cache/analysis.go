// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// This file defines gopls' driver for modular static analysis (go/analysis).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/filecache"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/analysis/driverutil"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/astutil"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/event"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/facts"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/typesyncmu"
	"github.com/conductorone/plaid-lint/internal/gopls/label"
	"github.com/conductorone/plaid-lint/internal/gopls/progress"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/gopls/util/bug"
	"github.com/conductorone/plaid-lint/internal/gopls/util/frob"
	"github.com/conductorone/plaid-lint/internal/gopls/util/moremaps"
	"github.com/conductorone/plaid-lint/internal/gopls/util/persistent"
	"github.com/conductorone/plaid-lint/internal/gopls/util/safetoken"
	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
)

/*

   DESIGN

   An analysis request ([Snapshot.Analyze]) computes diagnostics for the
   requested packages using the set of analyzers enabled in this view. Each
   request constructs a transitively closed DAG of nodes, each representing a
   package, then works bottom up in parallel postorder calling
   [analysisNode.runCached] to ensure that each node's analysis summary is up
   to date. The summary contains the analysis diagnostics and serialized facts.

   The entire DAG is ephemeral. Each node in the DAG records the set of
   analyzers to run: the complete set for the root packages, and the "facty"
   subset for dependencies. Each package is thus analyzed at most once.

   Each node has a cryptographic key, which is either memoized in the Snapshot
   or computed by [analysisNode.cacheKey]. This key is a hash of the "recipe"
   for the analysis step, including the inputs into the type checked package
   (and its reachable dependencies), the set of analyzers, and importable
   facts.

   The key is sought in a machine-global persistent file-system based cache. If
   this gopls process, or another gopls process on the same machine, has
   already performed this analysis step, runCached will make a cache hit and
   load the serialized summary of the results. If not, it will have to proceed
   to run() to parse and type-check the package and then apply a set of
   analyzers to it. (The set of analyzers applied to a single package itself
   forms a graph of "actions", and it too is evaluated in parallel postorder;
   these dependency edges within the same package are called "horizontal".)
   Finally it writes a new cache entry containing serialized diagnostics and
   analysis facts.

   The summary must record whether a package is transitively error-free
   (whether it would compile) because many analyzers are not safe to run on
   packages with inconsistent types.

   For fact encoding, we use the same fact set as the unitchecker (vet) to
   record and serialize analysis facts. The fact serialization mechanism is
   analogous to "deep" export data.

*/

// TODO(adonovan):
// - Add a (white-box) test of pruning when a change doesn't affect export data.
// - Optimise pruning based on subset of packages mentioned in exportdata.
// - Better logging so that it is possible to deduce why an analyzer is not
//   being run--often due to very indirect failures. Even if the ultimate
//   consumer decides to ignore errors, tests and other situations want to be
//   assured of freedom from errors, not just missing results. This should be
//   recorded.

// AnalysisProgressTitle is the title of the progress report for ongoing
// analysis. It is sought by regression tests for the progress reporting
// feature.
const AnalysisProgressTitle = "Analyzing Dependencies"

// Analyze applies the set of enabled analyzers to the packages in the pkgs
// map, and returns their diagnostics.
//
// Notifications of progress may be sent to the optional reporter.
func (s *Snapshot) Analyze(ctx context.Context, pkgs map[PackageID]*metadata.Package, reporter *progress.Tracker) ([]*Diagnostic, error) {
	start := time.Now() // for progress reporting

	var tagStr string // sorted comma-separated list of PackageIDs
	{
		keys := make([]string, 0, len(pkgs))
		for id := range pkgs {
			keys = append(keys, string(id))
		}
		sort.Strings(keys)
		tagStr = strings.Join(keys, ",")
	}
	ctx, done := event.Start(ctx, "snapshot.Analyze", label.Package.Of(tagStr))
	defer done()

	// Filter and sort enabled root analyzers.
	// A disabled analyzer may still be run if required by another.
	var (
		toSrc            = make(map[*analysis.Analyzer]*settings.Analyzer)
		enabledAnalyzers []*analysis.Analyzer // enabled subset + transitive requirements
	)
	for _, a := range settings.AllAnalyzers {
		if a.Enabled(s.Options()) {
			toSrc[a.Analyzer()] = a
			enabledAnalyzers = append(enabledAnalyzers, a.Analyzer())
		}
	}
	sort.Slice(enabledAnalyzers, func(i, j int) bool {
		return enabledAnalyzers[i].Name < enabledAnalyzers[j].Name
	})

	enabledAnalyzers = requiredAnalyzers(enabledAnalyzers)

	// Perform basic sanity checks.
	// (Ideally we would do this only once.)
	if err := analysis.Validate(enabledAnalyzers); err != nil {
		return nil, fmt.Errorf("invalid analyzer configuration: %v", err)
	}

	stableNames := make(map[*analysis.Analyzer]string)

	var facty []*analysis.Analyzer // facty subset of enabled + transitive requirements
	for _, a := range enabledAnalyzers {
		// TODO(adonovan): reject duplicate stable names (very unlikely).
		stableNames[a] = stableName(a)

		// Register fact types of all required analyzers.
		if len(a.FactTypes) > 0 {
			facty = append(facty, a)
			for _, f := range a.FactTypes {
				gob.Register(f) // <2us
			}
		}
	}
	facty = requiredAnalyzers(facty)

	batch, release := s.acquireTypeChecking()
	defer release()

	ids := moremaps.KeySlice(pkgs)
	handles, err := s.getPackageHandles(ctx, ids)
	if err != nil {
		return nil, err
	}
	batch.addHandles(handles)

	// Starting from the root packages and following DepsByPkgPath,
	// build the DAG of packages we're going to analyze.
	//
	// Root nodes will run the enabled set of analyzers,
	// whereas dependencies will run only the facty set.
	// Because (by construction) enabled is a superset of facty,
	// we can analyze each node with exactly one set of analyzers.
	nodes := make(map[PackageID]*analysisNode)
	var leaves []*analysisNode // nodes with no unfinished successors
	var makeNode func(from *analysisNode, id PackageID) (*analysisNode, error)
	makeNode = func(from *analysisNode, id PackageID) (*analysisNode, error) {
		an, ok := nodes[id]
		if !ok {
			ph := handles[id]
			if ph == nil {
				return nil, bug.Errorf("no metadata for %s", id)
			}

			// -- preorder --

			an = &analysisNode{
				parseCache:  s.view.parseCache,
				fsource:     s, // expose only ReadFile
				batch:       batch,
				ph:          ph,
				analyzers:   facty, // all nodes run at least the facty analyzers
				stableNames: stableNames,
			}
			nodes[id] = an

			// -- recursion --

			// Build subgraphs for dependencies.
			an.succs = make(map[PackageID]*analysisNode, len(ph.mp.DepsByPkgPath))
			for _, depID := range ph.mp.DepsByPkgPath {
				dep, err := makeNode(an, depID)
				if err != nil {
					return nil, err
				}
				an.succs[depID] = dep
			}

			// -- postorder --

			// Add leaf nodes (no successors) directly to queue.
			if len(an.succs) == 0 {
				leaves = append(leaves, an)
			}
		}
		// Add edge from predecessor.
		if from != nil {
			from.unfinishedSuccs.Add(+1) // incref
			an.preds = append(an.preds, from)
			// Count this predecessor as a future consumer of an's
			// summary / *types.Package. Root nodes (from==nil) are NOT
			// counted as consumers — once their own analysis completes
			// and onNodeAnalyzed fires, no other node will read them, so
			// they may be released too.
			an.unfinishedSummaryConsumers.Add(+1)
		}
		// Increment unfinishedPreds even for root nodes (from==nil), so that their
		// Action summaries are never cleared.
		an.unfinishedPreds.Add(+1)
		return an, nil
	}

	// For root packages, we run the enabled set of analyzers.
	var roots []*analysisNode
	for id := range pkgs {
		root, err := makeNode(nil, id)
		if err != nil {
			return nil, err
		}
		root.analyzers = enabledAnalyzers
		root.isRoot = true
		roots = append(roots, root)
	}

	// Progress reporting. If supported, gopls reports progress on analysis
	// passes that are taking a long time.
	maybeReport := func(completed int64) {}

	// Enable progress reporting if enabled by the user
	// and we have a capable reporter.
	if reporter != nil && reporter.SupportsWorkDoneProgress() && s.Options().AnalysisProgressReporting {
		var reportAfter = s.Options().ReportAnalysisProgressAfter // tests may set this to 0
		const reportEvery = 1 * time.Second

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		var (
			reportMu   sync.Mutex
			lastReport time.Time
			wd         *progress.WorkDone
		)
		defer func() {
			reportMu.Lock()
			defer reportMu.Unlock()

			if wd != nil {
				wd.End(ctx, "Done.") // ensure that the progress report exits
			}
		}()
		maybeReport = func(completed int64) {
			now := time.Now()
			if now.Sub(start) < reportAfter {
				return
			}

			reportMu.Lock()
			defer reportMu.Unlock()

			if wd == nil {
				wd = reporter.Start(ctx, AnalysisProgressTitle, "", nil, cancel)
			}

			if now.Sub(lastReport) > reportEvery {
				lastReport = now
				// Trailing space is intentional: some LSP clients strip newlines.
				msg := fmt.Sprintf(`Indexed %d/%d packages. (Set "analysisProgressReporting" to false to disable notifications.)`,
					completed, len(nodes))
				wd.Report(ctx, msg, float64(completed)/float64(len(nodes)))
			}
		}
	}

	// Under PLAID_INPUT_DIGEST=1, install a typecheck-before-
	// analyze barrier on the batch. The barrier owns the typesyncmu
	// load-phase claim from this point until releaseAtTeardown fires
	// at the end of dispatch. acquireTypeChecking already incremented
	// the load-phase counter; we hand ownership over by setting
	// loadPhaseClaimedByBarrier so the teardown branch skips its
	// matching ExitLoadPhase.
	barrierEnabled := inputDigestEnabled()
	if barrierEnabled {
		batch.loadPhase = newBarrierForExistingClaim(len(nodes))
		batch.loadPhaseClaimedByBarrier.Store(true)
		defer batch.loadPhase.releaseAtTeardown()
	}

	// Execute phase: run leaves first, adding
	// new nodes to the queue as they become leaves.
	var g errgroup.Group

	// Analysis is CPU-bound.
	//
	// Note: avoid g.SetLimit here: it makes g.Go stop accepting work, which
	// prevents workers from enqeuing, and thus finishing, and thus allowing the
	// group to make progress: deadlock.
	//
	// The outer limiter is the cache-shared analysisGate. With the default
	// configuration (MaxInFlightPackages == 0) the gate degrades to the
	// prior channel-semaphore: cap = GOMAXPROCS, no clustering bias. With
	// clustering enabled, the gate additionally caps distinct in-flight
	// packages.
	gate := s.view.c.getOrCreateAnalysisGate()
	var completed atomic.Int64

	var enqueue func(*analysisNode)
	enqueue = func(an *analysisNode) {
		g.Go(func() error {
			rawRelease, err := gate.acquire(ctx, an.ph.mp.ID)
			if err != nil {
				return err
			}
			// Wrap release in sync.Once: the barrier-aware path
			// releases the gate after typecheck and re-acquires
			// for analyze, so it explicitly fires release before
			// invoking runEnqueueBarrierAware. The defer below must
			// be a no-op in that case. The flag=0 path simply lets
			// the defer fire once at the end of the enqueue body.
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(rawRelease) }
			defer release()

			// Check to see if we already have a valid cache key. If not, compute it.
			//
			// The snapshot field that memoizes keys depends on whether this key is
			// for the analysis result including all enabled analyzer, or just facty analyzers.
			var keys *persistent.Map[PackageID, file.Hash]
			if _, root := pkgs[an.ph.mp.ID]; root {
				keys = s.fullAnalysisKeys
			} else {
				keys = s.factyAnalysisKeys
			}

			// As keys is referenced by a snapshot field, it's guarded by s.mu.
			s.mu.Lock()
			key, keyFound := keys.Get(an.ph.mp.ID)
			s.mu.Unlock()

			if !keyFound {
				key = an.cacheKey()
				s.mu.Lock()
				keys.Set(an.ph.mp.ID, key, nil)
				s.mu.Unlock()
			}

			if barrierEnabled {
				return runEnqueueBarrierAware(ctx, s, gate, release, an, key, enqueue, maybeReport, &completed)
			}

			summary, err := an.runCached(ctx, key)
			if err != nil {
				return err // cancelled, or failed to produce a package
			}

			maybeReport(completed.Add(1))
			// Copy fields onto the node. The summary itself (which may
			// be shared via filecache or inFlightAnalyses) is not
			// retained, so it is never mutated.
			an.compiles = summary.Compiles
			an.actions = summary.Actions

			// Dep-override callback. Invoked BEFORE decrefPreds frees an.actions
			// on the successor side: the engine needs the per-analyzer
			// fact blobs from every node (root + dep) to write L0 entries
			// for the warm-run override fast path. The reverse stableName
			// map is built once below from an.stableNames; the analyzer
			// pointers in the resulting L0NodeData refer to the same
			// *analysis.Analyzer instances the action graph used.
			if cb := an.batch.onNodeAnalyzed; cb != nil && summary != nil && summary.Actions != nil {
				cb(buildL0NodeData(an, summary))
			}

			// Notify each waiting predecessor,
			// and enqueue it when it becomes a leaf.
			for _, pred := range an.preds {
				if pred.unfinishedSuccs.Add(-1) == 0 { // decref
					enqueue(pred)
				}
			}

			// Notify each successor that we no longer need
			// its action summaries, which hold Result values.
			// After the last one, delete it, so that we
			// free up large results such as SSA.
			for _, succ := range an.succs {
				succ.decrefPreds()
				// This node has finished consuming succ's
				// summary AND succ's *types.Package (which we imported
				// at typeCheck time). When the last consumer decrements
				// to zero, evict succ's persistent state from the batch
				// so the *types.Package and its parsed files become
				// garbage-collectable. Behind the same env-var escape
				// hatch as the rest of the aggressive-release path.
				succ.decrefSummaryConsumers()
			}
			// This node is a root (or all its preds already
			// finished). With no remaining consumers, release self too.
			// The check is no-op when unfinishedSummaryConsumers>0 because
			// the dec on the last pred (above) is what fires the release.
			an.maybeReleaseAggressive()
			return nil
		})
	}
	for _, leaf := range leaves {
		enqueue(leaf)
	}
	if err := g.Wait(); err != nil {
		return nil, err // cancelled, or failed to produce a package
	}

	// Inv: all root nodes now have a summary.
	for _, root := range roots {
		if root.actions == nil {
			panic("root analysisNode has nil actions")
		}
	}

	// Report diagnostics only from enabled actions that succeeded.
	// Errors from creating or analyzing packages are ignored.
	// Diagnostics are reported in the order of the analyzers argument.
	//
	// TODO(adonovan): ignoring action errors gives the caller no way
	// to distinguish "there are no problems in this code" from
	// "the code (or analyzers!) are so broken that we couldn't even
	// begin the analysis you asked for".
	// Even if current callers choose to discard the
	// results, we should propagate the per-action errors.
	// Phase 1.7 Lever J instrumentation: count summary.Err categories
	// per analyzer + root package. Surfaces silent drop paths in the
	// driver under GC pressure. Set PLAID_LEVER_J_TRACE=1 to enable.
	var leverJErrs map[string]int
	if os.Getenv("PLAID_LEVER_J_TRACE") == "1" {
		leverJErrs = make(map[string]int)
		defer func() {
			if len(leverJErrs) > 0 {
				fmt.Fprintln(os.Stderr, "[lever-j-trace] action summary errors by (analyzer, pkg, error-prefix):")
				keys := make([]string, 0, len(leverJErrs))
				for k := range leverJErrs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(os.Stderr, "  %d  %s\n", leverJErrs[k], k)
				}
			}
		}()
	}
	if os.Getenv("PLAID_ADR11_TRACE") == "1" {
		defer adr11DumpTrace()
	}

	var results []*Diagnostic
	for _, root := range roots {
		for _, a := range enabledAnalyzers {
			// Skip analyzers that were added only to
			// fulfil requirements of the original set.
			srcAnalyzer, ok := toSrc[a]
			if !ok {
				// Although this 'skip' operation is logically sound,
				// it is nonetheless surprising that its absence should
				// cause #60909 since none of the analyzers currently added for
				// requirements (e.g. ctrlflow, inspect, buildssa)
				// is capable of reporting diagnostics.
				if summary := root.actions[stableNames[a]]; summary != nil {
					if n := len(summary.Diagnostics); n > 0 {
						bug.Reportf("Internal error: got %d unexpected diagnostics from analyzer %s. This analyzer was added only to fulfil the requirements of the requested set of analyzers, and it is not expected that such analyzers report diagnostics. Please report this in issue #60909.", n, a)
					}
				}
				continue
			}

			// Inv: root.actions is the successful result of run (via runCached).
			summary, ok := root.actions[stableNames[a]]
			if summary == nil {
				panic(fmt.Sprintf("analyzeSummary.Actions[%q] = (nil, %t); got %v (#60551)",
					stableNames[a], ok, root.actions))
			}
			if summary.Err != "" {
				if leverJErrs != nil {
					errMsg := summary.Err
					if len(errMsg) > 200 {
						errMsg = errMsg[:200]
					}
					k := fmt.Sprintf("%s | %s | %s", a.Name, root.ph.mp.ID, errMsg)
					leverJErrs[k]++
				}
				continue // action failed
			}
			for _, gobDiag := range summary.Diagnostics {
				results = append(results, toSourceDiagnostic(srcAnalyzer, &gobDiag))
			}
		}
	}
	return results, nil
}

func (an *analysisNode) decrefPreds() {
	if an.unfinishedPreds.Add(-1) == 0 {
		an.actions = nil
		// memo aliases an.actions[*].Facts; drop together.
		an.gobFactsCache = sync.Map{}
	}
}

// decodeFactsFor returns this node's analyzer-stableName facts,
// gob-decoding once across all consumer siblings (Layer A).
func (an *analysisNode) decodeFactsFor(stableName string, data []byte) (facts.GobFacts, error) {
	if len(data) == 0 {
		return facts.GobFacts{}, nil
	}
	entry, _ := an.gobFactsCache.LoadOrStore(stableName, &gobFactsEntry{})
	e := entry.(*gobFactsEntry)
	e.once.Do(func() {
		e.gf, e.err = facts.DecodeGobFacts(data)
	})
	return e.gf, e.err
}

// aggressiveReleaseEnabled reports whether the aggressive-release
// path is engaged. Default: enabled. PLAID_AGGRESSIVE_RELEASE=0
// disables it and returns to the earlier behavior of holding every
// dep's *types.Package alive until the batch ends.
var aggressiveReleaseEnabled = func() bool {
	if v, ok := os.LookupEnv("PLAID_AGGRESSIVE_RELEASE"); ok && v == "0" {
		return false
	}
	return true
}()

// decrefSummaryConsumers is the aggressive-release-side decrement. Each time
// a predecessor finishes consuming this node's summary (and its
// *types.Package via b.importPackages), one consumer is removed. When
// the count reaches zero, the node's heavy state is eligible for
// release. Roots start at 0 and are released by the explicit
// maybeReleaseAggressive call at the end of their own enqueue body.
func (an *analysisNode) decrefSummaryConsumers() {
	if an.unfinishedSummaryConsumers.Add(-1) == 0 {
		an.maybeReleaseAggressive()
	}
}

// maybeReleaseAggressive performs the actual eviction once
// unfinishedSummaryConsumers has hit zero. CAS ensures the work runs
// exactly once even if a root node's own enqueue and a (now-impossible
// but defensively-handled) racing decrement both reach zero. Guarded by
// the PLAID_AGGRESSIVE_RELEASE escape hatch.
func (an *analysisNode) maybeReleaseAggressive() {
	if !aggressiveReleaseEnabled {
		return
	}
	if an.isRoot {
		// Roots' actions are consumed by the post-Analyze diagnostic
		// gather; the engine reads root.actions after the analysis
		// errgroup returns. Releasing them here would corrupt the
		// post-loop invariant "all root nodes have a summary".
		return
	}
	if an.unfinishedSummaryConsumers.Load() != 0 {
		return // still has consumers
	}
	if !an.releasedAggressive.CompareAndSwap(false, true) {
		return // already released
	}
	an.releaseAggressive()
}

// releaseAggressive evicts the node's persistent batch state so the
// *types.Package and its imported file trees can be garbage collected.
// Called at most once per node (guarded by releasedAggressive). Safe to
// call concurrently with other workers' runCached calls on this batch:
//
//   - The futureCache's persistent map is protected by its own mutex.
//   - The refcount invariant means no remaining pred has yet to import
//     this node's *types.Package (every pred either decremented before
//     us, or is us, or has already returned from typeCheck). Any
//     reader still holding *types.Package via an ordinary Go pointer
//     (apkg.pkg.types or b.l2Imports) keeps it alive through normal
//     references — we only drop the cache map's hold.
//   - All decrements happen AFTER the consumer's runCached has returned
//     and the L0 onNodeAnalyzed callback (if installed) has copied the
//     per-analyzer fact blobs out by value, so the engine never
//     observes a missing L0NodeData entry.
//
// The extended release set covers:
//
//   - an.stableNames (the per-analyzer cross-process name map) —
//     proportional to the enabled-analyzer count, kept on every node
//     in the workspace closure.
//   - an.preds and an.succs (the bidirectional edge slices/maps) —
//     once the enqueue body has decremented every neighbor and
//     returned, no caller of this node touches them again. Releasing
//     the edges lets transitive references to other nodes' state
//     become collectible sooner.
//   - The L0 dep-override synthetic summary's Actions map for this
//     node's package, if one was consumed at runCached time. The
//     synth was built from an L0DepEntry and carries gob-decoded
//     Facts []byte slices; once an.actions has aliased them and the
//     L0 onNodeAnalyzed callback has run, the map's hold on those
//     blobs is the last persistent reference and we can drop it.
func (an *analysisNode) releaseAggressive() {
	if an.batch == nil {
		return
	}

	id := an.ph.mp.ID

	// Evict the importPackages entry. This is THE leak vector: this
	// futureCache is persistent (it holds *types.Package across the
	// whole batch). After this delete, no future getImportPackage call
	// for this id will reuse the entry; if some node still needs it,
	// the futureCache will compute a fresh one — which is fine because
	// our refcount said nobody does.
	an.batch.importPackages.release(id)

	// Drop the action graph's contribution to this node's footprint.
	// an.actions may already be nil if decrefPreds got here first
	// (which it usually does in topological order). The summary's
	// per-analyzer fact blobs were already aliased into the L0
	// callback's L0NodeData payload if a callback is installed.
	an.actions = nil

	// Belt-and-braces for the path where releaseAggressive
	// fires without decrefPreds having already cleared the memo.
	an.gobFactsCache = sync.Map{}

	// Extended release set. Each of these clears a
	// reference that the earlier aggressive-release baseline left pinned
	// for the remainder of the batch.
	an.stableNames = nil
	an.preds = nil
	an.succs = nil

	// Release the L0 dep-override synthetic summary's
	// Actions map. When runCached returned an override, the consumer
	// aliased synth.Actions into an.actions; the L0 onNodeAnalyzed
	// callback then aliased the Facts []byte slices into its
	// L0NodeData payload. After both have happened, the synth's
	// Actions map is the last persistent reference inside the batch.
	// Drop it via the batch's mutating helper so the gob-decoded fact
	// blobs become collectible while the batch is still in flight.
	an.batch.releaseL0OverrideActions(id)
}

// An analysisNode is a node in a doubly-linked DAG isomorphic to the
// import graph. Each node represents a single package, and the DAG
// represents a batch of analysis work done at once using a single
// realm of token.Pos or types.Object values.
//
// A complete DAG is created anew for each batch of analysis;
// subgraphs are not reused over time.
// TODO(rfindley): with cached keys we can typically avoid building the full
// DAG, so as an optimization we should rewrite this using a top-down
// traversal, rather than bottom-up.
//
// Each node's run method is called in parallel postorder. On success,
// its summary field is populated, either from the cache (hit), or by
// type-checking and analyzing syntax (miss).
type analysisNode struct {
	parseCache      *parseCache                 // shared parse cache
	fsource         file.Source                 // Snapshot.ReadFile, for use by Pass.ReadFile
	batch           *typeCheckBatch             // type checking batch, for shared type checking
	ph              *packageHandle              // package handle, for key and reachability analysis
	analyzers       []*analysis.Analyzer        // set of analyzers to run
	preds           []*analysisNode             // graph edges:
	succs           map[PackageID]*analysisNode //   (preds -> self -> succs)
	unfinishedSuccs atomic.Int32
	unfinishedPreds atomic.Int32 // effectively an actions refcount

	// unfinishedSummaryConsumers is the aggressive-release refcount.
	// It tracks the number of dependents (preds) plus the node itself
	// that still need this node's heavy state (*types.Package held in
	// b.importPackages, the action graph's *act.result values, the
	// per-file *ast.File trees). When it hits zero we evict the
	// importPackages entry so the dep's *types.Package becomes
	// garbage-collectable. Mirrors unfinishedPreds at increment time so
	// that the release fires at the same point decrefPreds nils
	// an.actions today, but only after the L0 onNodeAnalyzed callback
	// has read the summary. Gated by PLAID_AGGRESSIVE_RELEASE.
	unfinishedSummaryConsumers atomic.Int32

	// releasedAggressive is set once the aggressive-release path has
	// fired for this node. Guarded by the same CAS step that decrements
	// unfinishedSummaryConsumers to zero, so we never double-release.
	releasedAggressive atomic.Bool

	// isRoot is true for nodes the caller registered as analysis roots
	// (i.e. created via makeNode(nil, id) at the top of Snapshot.Analyze).
	// Root nodes' actions are read by the post-loop diagnostic gather
	// stage, so the aggressive-release path leaves them alone — only their
	// transitive dep nodes are eligible for eviction.
	isRoot bool

	compiles    bool                          // copied from analyzeSummary
	actions     actionMap                     // copied from analyzeSummary; nilled by decrefPreds
	stableNames map[*analysis.Analyzer]string // cross-process stable names for Analyzers

	// gobFactsCache memoizes the gob-decoded fact slice per analyzer
	// stableName so sibling consumer packages share one decode pass
	// (Layer A). Cleared by decrefPreds / releaseAggressive
	// alongside an.actions.
	gobFactsCache sync.Map // string → *gobFactsEntry

	summaryHashOnce sync.Once
	_summaryHash    file.Hash // memoized hash of data affecting dependents

	// analyzeDone is the per-node analyze-phase signal. Closed once
	// an.actions has been written (either by the cache-shortcut path
	// or by runFromTypeCheck after execActions completes). A node's
	// analyze phase blocks on each of its vdep's analyzeDone before
	// reading vdep.actions inside execActions; this preserves the
	// DAG-ordered serialization that runCached used to provide via
	// the unfinishedSuccs gate, which fires after typecheck
	// rather than after analyze.
	//
	// Initialised lazily by ensureAnalyzeDone (idempotent). Channels
	// can only be closed once; closeAnalyzeDone uses sync.Once for
	// safety against double-close from cache-shortcut + barrier paths.
	analyzeDoneInit sync.Once
	analyzeDone     chan struct{}
	analyzeDoneOnce sync.Once
}

// ensureAnalyzeDone initialises an.analyzeDone (idempotent). Called
// lazily at the first access point so nodes that never enter the
// barrier-aware path (flag=0) skip the allocation.
func (an *analysisNode) ensureAnalyzeDone() {
	an.analyzeDoneInit.Do(func() {
		an.analyzeDone = make(chan struct{})
	})
}

// closeAnalyzeDone signals that an.actions has been written and is now
// safe to read from a peer node's analyze phase. Idempotent.
func (an *analysisNode) closeAnalyzeDone() {
	an.ensureAnalyzeDone()
	an.analyzeDoneOnce.Do(func() { close(an.analyzeDone) })
}

type gobFactsEntry struct {
	once sync.Once
	gf   facts.GobFacts
	err  error
}

func (an *analysisNode) String() string { return string(an.ph.mp.ID) }

// summaryHash computes the hash of the node summary, which may affect other
// nodes depending on this node.
//
// The result is memoized to avoid redundant work when analyzing multiple
// dependents.
func (an *analysisNode) summaryHash() file.Hash {
	an.summaryHashOnce.Do(func() {
		hasher := sha256.New()
		fmt.Fprintf(hasher, "dep: %s\n", an.ph.mp.PkgPath)
		fmt.Fprintf(hasher, "compiles: %t\n", an.compiles)

		// action results: errors and facts
		for name, summary := range moremaps.Sorted(an.actions) {
			fmt.Fprintf(hasher, "action %s\n", name)
			// Reads vdep.actions; under the flag=1 cacheKey path
			// this code does NOT execute (cacheKeyInputBased
			// substitutes inputDigest). Observed by the
			// vdepActionRead instrument so
			// TestInputDigest_NoActionFieldRead can pin the
			// structural decoupling.
			vdepActionReadObserve(1)
			if summary.Err != "" {
				fmt.Fprintf(hasher, "error %s\n", summary.Err)
			} else {
				fmt.Fprintf(hasher, "facts %s\n", summary.FactsHash)
				// We can safely omit summary.diagnostics
				// from the key since they have no downstream effect.
			}
		}
		hasher.Sum(an._summaryHash[:0])
		if hook := outputDigestHook; hook != nil {
			an._summaryHash = hook(an, an._summaryHash)
		}
	})
	return an._summaryHash
}

// analyzeSummary is a gob-serializable summary of successfully
// applying a list of analyzers to a package.
type analyzeSummary struct {
	Compiles bool      // transitively free of list/parse/type errors
	Actions  actionMap // maps analyzer stablename to analysis results (*actionSummary)
}

// actionMap defines a stable Gob encoding for a map.
// TODO(adonovan): generalize and move to a library when we can use generics.
type actionMap map[string]*actionSummary

var (
	_ gob.GobEncoder = (actionMap)(nil)
	_ gob.GobDecoder = (*actionMap)(nil)
)

type actionsMapEntry struct {
	K string
	V *actionSummary
}

func (m actionMap) GobEncode() ([]byte, error) {
	entries := make([]actionsMapEntry, 0, len(m))
	for k, v := range m {
		entries = append(entries, actionsMapEntry{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].K < entries[j].K
	})
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(entries)
	return buf.Bytes(), err
}

func (m *actionMap) GobDecode(data []byte) error {
	var entries []actionsMapEntry
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&entries); err != nil {
		return err
	}
	*m = make(actionMap, len(entries))
	for _, e := range entries {
		(*m)[e.K] = e.V
	}
	return nil
}

// actionSummary is a gob-serializable summary of one possibly failed analysis action.
// If Err is non-empty, the other fields are undefined. The unexported
// gobFacts is the Layer B in-process shortcut and is intentionally
// not gob-serialized.
type actionSummary struct {
	Facts       []byte    // the encoded facts.Set
	FactsHash   file.Hash // hash(Facts)
	Diagnostics []gobDiagnostic
	Err         string // "" => success

	// gobFacts, when non-zero, is the producer's pre-gob-decoded fact
	// slice; in-process consumers read it directly and skip
	// gob.Decode. Zero on cross-process summaries (L1 restore, L0
	// override) — consumer then falls back to the Layer A memo.
	gobFacts facts.GobFacts
}

var (
	// inFlightAnalyses records active analysis operations so that later requests
	// can be satisfied by joining onto earlier requests that are still active.
	//
	// Note that persistent=false, so results are cleared once they are delivered
	// to awaiting goroutines.
	inFlightAnalyses = newFutureCache[file.Hash, *analyzeSummary](false)

	// cacheLimit reduces parallelism of filecache updates.
	// We allow more than typical GOMAXPROCS as it's a mix of CPU and I/O.
	cacheLimit = make(chan unit, 32)
)

// runCached applies a list of analyzers (plus any others
// transitively required by them) to a package.  It succeeds as long
// as it could produce a types.Package, even if there were direct or
// indirect list/parse/type errors, and even if all the analysis
// actions failed. It usually fails only if the package was unknown,
// a file was missing, or the operation was cancelled.
//
// The provided key is the cache key for this package.
func (an *analysisNode) runCached(ctx context.Context, key file.Hash) (*analyzeSummary, error) {
	// Dep-override fast path. Consulted BEFORE the gopls-shared
	// filecache and the L1 action graph. When the engine has
	// pre-installed an override entry for this node's package, every
	// downstream consumer's fact lookup is satisfied without ever
	// type-checking or running an analyzer body for this package. Closes
	// the leaf-edit cascade diagnosed (1,074 dep buildir runs per
	// leaf edit → 0). The override map is installed once per snapshot,
	// before Analyze begins, and is read-only during the run.
	if an.batch != nil {
		if synth := an.batch.l0OverrideFor(an.ph.mp.ID); synth != nil {
			l0OverrideHits.add(1)
			return synth, nil
		}
	}

	// At this point we have the action results (serialized packages and facts)
	// of our immediate dependencies, and the metadata and content of this
	// package.
	//
	// We now consult a global cache of promised results. If nothing material has
	// changed, we'll make a hit in the shared cache.

	// Access the cache. The returned summary is shared (via memCache or
	// inFlightAnalyses) and must be treated as immutable; the caller
	// copies its fields onto the analysisNode rather than retaining it.
	//
	// When the plaid-lint L1 cache is attached, the gopls analyzeSummary
	// filecache is bypassed: L1 is more granular (per-(package,
	// analyzer)) and incorporates plaid-lint's ToolVersion, so a
	// filecache hit would suppress the L1 layer entirely and break the
	// W6 hit-rate invariant.
	const cacheKind = "analysis"
	l1Attached := an.batch != nil && an.batch.l1 != nil
	if !l1Attached {
		if summary, err := filecache.Get(cacheKind, key, analyzeSummaryCodec.Decode); err == nil {
			return summary, nil // cache hit
		} else if err != filecache.ErrNotFound {
			return nil, bug.Errorf("internal error reading shared cache: %v", err)
		}
	}

	// Cache miss: do the work.
	return inFlightAnalyses.get(ctx, key, func(ctx context.Context) (*analyzeSummary, error) {
		summary, err := an.run(ctx)
		if err != nil {
			return nil, err
		}
		if !l1Attached {
			go func() {
				cacheLimit <- unit{}            // acquire token
				defer func() { <-cacheLimit }() // release token

				data := analyzeSummaryCodec.Encode(summary)
				if false {
					log.Printf("Set key=%d value=%d id=%s\n", len(key), len(data), an.ph.mp.ID)
				}
				if err := filecache.Set(cacheKind, key, data); err != nil {
					event.Error(ctx, "internal error updating analysis shared cache", err)
				}
			}()
		}
		return summary, nil
	})
}

// cacheKey returns a cache key that is a cryptographic digest
// of the all the values that might affect type checking and analysis:
// the analyzer names, package metadata, names and contents of
// compiled Go files, and vdeps (successor) information
// (export data and facts).
//
// Under PLAID_INPUT_DIGEST=1 the input-based derivation
// fires instead, computing the key from data available at
// analysisNode construction time (ph.key, analyzer set,
// EngineCacheVersion). The flag is OFF by default; production
// behavior at flag=0 is identical to the prior shape.
func (an *analysisNode) cacheKey() file.Hash {
	if inputDigestEnabled() {
		if inputDigestVerifyEnabled() {
			// Walk each vdep's inputDigest twice. inputDigest is
			// a pure function of vdep.ph + vdep.analyzers + the
			// engine cache version — two consecutive invocations
			// MUST agree. Per-vdep check fires before the
			// consumer-level check so the panic message points
			// at the offending node directly. The static +
			// runtime audit pins zero failures across
			// BundledRegistry; verify mode is the runtime safety
			// net for analyzers added after that snapshot.
			for _, vdep := range an.succs {
				a := vdep.inputDigest()
				b := vdep.inputDigest()
				if a != b {
					panic(verifyDivergenceError("inputDigest:vdep", vdep, a, b))
				}
			}
			// Consumer-level re-derive: catches non-determinism
			// in the consumer's own inputs (ph.key, analyzer set
			// id, engine version).
			k := an.cacheKeyInputBased()
			k2 := an.cacheKeyInputBased()
			if k != k2 {
				panic(verifyDivergenceError("inputDigest", an, k, k2))
			}
			return k
		}
		return an.cacheKeyInputBased()
	}
	return an.cacheKeyOutputBased()
}

// cacheKeyOutputBased is the prior cacheKey derivation. Reads
// vdep.summaryHash(), which in turn reads vdep.actions — only
// populated AFTER the vdep's runCached returns. Retained as the
// flag=0 default path.
func (an *analysisNode) cacheKeyOutputBased() file.Hash {
	hasher := sha256.New()

	// In principle, a key must be the hash of an
	// unambiguous encoding of all the relevant data.
	// If it's ambiguous, we risk collisions.

	// analyzers
	fmt.Fprintf(hasher, "analyzers: %d\n", len(an.analyzers))
	for _, a := range an.analyzers {
		fmt.Fprintln(hasher, a.Name)
	}

	// type checked package
	fmt.Fprintf(hasher, "package: %s\n", an.ph.key)

	// metadata errors: used for 'compiles' field
	fmt.Fprintf(hasher, "errors: %d", len(an.ph.mp.Errors))

	// vdeps, in PackageID order
	for _, vdep := range moremaps.Sorted(an.succs) {
		hash := vdep.summaryHash()
		hasher.Write(hash[:])
	}

	var hash file.Hash
	hasher.Sum(hash[:0])
	return hash
}

// cacheKeyInputBased is the PLAID_INPUT_DIGEST=1 path. Reads
// only fields populated at analysisNode construction time, so a
// consumer's cacheKey can be computed BEFORE any of its vdeps have
// run. Folds in inputDigestDomain so the flag=1 keyspace is isolated
// from flag=0 entries when shared across one process lifetime.
//
// The shape mirrors cacheKeyOutputBased's first three sections
// (analyzer set, ph.key, metadata error count) but replaces the
// vdep.summaryHash() loop with vdep.inputDigest() — a hash of the
// vdep's pre-execActions inputs rather than its post-execActions
// output.
func (an *analysisNode) cacheKeyInputBased() file.Hash {
	hasher := sha256.New()

	// (Domain tag: isolate flag=1 keys from flag=0 keys.)
	hasher.Write([]byte(inputDigestDomain))

	// analyzers — same shape as cacheKeyOutputBased.
	fmt.Fprintf(hasher, "analyzers: %d\n", len(an.analyzers))
	for _, a := range an.analyzers {
		fmt.Fprintln(hasher, a.Name)
	}

	// type checked package — same shape.
	fmt.Fprintf(hasher, "package: %s\n", an.ph.key)

	// metadata errors: used for 'compiles' field — same shape.
	fmt.Fprintf(hasher, "errors: %d", len(an.ph.mp.Errors))

	// vdeps, in PackageID order. Substitute inputDigest for
	// summaryHash. inputDigest does not read vdep.actions, so this
	// loop is safe to call before any vdep has finished runCached.
	for _, vdep := range moremaps.Sorted(an.succs) {
		hash := vdep.inputDigest()
		hasher.Write(hash[:])
	}

	var hash file.Hash
	hasher.Sum(hash[:0])
	return hash
}

// run implements the cache-miss case.
// This function does not access the snapshot.
//
// Postcondition: on success, the analyzeSummary.Actions
// key set is {a.Name for a in analyzers}.
func (an *analysisNode) run(ctx context.Context) (*analyzeSummary, error) {
	// Type-check the package syntax.
	pkg, err := an.typeCheck(ctx)
	if err != nil {
		return nil, err
	}

	// Poll cancellation state.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return an.analyzeFromTypeCheck(ctx, pkg)
}

// analyzeFromTypeCheck performs the analyze portion of run() given a
// pre-computed analysisPackage. Factored out so the barrier-aware
// dispatch can interleave a barrier wait + per-vdep analyzeDone wait
// between typeCheck and execActions; flag=0 callers reach this through
// run() unchanged.
func (an *analysisNode) analyzeFromTypeCheck(ctx context.Context, pkg *analysisPackage) (*analyzeSummary, error) {
	// -- analysis --

	// Build action graph for this package.
	// Each graph node (action) is one unit of analysis.
	actions := make(map[*analysis.Analyzer]*action)
	var mkAction func(a *analysis.Analyzer) *action
	mkAction = func(a *analysis.Analyzer) *action {
		act, ok := actions[a]
		if !ok {
			var hdeps []*action
			for _, req := range a.Requires {
				hdeps = append(hdeps, mkAction(req))
			}
			act = &action{
				a:          a,
				fsource:    an.fsource,
				stableName: an.stableNames[a],
				pkg:        pkg,
				vdeps:      an.succs,
				hdeps:      hdeps,
				an:         an,
			}
			actions[a] = act
		}
		return act
	}

	// Build actions for initial package.
	var roots []*action
	for _, a := range an.analyzers {
		roots = append(roots, mkAction(a))
	}

	// Compute the consumer-of set: for every action A in this graph,
	// mark A.isPrerequisiteOfEnabled = true if some other action in
	// the same graph requires A. The marker is then narrowed by W7's
	// descriptor work: a prerequisite action whose descriptor has a
	// working Result codec can still hit L1 (the codec round-trips
	// the Result for the consumer), so it does NOT need the bypass.
	// Only prerequisites WITHOUT a Result codec fall back to the W6
	// always-run path.
	for _, act := range actions {
		for _, dep := range act.hdeps {
			if !descriptorCanRoundTripResult(an, dep.a) {
				dep.isPrerequisiteOfEnabled = true
			}
		}
	}

	// Execute the graph in parallel.
	execActions(ctx, roots)
	// Inv: each root's summary is set (whether success or error).

	// Don't return (or cache) the result in case of cancellation.
	if err := ctx.Err(); err != nil {
		return nil, err // cancelled
	}

	// Return summaries only for the requested actions.
	summaries := make(map[string]*actionSummary)
	for _, root := range roots {
		if root.summary == nil {
			panic("root has nil action.summary (#60551)")
		}
		summaries[root.stableName] = root.summary
	}

	return &analyzeSummary{
		Compiles: pkg.compiles,
		Actions:  summaries,
	}, nil
}

func (an *analysisNode) typeCheck(ctx context.Context) (*analysisPackage, error) {
	ppkg, err := an.batch.getPackage(ctx, an.ph)
	if err != nil {
		return nil, err
	}

	compiles := len(an.ph.mp.Errors) == 0 && len(ppkg.TypeErrors()) == 0

	// Instrumentation: capture the originating signal when a
	// previously-clean package flips to compiles=false. Gated by
	// PLAID_ADR11_TRACE=1; zero production overhead when unset.
	if !compiles && os.Getenv("PLAID_ADR11_TRACE") == "1" {
		adr11TraceCompilesFalse(an.ph.mp.ID, an.ph.mp.Errors, ppkg.TypeErrors(), nil, "")
	}

	// The go/analysis framework implicitly promises to deliver
	// trees with legacy ast.Object resolution. Do that now.
	files := make([]*ast.File, len(ppkg.CompiledGoFiles()))
	parseErrCount := 0
	for i, p := range ppkg.CompiledGoFiles() {
		p.Resolve()
		files[i] = p.File
		if p.ParseErr != nil {
			compiles = false // parse error
			parseErrCount++
		}
	}
	if parseErrCount > 0 && os.Getenv("PLAID_ADR11_TRACE") == "1" {
		adr11TraceCompilesFalse(an.ph.mp.ID, nil, nil, nil, fmt.Sprintf("parse-error-files=%d", parseErrCount))
	}

	// The fact decoder needs a means to look up a Package by path.
	pkgLookup := typesLookup(ppkg.Types())
	factsDecoder := facts.NewDecoderFunc(ppkg.Types(), func(path string) *types.Package {
		// Note: Decode is called concurrently, and thus so is this function.

		// Does the fact relate to a package reachable through imports?
		if !an.ph.reachable.MayContain(path) {
			return nil
		}

		return pkgLookup(path)
	})

	var typeErrors []types.Error
filterErrors:
	for _, typeError := range ppkg.TypeErrors() {
		// Suppress type errors in files with parse errors
		// as parser recovery can be quite lossy (#59888).
		for _, p := range ppkg.CompiledGoFiles() {
			if p.ParseErr != nil && astutil.NodeContainsPos(p.File, typeError.Pos) {
				continue filterErrors
			}
		}
		typeErrors = append(typeErrors, typeError)
	}

	for _, vdep := range an.succs {
		if !vdep.compiles {
			compiles = false // transitive error
			if os.Getenv("PLAID_ADR11_TRACE") == "1" {
				adr11TraceCompilesFalse(an.ph.mp.ID, nil, nil, nil, fmt.Sprintf("transitive-from=%s", vdep.ph.mp.ID))
			}
		}
	}

	return &analysisPackage{
		pkg:          ppkg,
		files:        files,
		typeErrors:   typeErrors,
		compiles:     compiles,
		factsDecoder: factsDecoder,
	}, nil
}

// importErr records a single getImportPackage failure for compiles=false tracing.
type importErr struct {
	depID metadata.PackageID
	err   error
}

// Instrumentation: classifies and counts compiles=false signals.
// Output is dumped at the end of Snapshot.Analyze (alongside the existing
// Lever J trace). Gated by PLAID_ADR11_TRACE=1.
var (
	adr11Mu      sync.Mutex
	adr11Buckets = make(map[string]int)
)

func adr11TraceImportFailures(parentID metadata.PackageID, errs []importErr) {
	adr11Mu.Lock()
	defer adr11Mu.Unlock()
	for _, e := range errs {
		msg := e.err.Error()
		if len(msg) > 160 {
			msg = msg[:160]
		}
		key := fmt.Sprintf("import-failure | parent=%s | dep=%s | msg=%s", parentID, e.depID, msg)
		adr11Buckets[key]++
	}
}

func adr11TraceCompilesFalse(pkgID metadata.PackageID, mpErrs []packages.Error, typeErrs []types.Error, _ []error, note string) {
	adr11Mu.Lock()
	defer adr11Mu.Unlock()
	if note != "" {
		key := fmt.Sprintf("note=%s | pkg=%s", note, pkgID)
		adr11Buckets[key]++
		return
	}
	if len(mpErrs) > 0 {
		for _, e := range mpErrs {
			msg := e.Msg
			if len(msg) > 120 {
				msg = msg[:120]
			}
			key := fmt.Sprintf("mp-error | kind=%v | pkg=%s | msg=%s", e.Kind, pkgID, msg)
			adr11Buckets[key]++
		}
	}
	if len(typeErrs) > 0 {
		for _, te := range typeErrs {
			msg := te.Msg
			if len(msg) > 160 {
				msg = msg[:160]
			}
			// Strip the file:line:col position prefix so identical messages
			// across packages cluster correctly. The Msg field is just text.
			key := fmt.Sprintf("type-error | pkg=%s | msg=%s", pkgID, msg)
			adr11Buckets[key]++
		}
	}
}

func adr11DumpTrace() {
	adr11Mu.Lock()
	defer adr11Mu.Unlock()
	if len(adr11Buckets) == 0 {
		return
	}
	fmt.Fprintln(os.Stderr, "[adr11-trace] compiles=false signals by (kind, pkg, msg-prefix):")
	keys := make([]string, 0, len(adr11Buckets))
	for k := range adr11Buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(os.Stderr, "  %d  %s\n", adr11Buckets[k], k)
	}
	// Reset for the next call window (warm/cascade scenarios reuse the same process).
	adr11Buckets = make(map[string]int)
}

// typesLookup implements a concurrency safe depth-first traversal searching
// imports of pkg for a given package path.
func typesLookup(pkg *types.Package) func(string) *types.Package {
	var (
		mu sync.Mutex // guards impMap and pending

		// impMap memoizes the lookup of package paths.
		impMap = map[string]*types.Package{
			pkg.Path(): pkg,
		}
		// pending is a FIFO queue of packages that have yet to have their
		// dependencies fully scanned.
		// Invariant: all entries in pending are already mapped in impMap.
		pending = []*types.Package{pkg}
	)

	// search scans children the next package in pending, looking for pkgPath.
	search := func(pkgPath string) (sought *types.Package, numPending int) {
		mu.Lock()
		defer mu.Unlock()

		if p, ok := impMap[pkgPath]; ok {
			return p, len(pending)
		}

		if len(pending) == 0 {
			return nil, 0
		}

		pkg := pending[0]
		pending = pending[1:]
		for _, dep := range pkg.Imports() {
			depPath := dep.Path()
			if _, ok := impMap[depPath]; ok {
				continue
			}
			impMap[depPath] = dep

			pending = append(pending, dep)
			if depPath == pkgPath {
				// Don't return early; finish processing pkg's deps.
				sought = dep
			}
		}
		return sought, len(pending)
	}

	return func(pkgPath string) *types.Package {
		p, np := (*types.Package)(nil), 1
		for p == nil && np > 0 {
			p, np = search(pkgPath)
		}
		return p
	}
}

// analysisPackage contains information about a package, including
// syntax trees, used transiently during its type-checking and analysis.
type analysisPackage struct {
	pkg          *Package
	files        []*ast.File   // same as parsed[i].File
	typeErrors   []types.Error // filtered type checker errors
	compiles     bool          // package is transitively free of list/parse/type errors
	factsDecoder *facts.Decoder
}

// An action represents one unit of analysis work: the application of
// one analysis to one package. Actions form a DAG, both within a
// package (as different analyzers are applied, either in sequence or
// parallel), and across packages (as dependencies are analyzed).
type action struct {
	once       sync.Once
	a          *analysis.Analyzer
	fsource    file.Source // Snapshot.ReadFile, for Pass.ReadFile
	stableName string      // cross-process stable name of analyzer
	pkg        *analysisPackage
	hdeps      []*action                   // horizontal dependencies
	vdeps      map[PackageID]*analysisNode // vertical dependencies

	// an is a back-reference to the owning analysisNode, used by the L1
	// fast path in exec to access the batch's l1 handle, the package's
	// reachability hash (ph.key), and the dep-summary facts for digest
	// computation. Always set by mkAction. See W6.
	an *analysisNode

	// isPrerequisiteOfEnabled is true when at least one other action in
	// this analysisNode's action graph lists this action's analyzer in
	// its Requires. Such "prerequisite" actions MUST bypass the L1
	// fast path: the consumer reads its prerequisites' Result values
	// from pass.ResultOf, and the L1 fast path returns result=nil. If
	// the prerequisite hits L1 while the consumer misses, the consumer
	// dereferences a nil ResultOf entry and crashes. The marker is set
	// at DAG construction time in analysisNode.run; the action.exec
	// L1 fast-path gate consults it.
	isPrerequisiteOfEnabled bool

	// results of action.exec():
	result  any // result of Run function, of type a.ResultType
	summary *actionSummary
	err     error
}

func (act *action) String() string {
	return fmt.Sprintf("%s@%s", act.a.Name, act.pkg.pkg.metadata.ID)
}

// perActionFanoutEnabled gates the intra-package action fan-out. Default:
// enabled (matches the earlier behavior of one goroutine per action).
// Set `PLAID_PER_ACTION_FANOUT=0` to switch to sequential intra-package
// execution: hdeps still recursively fan out across packages because they
// go through execActions, but inside a single package's action graph the
// actions run on the calling goroutine in declaration order.
//
// On cold (RSS-saturated) runs this trades a small slice of intra-package
// parallelism for a tighter peak working-set: at most one analyzer body
// per package is mid-execution at a time, so the GC mark phase sees less
// live transient state. Projected ~50-90s cold wall savings
// on the c1 controller scope. On warm runs (where every action returns
// from the L1 fast path in microseconds) the fanout is cheap; leaving the
// default at enabled preserves the warm-run shape.
//
// The gate is read once at process start; changes mid-run are not picked
// up.
var perActionFanoutEnabled = func() bool {
	if v, ok := os.LookupEnv("PLAID_PER_ACTION_FANOUT"); ok && v == "0" {
		return false
	}
	return true
}()

// SetPerActionFanoutForTest temporarily overrides the fan-out
// gate; returns a restore function callers must defer. Test-only path,
// kept on the package surface so external tests can drive the serial
// branch without spawning a subprocess. Returning the restore from a
// var-style setter (rather than a t.Setenv-flavored sleight) makes the
// test write happens-before the goroutine read execActions performs.
func SetPerActionFanoutForTest(enabled bool) func() {
	prev := perActionFanoutEnabled
	perActionFanoutEnabled = enabled
	return func() { perActionFanoutEnabled = prev }
}

// execActions executes a set of action graph nodes in parallel.
// Postcondition: each action.summary is set, even in case of error.
func execActions(ctx context.Context, actions []*action) {
	if !perActionFanoutEnabled {
		execActionsSerial(ctx, actions)
		return
	}
	var wg sync.WaitGroup
	for _, act := range actions {
		wg.Go(func() {
			act.once.Do(func() {
				execActions(ctx, act.hdeps) // analyze "horizontal" dependencies
				act.result, act.summary, act.err = act.exec(ctx)
				if act.err != nil {
					act.summary = &actionSummary{Err: act.err.Error()}
					// TODO(adonovan): suppress logging. But
					// shouldn't the root error's causal chain
					// include this information?
					if false { // debugging
						log.Printf("act.exec(%v) failed: %v", act, act.err)
					}
				}
			})
			if act.summary == nil {
				panic("nil action.summary (#60551)")
			}
		})
	}
	wg.Wait()
}

// execActionsSerial is the cold-mode-friendly variant of
// execActions: it runs each action's body on the calling goroutine in
// the declared order, dropping the per-action goroutine fan-out that
// pushes peak transient working set up. hdeps remain recursive so the
// horizontal-dependency contract is preserved (an analyzer's prereq
// completes before the consumer's body). The vertical (cross-package)
// parallelism still happens at the analysisGate level — sibling
// packages run concurrently in the engine's enqueue loop.
//
// Each action's body is still wrapped in act.once.Do so a future caller
// that mixes the serial path with another execActions invocation on the
// same action graph (not currently a thing, but defensive) still gets
// exactly-once semantics.
//
// Postcondition matches execActions: every action.summary is non-nil,
// either via successful exec or via the synthetic Err summary built
// from act.err. The nil-summary panic is replicated so a regression that
// breaks the postcondition is loud in either mode.
func execActionsSerial(ctx context.Context, actions []*action) {
	for _, act := range actions {
		act.once.Do(func() {
			execActionsSerial(ctx, act.hdeps)
			act.result, act.summary, act.err = act.exec(ctx)
			if act.err != nil {
				act.summary = &actionSummary{Err: act.err.Error()}
			}
		})
		if act.summary == nil {
			panic("nil action.summary (#60551)")
		}
	}
}

// exec defines the execution of a single action.
// It returns the (ephemeral) result of the analyzer's Run function,
// along with its (serializable) facts and diagnostics.
// Or it returns an error if the analyzer did not run to
// completion and deliver a valid result.
//
// W6 fast path: if the batch has an L1 cache attached and the
// content-addressed L1 entry for (analyzer, package) is present, exec
// returns the cached summary directly without running the analyzer
// body. The hit covers analyzers that produce diagnostics, facts, or
// both; the analyzer's Result value is not cached and will be nil on
// hit. Downstream analyzers that consume an upstream Result (via
// a.Requires) will therefore re-run the upstream analyzer on miss —
// which the hdeps execution already triggered before exec is called.
// See l1.go.
func (act *action) exec(ctx context.Context) (any, *actionSummary, error) {
	// L1 fast path — only meaningful when:
	//
	//   1. The analyzer is NOT consumed as a prerequisite by another
	//      action in this graph. L1 hits return result=nil, so a
	//      consumer's pass.ResultOf[prereq] would be nil. The
	//      isPrerequisiteOfEnabled marker, set at DAG construction
	//      time, identifies such prerequisite actions; they bypass
	//      L1 and always run their Run body. The proper fix (caching
	//      Result via AnalyzerDescriptor.ConsumedAsResult) is W7
	//      work.
	//   2. All vdep summaries required for the action ID derivation are
	//      present. vdeps run before this analysisNode is enqueued, so
	//      their actions[stableName].FactsHash is populated by the time
	//      exec() is called.
	if !act.isPrerequisiteOfEnabled {
		if result, summary, ok := act.tryL1Lookup(ctx); ok {
			return result, summary, nil
		}
	}

	analyzer := act.a
	apkg := act.pkg

	hasFacts := len(analyzer.FactTypes) > 0

	// Report an error if any action dependency (vertical or horizontal) failed.
	// To avoid long error messages describing chains of failure,
	// we return the dependencies' error' unadorned.
	if hasFacts {
		// TODO(adonovan): use deterministic order.
		for _, vdep := range act.vdeps {
			// vdep.actions[act.stableName] may be nil: the map can be
			// freed by the D-125 aggressive-release path once its last
			// consumer decrements, and L0-override / cache-shortcut
			// summaries need not carry an entry for every facty analyzer.
			// A nil summary carries no error to propagate, so skip it —
			// mirroring the guarded read in the fact-decode path below.
			summ := vdep.actions[act.stableName]
			if summ == nil {
				continue
			}
			if summ.Err != "" {
				return nil, nil, errors.New(summ.Err)
			}
		}
	}
	for _, dep := range act.hdeps {
		if dep.err != nil {
			return nil, nil, dep.err
		}
	}
	// Inv: all action dependencies succeeded.

	// Were there list/parse/type errors that might prevent analysis?
	if !apkg.compiles && !analyzer.RunDespiteErrors {
		return nil, nil, fmt.Errorf("skipping analysis %q because package %q does not compile", analyzer.Name, apkg.pkg.metadata.ID)
	}
	// Inv: package is well-formed enough to proceed with analysis.

	// W9 scheduler admit gate: when a scheduler is attached, block
	// here until its budget admits this action. The default
	// (no scheduler attached) is a no-op. The gate fires AFTER
	// the L1 fast path so cache hits aren't gratuitously
	// serialized; it fires BEFORE the analyzer body so the gate
	// actually throttles the expensive work.
	//
	// Admission errors abort the action: the analyzer body MUST NOT
	// run when Acquire returns err. Surfacing the error here pre-
	// serves the contract that future refusal policies (oversized
	// action, deadline-based admission, RSS exhaustion) actually
	// throttle work rather than silently bypass the gate.
	release, sa, acqErr := acquireSchedulerSlot(ctx, act)
	if acqErr != nil {
		return nil, nil, acqErr
	}
	if release != nil {
		defer release()
	}

	// RSS observation: when a scheduler is attached, the production
	// estimator (DefaultEstimator) feeds its sliding-window median
	// from Observe samples. The W10 indirection routes the choice
	// of observation source through ActionScheduler.Sampler() so
	// the harness can A/B alternative sources (VmHWM, runtime/metrics)
	// without touching this hot path. The W9 inlined HeapAlloc delta
	// is still available behind the PLAID_RSS_OBSERVATION env var.
	// A nil sampler (or nil scheduler) skips observation entirely.
	// W9 inlined the source; W10 routed it through the sampler.
	var obsSampler ObservationSampler
	var obsBefore any
	if release != nil {
		if s := act.an.batch.scheduler; s != nil {
			obsSampler = s.Sampler()
			if obsSampler != nil {
				obsBefore = obsSampler.NewSample()
			}
		}
	}
	defer func() {
		if release == nil || obsSampler == nil {
			return
		}
		s := act.an.batch.scheduler
		if s == nil {
			return
		}
		s.Observe(sa, obsSampler.Delta(obsBefore))
	}()

	if false { // debugging
		log.Println("action.exec", act)
	}

	// Gather analysis Result values from horizontal dependencies.
	inputs := make(map[*analysis.Analyzer]any)
	for _, dep := range act.hdeps {
		inputs[dep.a] = dep.result
	}

	// TODO(adonovan): opt: facts.Set works but it may be more
	// efficient to fork and tailor it to our precise needs.
	//
	// We've already sharded the fact encoding by action
	// so that it can be done in parallel.
	// We could eliminate locking.
	// We could also dovetail more closely with the export data
	// decoder to obtain a more compact representation of
	// packages and objects (e.g. its internal IDs, instead
	// of PkgPaths and objectpaths.)
	// More importantly, we should avoid re-export of
	// facts that related to objects that are discarded
	// by "deep" export data. Better still, use a "shallow" approach.

	// Read and decode analysis facts for each direct import.
	// Block any concurrent gcimporter Scope.Insert while we walk Scopes via
	// objectpath.Object. The Decoder path mirrors facts.Set.Encode's reader
	// side — both are gated by typesyncmu so importers see a consistent view.
	factsRTok := typesyncmu.RLock()
	factset, err := apkg.factsDecoder.DecodeFromGobFacts(func(pkgPath string) (facts.GobFacts, error) {
		if !hasFacts {
			return facts.GobFacts{}, nil // analyzer doesn't use facts, so no vdeps
		}

		// Package.Imports() may contain a fake "C" package. Ignore it.
		if pkgPath == "C" {
			return facts.GobFacts{}, nil
		}

		id, ok := apkg.pkg.metadata.DepsByPkgPath[PackagePath(pkgPath)]
		if !ok {
			// This may mean imp was synthesized by the type
			// checker because it failed to import it for any reason
			// (e.g. bug processing export data; metadata ignoring
			// a cycle-forming import).
			// In that case, the fake package's imp.Path
			// is set to the failed importPath (and thus
			// it may lack a "vendor/" prefix).
			//
			// For now, silently ignore it on the assumption
			// that the error is already reported elsewhere.
			// return nil, fmt.Errorf("missing metadata")
			return facts.GobFacts{}, nil
		}

		vdep := act.vdeps[id]
		if vdep == nil {
			return facts.GobFacts{}, bug.Errorf("internal error in %s: missing vdep for id=%s", apkg.pkg.Types().Path(), id)
		}

		summ := vdep.actions[act.stableName]
		if summ == nil {
			return facts.GobFacts{}, nil
		}
		// Layer B: in-process producer already populated
		// gobFacts; skip gob.Decode. Else fall back to Layer A memo
		// over the bytes (cross-process summaries: L1 / L0 override).
		if summ.gobFacts.Len() > 0 {
			return summ.gobFacts, nil
		}
		return vdep.decodeFactsFor(act.stableName, summ.Facts)
	})
	typesyncmu.RUnlock(factsRTok)
	if err != nil {
		return nil, nil, fmt.Errorf("internal error decoding analysis facts: %w", err)
	}

	// TODO(adonovan): make Export*Fact panic rather than discarding
	// undeclared fact types, so that we discover bugs in analyzers.
	factFilter := make(map[reflect.Type]bool)
	for _, f := range analyzer.FactTypes {
		factFilter[reflect.TypeOf(f)] = true
	}

	// Now run the (pkg, analyzer) action.
	var diagnostics []gobDiagnostic

	pass := &analysis.Pass{
		Analyzer:     analyzer,
		Fset:         apkg.pkg.FileSet(),
		Files:        apkg.files,
		OtherFiles:   nil, // since gopls doesn't handle non-Go (e.g. asm) files
		IgnoredFiles: nil, // zero-config gopls should analyze these files in another view
		Pkg:          apkg.pkg.Types(),
		TypesInfo:    apkg.pkg.TypesInfo(),
		TypesSizes:   apkg.pkg.TypesSizes(),
		TypeErrors:   apkg.typeErrors,
		Module:       analysisModuleFromPackagesModule(apkg.pkg.metadata.Module),
		ResultOf:     inputs,
		Report: func(d analysis.Diagnostic) {
			// Assert that SuggestedFixes are well formed.
			//
			// ValidateFixes allows a fix.End to be slightly beyond
			// EOF to avoid spurious assertions when reporting
			// fixes as the end of truncated files; see #71659.
			if err := driverutil.ValidateFixes(apkg.pkg.FileSet(), analyzer, d.SuggestedFixes); err != nil {
				bug.Reportf("invalid SuggestedFixes: %v", err)
				d.SuggestedFixes = nil
			}
			diagnostic, err := toGobDiagnostic(apkg.pkg, analyzer, d)
			if err != nil {
				// Don't bug.Report here: these errors all originate in
				// posToLocation, and we can more accurately discriminate
				// severe errors from benign ones in that function.
				event.Error(ctx, fmt.Sprintf("internal error converting diagnostic from analyzer %q", analyzer.Name), err)
				return
			}
			diagnostics = append(diagnostics, diagnostic)
		},
		ImportObjectFact:  factset.ImportObjectFact,
		ExportObjectFact:  factset.ExportObjectFact,
		ImportPackageFact: factset.ImportPackageFact,
		ExportPackageFact: factset.ExportPackageFact,
		AllObjectFacts:    func() []analysis.ObjectFact { return factset.AllObjectFacts(factFilter) },
		AllPackageFacts:   func() []analysis.PackageFact { return factset.AllPackageFacts(factFilter) },
	}

	pass.ReadFile = func(filename string) ([]byte, error) {
		// Read file from snapshot, to ensure reads are consistent.
		//
		// TODO(adonovan): make the dependency analysis sound by
		// incorporating these additional files into the analysis
		// hash. This requires either (a) preemptively reading and
		// hashing a potentially large number of mostly irrelevant
		// files; or (b) some kind of dynamic dependency discovery
		// system like used in Bazel for C++ headers. Neither entices.
		if err := driverutil.CheckReadable(pass, filename); err != nil {
			return nil, err
		}
		h, err := act.fsource.ReadFile(ctx, protocol.URIFromPath(filename))
		if err != nil {
			return nil, err
		}
		content, err := h.Content()
		if err != nil {
			return nil, err // file doesn't exist
		}
		return slices.Clone(content), nil // follow ownership of os.ReadFile
	}

	// L3 IRManager pin: when the descriptor opts in via NeedsIR=true,
	// record an in-flight pin on the package for the duration of the
	// Run body. The default Cache attaches no IRManager (nil), which
	// the helper treats as a noop — pin/release is zero-cost in the
	// W7 baseline. The W9 scheduler attaches a real IRManager and
	// keys RSS-aware free-after-fanin off the pin/release stream.
	irPin := pinIRForAction(act)
	defer irPin.Release()

	// Recover from panics (only) within the analyzer logic.
	// (Use an anonymous function to limit the recover scope.)
	var result any
	func() {
		start := time.Now()
		defer func() {
			if r := recover(); r != nil {
				// An Analyzer panicked, likely due to a bug.
				//
				// In general we want to discover and fix such panics quickly,
				// so we don't suppress them, but some bugs in third-party
				// analyzers cannot be quickly fixed, so we use an allowlist
				// to suppress panics.
				const strict = true
				if strict && bug.PanicOnBugs &&
					analyzer.Name != "buildir" { // see https://github.com/dominikh/go-tools/issues/1343
					// Uncomment this when debugging suspected failures
					// in the driver, not the analyzer.
					if false {
						debug.SetTraceback("all") // show all goroutines
					}
					panic(r)
				} else {
					// In production, suppress the panic and press on.
					err = fmt.Errorf("analysis %s for package %s panicked: %v", analyzer.Name, pass.Pkg.Path(), r)
				}
			}

			// Accumulate running time + call count for each checker.
			// The call count is the run-count instrument: per-package
			// dedupe inside analysisNode.run already makes (analyzer ×
			// analysisNode) a 1:1 relation, but the count exposes that
			// invariant for tests that pin it (e.g. assertions that
			// buildir runs exactly once per package even when many
			// SA*/QF analyzers depend on it).
			analyzerRunTimesMu.Lock()
			analyzerRunTimes[analyzer] += time.Since(start)
			analyzerRunCounts[analyzer]++
			analyzerRunTimesMu.Unlock()
		}()

		// Block any concurrent gcimporter Scope.Insert while the analyzer reads
		// shared *types.Package scopes. Many analyzers (e.g. staticcheck's
		// buildir → ir.Program.CreatePackage → Scope.Names) walk transitive
		// imports unguarded; the only way to make this safe without forking
		// every analyzer is to gate Run as the reader side of the typesyncmu
		// RWMutex.
		//
		// M2: acquire the buildir in-flight slot BEFORE
		// typesyncmu.RLock so a blocked buildir never holds the RLock.
		//
		// M1: when PLAID_SHARED_BUILDIR=1, route buildir
		// through the typeCheckBatch-scoped workspaceBuildir so all
		// siblings importing the same upstream share one *ir.Program
		// instead of each rebuilding. The cache is allocated lazily on
		// first dispatch; default (flag-off) behavior is unchanged —
		// pass.Analyzer.Run runs as before.
		if analyzer.Name == "buildir" {
			defer acquireBuildirSlot()()
		}
		runRTok := typesyncmu.RLock()
		if analyzer.Name == "buildir" && sharedBuildirEnabledFlag {
			wb := getOrCreateWorkspaceBuildir(act.an.batch)
			result, err = wb.runShared(pass)
		} else {
			result, err = pass.Analyzer.Run(pass)
		}
		typesyncmu.RUnlock(runRTok)
	}()
	if err != nil {
		return nil, nil, err
	}

	if got, want := reflect.TypeOf(result), pass.Analyzer.ResultType; got != want {
		return nil, nil, bug.Errorf(
			"internal error: on package %s, analyzer %s returned a result of type %v, but declared ResultType %v",
			pass.Pkg.Path(), pass.Analyzer, got, want)
	}

	// Disallow Export*Fact calls after Run.
	// (A panic means the Analyzer is abusing concurrency.)
	pass.ExportObjectFact = func(obj types.Object, fact analysis.Fact) {
		panic(fmt.Sprintf("%v: Pass.ExportObjectFact(%s, %T) called after Run", act, obj, fact))
	}
	pass.ExportPackageFact = func(fact analysis.Fact) {
		panic(fmt.Sprintf("%v: Pass.ExportPackageFact(%T) called after Run", act, fact))
	}

	// Layer B: producer surfaces the pre-gob-decoded slice for
	// in-process consumers. Bytes still feed FactsHash and L1/L0.
	factsdata, gobFacts := factset.EncodeAndGobFacts()
	summary := &actionSummary{
		Diagnostics: diagnostics,
		Facts:       factsdata,
		FactsHash:   file.HashOf(factsdata),
		gobFacts:    gobFacts,
	}
	// L1 store — opportunistic, errors are recorded and swallowed.
	// Run inline (cheap: file write of a small blob) so that subsequent
	// imports of this package in the same batch can hit L1 immediately.
	// The result is passed through so analyzers whose descriptor opts
	// in to Result caching round-trip their Run output on warm hits.
	act.l1Store(summary, result)
	return result, summary, nil
}

var (
	analyzerRunTimesMu sync.Mutex
	analyzerRunTimes   = make(map[*analysis.Analyzer]time.Duration)
	analyzerRunCounts  = make(map[*analysis.Analyzer]int64)
)

type LabelDuration struct {
	Label    string
	Duration time.Duration
}

// AnalyzerRunTimes returns the accumulated time spent in each Analyzer's
// Run function since process start, in descending order.
func AnalyzerRunTimes() []LabelDuration {
	analyzerRunTimesMu.Lock()
	defer analyzerRunTimesMu.Unlock()

	slice := make([]LabelDuration, 0, len(analyzerRunTimes))
	for a, t := range analyzerRunTimes {
		slice = append(slice, LabelDuration{Label: a.Name, Duration: t})
	}
	sort.Slice(slice, func(i, j int) bool {
		return slice[i].Duration > slice[j].Duration
	})
	return slice
}

// AnalyzerRunCount returns the cumulative count of times the named
// analyzer's Run body has executed since process start (or since the
// last ResetAnalyzerRunCounts). Matches by analyzer.Name so callers
// don't need a pointer to the *analysis.Analyzer.
//
// Used by the run-count probe tests to pin the contract "buildir's body
// runs exactly once per package even when N SA*/QF analyzers depend on
// it": when caller iterates over N packages with the full SA* set
// enabled, AnalyzerRunCount("buildir") must equal N. Per-package
// dedupe inside analysisNode.run.actions (mkAction) is what makes this
// invariant hold without an explicit IR cache.
func AnalyzerRunCount(name string) int64 {
	analyzerRunTimesMu.Lock()
	defer analyzerRunTimesMu.Unlock()
	for a, n := range analyzerRunCounts {
		if a.Name == name {
			return n
		}
	}
	return 0
}

// AnalyzerRunCounts returns a snapshot of every analyzer's cumulative
// run count, keyed by analyzer.Name. Snapshot only — the returned map
// is owned by the caller.
func AnalyzerRunCounts() map[string]int64 {
	analyzerRunTimesMu.Lock()
	defer analyzerRunTimesMu.Unlock()
	out := make(map[string]int64, len(analyzerRunCounts))
	for a, n := range analyzerRunCounts {
		out[a.Name] = n
	}
	return out
}

// ResetAnalyzerRunCounts zeroes the per-analyzer call counters. Tests
// call this between iterations so a count assertion is scoped to one
// engine.Run.
func ResetAnalyzerRunCounts() {
	analyzerRunTimesMu.Lock()
	defer analyzerRunTimesMu.Unlock()
	for k := range analyzerRunCounts {
		delete(analyzerRunCounts, k)
	}
}

// requiredAnalyzers returns the transitive closure of required analyzers in preorder.
func requiredAnalyzers(analyzers []*analysis.Analyzer) []*analysis.Analyzer {
	var result []*analysis.Analyzer
	seen := make(map[*analysis.Analyzer]bool)
	var visitAll func([]*analysis.Analyzer)
	visitAll = func(analyzers []*analysis.Analyzer) {
		for _, a := range analyzers {
			if !seen[a] {
				seen[a] = true
				result = append(result, a)
				visitAll(a.Requires)
			}
		}
	}
	visitAll(analyzers)
	return result
}

var analyzeSummaryCodec = frob.CodecFor[*analyzeSummary]()

// -- data types for serialization of analysis.Diagnostic and golang.Diagnostic --

// (The name says gob but we use frob.)
var diagnosticsCodec = frob.CodecFor[[]gobDiagnostic]()

type gobDiagnostic struct {
	Location       protocol.Location
	Severity       protocol.DiagnosticSeverity
	Code           string
	CodeHref       string
	Source         string
	Message        string
	SuggestedFixes []gobSuggestedFix
	Related        []gobRelatedInformation
	Tags           []protocol.DiagnosticTag
}

type gobRelatedInformation struct {
	Location protocol.Location
	Message  string
}

type gobSuggestedFix struct {
	Message    string
	TextEdits  []gobTextEdit
	Command    *gobCommand
	ActionKind protocol.CodeActionKind
}

type gobCommand struct {
	Title     string
	Command   string
	Arguments []json.RawMessage
}

type gobTextEdit struct {
	Location protocol.Location
	NewText  []byte
}

// toGobDiagnostic converts an analysis.Diagnosic to a serializable gobDiagnostic,
// which requires expanding token.Pos positions into protocol.Location form.
func toGobDiagnostic(pkg *Package, a *analysis.Analyzer, diag analysis.Diagnostic) (gobDiagnostic, error) {
	var fixes []gobSuggestedFix
	for _, fix := range diag.SuggestedFixes {
		var gobEdits []gobTextEdit
		for _, textEdit := range fix.TextEdits {
			loc, err := diagnosticPosToLocation(pkg, false, textEdit.Pos, textEdit.End)
			if err != nil {
				return gobDiagnostic{}, fmt.Errorf("in SuggestedFixes: %w", err)
			}
			gobEdits = append(gobEdits, gobTextEdit{
				Location: loc,
				NewText:  textEdit.NewText,
			})
		}
		fixes = append(fixes, gobSuggestedFix{
			Message:   fix.Message,
			TextEdits: gobEdits,
		})
	}

	var related []gobRelatedInformation
	for _, r := range diag.Related {
		// The position of RelatedInformation may be
		// within another (dependency) package.
		const allowDeps = true
		loc, err := diagnosticPosToLocation(pkg, allowDeps, r.Pos, r.End)
		if err != nil {
			return gobDiagnostic{}, fmt.Errorf("in Related: %w", err)
		}
		related = append(related, gobRelatedInformation{
			Location: loc,
			Message:  r.Message,
		})
	}

	loc, err := diagnosticPosToLocation(pkg, false, diag.Pos, diag.End)
	if err != nil {
		return gobDiagnostic{}, err
	}

	// The Code column of VSCode's Problems table renders this
	// information as "Source(Code)" where code is a link to CodeHref.
	// (The code field must be nonempty for anything to appear.)
	diagURL := effectiveURL(a, diag)
	code := "default"
	if diag.Category != "" {
		code = diag.Category
	}

	return gobDiagnostic{
		Location: loc,
		// Severity for analysis diagnostics is dynamic,
		// based on user configuration per analyzer.
		Code:           code,
		CodeHref:       diagURL,
		Source:         a.Name,
		Message:        diag.Message,
		SuggestedFixes: fixes,
		Related:        related,
		// Analysis diagnostics do not contain tags.
	}, nil
}

// diagnosticPosToLocation converts from token.Pos to protocol form, in the
// context of the specified package and, optionally, its dependencies.
func diagnosticPosToLocation(pkg *Package, allowDeps bool, start, end token.Pos) (protocol.Location, error) {
	if end == token.NoPos {
		end = start
	}

	fset := pkg.FileSet()
	tokFile := fset.File(start)

	// Find existing mapper by file name.
	// (Don't require an exact token.File match
	// as the analyzer may have re-parsed the file.)
	var (
		mapper *protocol.Mapper
		fixed  bool
	)
	for _, p := range pkg.CompiledGoFiles() {
		if p.Tok.Name() == tokFile.Name() {
			mapper = p.Mapper
			fixed = p.Fixed() // suppress some assertions after parser recovery
			break
		}
	}
	// TODO(adonovan): search pkg.AsmFiles too; see #71754.
	if mapper != nil {
		// debugging #64547
		fileStart := token.Pos(tokFile.Base())
		fileEnd := fileStart + token.Pos(tokFile.Size())
		if start < fileStart {
			if !fixed {
				bug.Reportf("start < start of file")
			}
			start = fileStart
		}
		if end < start {
			// This can happen if End is zero (#66683)
			// or a small positive displacement from zero
			// due to recursive Node.End() computation.
			// This usually arises from poor parser recovery
			// of an incomplete term at EOF.
			if !fixed {
				bug.Reportf("end < start of file")
			}
			end = fileEnd
		}
		if end > fileEnd+1 {
			if !fixed {
				bug.Reportf("end > end of file + 1")
			}
			end = fileEnd
		}

		return mapper.PosLocation(tokFile, start, end)
	}

	// Inv: the positions are not within this package.

	if allowDeps {
		// Positions in Diagnostic.RelatedInformation may belong to a
		// dependency package. We cannot accurately map them to
		// protocol.Location coordinates without a Mapper for the
		// relevant file, but none exists if the file was loaded from
		// export data, and we have no means (Snapshot) of loading it.
		//
		// So, fall back to approximate conversion to UTF-16:
		// for non-ASCII text, the column numbers may be wrong.
		var (
			startPosn = safetoken.StartPosition(fset, start)
			endPosn   = safetoken.EndPosition(fset, end)
		)
		return protocol.Location{
			URI: protocol.URIFromPath(startPosn.Filename),
			Range: protocol.Range{
				Start: protocol.Position{
					Line:      uint32(startPosn.Line - 1),
					Character: uint32(startPosn.Column - 1),
				},
				End: protocol.Position{
					Line:      uint32(endPosn.Line - 1),
					Character: uint32(endPosn.Column - 1),
				},
			},
		}, nil
	}

	// The start position was not among the package's parsed
	// Go files, indicating that the analyzer added new files
	// to the FileSet.
	//
	// For example, the cgocall analyzer re-parses and
	// type-checks some of the files in a special environment;
	// and asmdecl and other low-level runtime analyzers call
	// ReadFile to parse non-Go files.
	// (This is a supported feature, documented at go/analysis.)
	//
	// In principle these files could be:
	//
	// - OtherFiles (non-Go files such as asm).
	//   However, we set Pass.OtherFiles=[] because
	//   gopls won't service "diagnose" requests
	//   for non-Go files, so there's no point
	//   reporting diagnostics in them.
	//
	// - IgnoredFiles (files tagged for other configs).
	//   However, we set Pass.IgnoredFiles=[] because,
	//   in most cases, zero-config gopls should create
	//   another view that covers these files.
	//
	// - Referents of //line directives, as in cgo packages.
	//   The file names in this case are not known a priori.
	//   gopls generally tries to avoid honoring line directives,
	//   but analyzers such as cgocall may honor them.
	//
	// In short, it's unclear how this can be reached
	// other than due to an analyzer bug.

	return protocol.Location{}, bug.Errorf("diagnostic location is not among files of package: %s", tokFile.Name())
}

// effectiveURL computes the effective URL of diag,
// using the algorithm specified at Diagnostic.URL.
func effectiveURL(a *analysis.Analyzer, diag analysis.Diagnostic) string {
	u := diag.URL
	if u == "" && diag.Category != "" {
		u = "#" + diag.Category
	}
	if base, err := urlpkg.Parse(a.URL); err == nil {
		if rel, err := urlpkg.Parse(u); err == nil {
			u = base.ResolveReference(rel).String()
		}
	}
	return u
}

// stableName returns a name for the analyzer that is unique and
// stable across address spaces.
//
// Analyzer names are not unique. For example, gopls includes
// both x/tools/passes/nilness and staticcheck/nilness.
// For serialization, we must assign each analyzer a unique identifier
// that two gopls processes accessing the cache can agree on.
func stableName(a *analysis.Analyzer) string {
	// Incorporate the file and line of the analyzer's Run function.
	addr := reflect.ValueOf(a.Run).Pointer()
	fn := runtime.FuncForPC(addr)
	file, line := fn.FileLine(addr)

	// It is tempting to use just a.Name as the stable name when
	// it is unique, but making them always differ helps avoid
	// name/stablename confusion.
	return fmt.Sprintf("%s(%s:%d)", a.Name, filepath.Base(file), line)
}

// analysisModuleFromPackagesModule converts a packages.Module to an analysis.Module.
func analysisModuleFromPackagesModule(mod *packages.Module) *analysis.Module {
	if mod == nil {
		return nil
	}

	var modErr *analysis.ModuleError
	if mod.Error != nil {
		modErr = &analysis.ModuleError{
			Err: mod.Error.Err,
		}
	}

	return &analysis.Module{
		Path:      mod.Path,
		Version:   mod.Version,
		Replace:   analysisModuleFromPackagesModule(mod.Replace),
		Time:      mod.Time,
		Main:      mod.Main,
		Indirect:  mod.Indirect,
		Dir:       mod.Dir,
		GoMod:     mod.GoMod,
		GoVersion: mod.GoVersion,
		Error:     modErr,
	}
}
