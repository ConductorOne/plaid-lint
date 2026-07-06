// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"os"
	"sync"

	"golang.org/x/tools/go/analysis"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
)

// PLAID_L0_DEP_OVERRIDE is the env-var escape hatch for the
// dep-override fast path. Setting it to "0" reverts to the prior
// behavior (every L0 miss runs Analyze across the full dep closure,
// invoking buildir per dep). Default is enabled.
const envDepOverride = "PLAID_L0_DEP_OVERRIDE"

// depOverrideEnabled reports whether the dep-override fast path is on.
// Reads the env var on every call so tests can flip it per-run.
func depOverrideEnabled() bool {
	return os.Getenv(envDepOverride) != "0"
}

// installDepOverrides walks the dep closure of pkgsToAnalyze, computes
// the L0 key for every dep, looks each up in the L0 cache, and
// installs an override map on inner so analysisNode.runCached can
// short-circuit on cache hits. Returns the per-dep keys (used by the
// post-analysis writer) and the number of overrides installed (for
// the metrics trace + tests).
//
// pkgsToAnalyze is the (workspace-package) set that already missed
// L0 — every dep of these packages is a candidate. Workspace packages
// that hit L0 are already excluded from Analyze upstream, so they're
// not considered here.
//
// On any L0 read error (or a missing entry), the dep falls through to
// full analysis; the override is a strict accelerator and never
// suppresses correct work.
func installDepOverrides(
	ctx context.Context,
	in RunInput,
	plan *runPlan,
	inner *cache.Snapshot,
	pkgsToAnalyze map[metadata.PackageID]*metadata.Package,
	lc *l0Context,
) (
	depKeys map[metadata.PackageID]clcache.ActionID,
	hits int,
) {
	if !depOverrideEnabled() || in.L0 == nil || lc == nil || len(pkgsToAnalyze) == 0 {
		return nil, 0
	}

	// Walk the transitive dep closure of pkgsToAnalyze. Exclude the
	// workspace packages themselves — those go through Analyze and
	// will write their own L0 entries.
	deps := collectDepClosure(lc, pkgsToAnalyze)
	if len(deps) == 0 {
		return nil, 0
	}

	overrides := make(map[metadata.PackageID]cache.L0DepEntry, len(deps))
	depKeys = make(map[metadata.PackageID]clcache.ActionID, len(deps))

	// Resolve the analyzer pointer table once. Every L0 entry stores
	// per-analyzer facts keyed by stableName; we need to map back to
	// *analysis.Analyzer to satisfy the cache.L0DepEntry contract.
	analyzersBySN := analyzersByStableName(plan)

	for id, mp := range deps {
		key, ok := lc.keyFor(ctx, mp)
		if !ok {
			continue
		}
		depKeys[id] = key
		entry, err := in.L0.Get(key)
		if err != nil || entry == nil {
			continue
		}
		// Translate the gob-decoded Entry.Actions (string-keyed) into
		// the cache-package's *Analyzer-keyed L0DepEntry. Drop entries
		// whose stableName isn't in our current analyzer set: a
		// missing key would feed a nil *Analyzer into the cache, and
		// downstream consumers wouldn't find the analyzer anyway.
		acts := make(map[*analysis.Analyzer]cache.L0ActionData, len(entry.Actions))
		for sn, af := range entry.Actions {
			a := analyzersBySN[sn]
			if a == nil {
				continue
			}
			acts[a] = cache.L0ActionData{
				Facts:     af.Facts,
				FactsHash: af.FactsHash,
				Err:       af.Err,
			}
		}
		overrides[id] = cache.L0DepEntry{
			Compiles: entry.Compiles,
			Actions:  acts,
		}
		hits++
	}

	if hits > 0 {
		inner.SetL0DepOverrides(overrides)
	}
	return depKeys, hits
}

// collectDepClosure returns every PackageID transitively reachable
// from roots' DepsByPkgPath, excluding the roots themselves. Uses the
// l0Context's pre-built metadata graph so we don't re-walk Snapshot
// metadata for each root.
func collectDepClosure(lc *l0Context, roots map[metadata.PackageID]*metadata.Package) map[metadata.PackageID]*metadata.Package {
	out := make(map[metadata.PackageID]*metadata.Package, len(lc.graph))
	visited := make(map[metadata.PackageID]bool, len(lc.graph))
	var visit func(id metadata.PackageID)
	visit = func(id metadata.PackageID) {
		if visited[id] {
			return
		}
		visited[id] = true
		mp := lc.graph[id]
		if mp == nil {
			return
		}
		// Skip self if it's a root — roots are analyzed directly.
		if _, isRoot := roots[id]; !isRoot {
			out[id] = mp
		}
		for _, depID := range mp.DepsByPkgPath {
			visit(depID)
		}
	}
	for id := range roots {
		visit(id)
	}
	return out
}

// analyzersByStableName builds the reverse lookup from stableName ->
// *analysis.Analyzer for every analyzer the engine plans to run. The
// stableName is the gopls cross-process identifier
// (cache.StableNameForAnalyzer) that L0 entries store as their
// Actions map key.
func analyzersByStableName(plan *runPlan) map[string]*analysis.Analyzer {
	out := make(map[string]*analysis.Analyzer, len(plan.analyzers)*2)
	// Walk the transitive Requires closure so facty analyzers
	// (inspect, ctrlflow, ...) that don't appear directly in the
	// plan but DO appear in stored L0 entries are still resolvable.
	seen := make(map[*analysis.Analyzer]bool, len(plan.analyzers)*2)
	var visit func(*analysis.Analyzer)
	visit = func(a *analysis.Analyzer) {
		if a == nil || seen[a] {
			return
		}
		seen[a] = true
		out[cache.StableNameForAnalyzer(a)] = a
		for _, r := range a.Requires {
			visit(r)
		}
	}
	for _, e := range plan.analyzers {
		visit(e.analyzer)
	}
	return out
}

// depOverrideCapture is the engine-side collector for the
// SetNodeAnalyzedCallback stream. It accumulates every analysisNode's
// per-analyzer fact blobs in a thread-safe slice the post-Analyze
// writer drains to produce L0 entries.
type depOverrideCapture struct {
	mu    sync.Mutex
	nodes map[metadata.PackageID]cache.L0NodeData
}

func newDepOverrideCapture() *depOverrideCapture {
	return &depOverrideCapture{nodes: make(map[metadata.PackageID]cache.L0NodeData)}
}

func (c *depOverrideCapture) callback() func(cache.L0NodeData) {
	return func(nd cache.L0NodeData) {
		c.mu.Lock()
		defer c.mu.Unlock()
		// Clone the Actions map per node so the capture is independent
		// of the action graph's lifetime. The underlying Facts []byte
		// is owned by gob.Decode output and not mutated by the driver,
		// so aliasing is safe.
		actions := make(map[*analysis.Analyzer]cache.L0ActionData, len(nd.Actions))
		for k, v := range nd.Actions {
			actions[k] = v
		}
		c.nodes[nd.PackageID] = cache.L0NodeData{
			PackageID: nd.PackageID,
			Compiles:  nd.Compiles,
			Actions:   actions,
		}
	}
}

// writeDepL0Entries writes L0 entries for every dep (non-root) node
// captured during Analyze. Workspace-package (root) entries are
// handled by writeL0Entries — see runInProcess. The diagnostic stream
// stored here is empty: deps don't produce diagnostics for the
// engine's output (only roots do).
//
// Each entry's Actions map is keyed by analyzer stableName. depKeys
// pre-computed the L0 key for every dep we COULD override; we only
// write for deps that were actually analyzed AND whose pre-computed
// key was derivable (some deps in cycles or with missing metadata are
// silently skipped on the read side too).
func writeDepL0Entries(
	cacheL0 *l0.Cache,
	depKeys map[metadata.PackageID]clcache.ActionID,
	capture *depOverrideCapture,
	rootIDs map[metadata.PackageID]struct{},
) int {
	if cacheL0 == nil || capture == nil {
		return 0
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	written := 0
	for id, nd := range capture.nodes {
		if _, isRoot := rootIDs[id]; isRoot {
			continue
		}
		key, ok := depKeys[id]
		if !ok {
			// We didn't pre-compute a key for this dep on the read
			// side (it was a missing-metadata case or the override
			// path was disabled). Re-derive isn't trivial here — the
			// l0Context lives in runInProcess. Skip the write; the
			// next cold run will populate this dep when the override
			// is enabled again. The leaf-edit wall doesn't depend on
			// THIS run's stores.
			continue
		}
		stringActions := make(map[string]l0.ActionFacts, len(nd.Actions))
		for a, data := range nd.Actions {
			stringActions[cache.StableNameForAnalyzer(a)] = l0.ActionFacts{
				Facts:     data.Facts,
				FactsHash: data.FactsHash,
				Err:       data.Err,
			}
		}
		entry := &l0.Entry{
			PackageID:   string(id),
			Diagnostics: nil, // deps don't contribute to output diagnostics
			Actions:     stringActions,
			Compiles:    nd.Compiles,
		}
		if err := cacheL0.Put(key, entry); err == nil {
			written++
		}
	}
	return written
}

// rootCapturedNodeFor returns the captured node data for a root
// package, or false if the callback never saw it (most commonly
// because the package was served from L0 and never went through
// Analyze). Used by the root-write path so root L0 entries get the
// same per-analyzer fact blob as deps — required for cases where a
// workspace package is itself a dep of another workspace package
// (gopls re-uses the analysisNode; the node carries the full enabled
// set's actions).
func (c *depOverrideCapture) rootCapturedNodeFor(id metadata.PackageID) (cache.L0NodeData, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	nd, ok := c.nodes[id]
	return nd, ok
}

// unused alias to keep the output import alive if a future refactor
// drops the only consumer.
var _ = output.Sort
