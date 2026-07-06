// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"sync"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// L0DepEntry is the data the engine plumbs through to the cache package
// per workspace-package-or-dep that hit L0. The cache reconstitutes it
// into a synthetic *analyzeSummary used by analysisNode.runCached to
// short-circuit the action graph (Option A).
//
// The keys of Actions are analyzer stableName values (the same string
// the cache derives via cache.StableNameForAnalyzer or, equivalently,
// reflect-based introspection of the analyzer's Run pointer). The
// engine doesn't have a public way to derive stableName itself, so it
// passes the analyzer pointers through and the cache resolves them.
type L0DepEntry struct {
	// Compiles mirrors analyzeSummary.Compiles. Set true when the L0
	// entry was written from a successful analyze; false otherwise (the
	// engine never writes false today, but the field is here so future
	// "cache failed-analysis sentinels" can extend without another
	// schema bump).
	Compiles bool

	// Actions is keyed by *analysis.Analyzer pointer. Each entry
	// supplies the facts blob, the facts hash, and an error string.
	// Diagnostics are intentionally not duplicated here: the engine
	// surfaces them via the L0 entry's output.Diagnostic stream
	// directly (post-filter, per-package), so the synthetic summary
	// only needs to satisfy the FACT pipeline of downstream consumers.
	// For root packages whose diagnostics are pulled from the action
	// graph (the gopls upstream path), the engine treats root L0 hits
	// as "skip Analyze entirely" — synthetic summaries serve only the
	// dep-override path.
	Actions map[*analysis.Analyzer]L0ActionData
}

// L0ActionData is the per-analyzer payload of an L0DepEntry.
type L0ActionData struct {
	Facts     []byte
	FactsHash [32]byte
	Err       string
}

// SetL0DepOverrides installs the dep-override map on the
// snapshot. The map is copied into the snapshot's typeCheckBatch on
// the next acquireTypeChecking call, so callers must install overrides
// BEFORE calling Snapshot.Analyze. Calling SetL0DepOverrides after
// Analyze has begun is a no-op for the in-flight batch.
//
// Passing nil or an empty map disables the dep-override fast path
// (every analysisNode goes through its normal action graph).
//
// The cache package translates each L0DepEntry into a synthetic
// *analyzeSummary keyed by the analyzer's stableName so the action
// graph's vdep lookups (which use stableName as the key) resolve
// against the right entries.
func (s *Snapshot) SetL0DepOverrides(overrides map[PackageID]L0DepEntry) {
	if len(overrides) == 0 {
		s.typeCheckMu.Lock()
		s.l0DepOverrides = nil
		s.typeCheckMu.Unlock()
		return
	}
	built := make(map[PackageID]*analyzeSummary, len(overrides))
	for id, e := range overrides {
		built[id] = buildSyntheticSummary(e)
	}
	s.typeCheckMu.Lock()
	s.l0DepOverrides = built
	s.typeCheckMu.Unlock()
}

// buildSyntheticSummary constructs a *analyzeSummary from an L0DepEntry.
// The Actions map is keyed by the analyzer's stableName (matching the
// gopls action-graph convention). Each entry's Diagnostics slice is
// left nil — see L0DepEntry.Actions for the rationale.
func buildSyntheticSummary(e L0DepEntry) *analyzeSummary {
	actions := make(actionMap, len(e.Actions))
	for a, data := range e.Actions {
		actions[stableName(a)] = &actionSummary{
			Facts:       data.Facts,
			FactsHash:   data.FactsHash,
			Diagnostics: nil,
			Err:         data.Err,
		}
	}
	return &analyzeSummary{
		Compiles: e.Compiles,
		Actions:  actions,
	}
}

// L0NodeData is the post-analysis payload the engine receives via
// SetNodeAnalyzedCallback. It carries the per-analyzer fact blobs the
// engine needs to write to L0 so subsequent runs can serve the package
// from an override.
//
// Each map key is an *analysis.Analyzer pointer that ran on the node
// (the facty subset for deps, the full enabled set for roots).
type L0NodeData struct {
	PackageID PackageID
	Compiles  bool
	Actions   map[*analysis.Analyzer]L0ActionData
}

// SetNodeAnalyzedCallback installs a callback the analysis driver
// invokes after each analysisNode completes its run. The callback runs
// synchronously inside the analysis enqueue loop, BEFORE
// decrefPreds() nils the node's actions map — so the callback sees
// every node's summary, including transitive deps whose actions are
// otherwise freed after their last predecessor finishes.
//
// The callback receives a deep-enough copy of the per-analyzer fact
// blobs that it may retain past the lifetime of the analysisNode (the
// underlying []byte slices are aliased from the actionSummary, which
// is gob-decoded fresh per node and never mutated post-decode).
//
// Pass nil to remove a previously installed callback.
func (s *Snapshot) SetNodeAnalyzedCallback(cb func(L0NodeData)) {
	s.typeCheckMu.Lock()
	s.onNodeAnalyzed = cb
	s.typeCheckMu.Unlock()
}

// nodeAnalyzedCallback returns the current callback, if any. Used by
// the analysis driver under typeCheckMu so a concurrent SetNode... can
// swap without a race.
func (s *Snapshot) nodeAnalyzedCallback() func(L0NodeData) {
	s.typeCheckMu.Lock()
	defer s.typeCheckMu.Unlock()
	return s.onNodeAnalyzed
}

// l0OverridesFor returns the synthetic summary registered for id, or
// nil if none. Used by analysisNode.runCached for the dep-
// override short-circuit. Safe for concurrent access by the analysis
// driver workers; the underlying map is installed once per snapshot
// before Analyze begins and read-only after.
func (b *typeCheckBatch) l0OverrideFor(id PackageID) *analyzeSummary {
	if b == nil || len(b.l0Overrides) == 0 {
		return nil
	}
	return b.l0Overrides[id]
}

// releaseL0OverrideActions clears the synthetic summary's Actions map
// for id, once. The check is keyed on l0OverridesReleased so that
// concurrent calls from multiple enqueue goroutines (or, defensively,
// double-release on the same node) clear at most once. The Actions
// map carries the per-analyzer Facts byte slices; once the consumer
// node has aliased them into an.actions and the L0 onNodeAnalyzed
// callback has run, the synth's hold on those blobs is the last
// persistent reference. Dropping it lets the gob-decoded fact data
// become collectible while the batch is still in flight.
//
// No-op when:
//   - the batch has no overrides installed (the override path never fired),
//   - id is not in the override map (a node that didn't take the dep-override fast path), or
//   - this id has already been released (subsequent re-decrements past zero).
func (b *typeCheckBatch) releaseL0OverrideActions(id PackageID) {
	if b == nil || len(b.l0Overrides) == 0 {
		return
	}
	synth, ok := b.l0Overrides[id]
	if !ok || synth == nil {
		return
	}
	if _, loaded := b.l0OverridesReleased.LoadOrStore(id, struct{}{}); loaded {
		return
	}
	// Drop the Actions map. Other state on synth (Compiles) stays;
	// no consumer reads it past this point but leaving the scalar is
	// safe and keeps the struct shape stable for the (currently nil)
	// case where a future reader looks up a released-but-still-present
	// synth.
	synth.Actions = nil
}

// l0OverrideHits is the atomic counter the test harness uses to assert
// that overrides actually fired the expected number of times. The
// counter is process-global because tests run with t.Parallel() off
// and the field doesn't add observable cost (one atomic add per
// override hit).
var l0OverrideHits l0OverrideHitsCounter

type l0OverrideHitsCounter struct {
	mu sync.Mutex
	n  int64
}

func (c *l0OverrideHitsCounter) add(delta int64) {
	c.mu.Lock()
	c.n += delta
	c.mu.Unlock()
}

// L0OverrideHits returns the cumulative count of dep-override
// short-circuits since process start. Tests use it as an
// instrumentation signal to verify the override fires.
func L0OverrideHits() int64 {
	l0OverrideHits.mu.Lock()
	defer l0OverrideHits.mu.Unlock()
	return l0OverrideHits.n
}

// ResetL0OverrideHits zeroes the counter. Tests call this between
// iterations.
func ResetL0OverrideHits() {
	l0OverrideHits.mu.Lock()
	l0OverrideHits.n = 0
	l0OverrideHits.mu.Unlock()
}

// StableNameForAnalyzer returns the cross-process stable name of the
// analyzer the cache uses as the actionMap key. Exposed publicly so
// engine-side test instrumentation can derive the same string the
// cache package's internal stableName would. Not used by the
// production engine flow — buildSyntheticSummary handles the
// translation internally — but tests that synthesise an L0DepEntry
// directly need a way to inspect the resulting Actions key set.
func StableNameForAnalyzer(a *analysis.Analyzer) string {
	return stableName(a)
}

// buildL0NodeData translates a freshly-analyzed analysisNode's summary
// into the L0NodeData payload the engine's callback consumes. The
// reverse stableName→Analyzer map is rebuilt here per call (cheap:
// the analyzer set is small and the same map walk happens in the
// engine's L0 write).
func buildL0NodeData(an *analysisNode, summary *analyzeSummary) L0NodeData {
	// Map stableName → *analysis.Analyzer using the node's stableNames
	// map. The map carries one entry per analyzer in the requiredAnalyzers
	// closure; we only emit L0ActionData for stableNames that appear in
	// summary.Actions (i.e. analyzers that actually ran).
	stableToAnalyzer := make(map[string]*analysis.Analyzer, len(an.stableNames))
	for a, sn := range an.stableNames {
		stableToAnalyzer[sn] = a
	}

	actions := make(map[*analysis.Analyzer]L0ActionData, len(summary.Actions))
	for sn, sumAct := range summary.Actions {
		a := stableToAnalyzer[sn]
		if a == nil {
			// Defensive: an action summary whose stableName isn't in
			// stableNames shouldn't happen (the action graph is built
			// from the requiredAnalyzers list which seeds stableNames).
			// Skip it so the L0NodeData stays consistent.
			continue
		}
		actions[a] = L0ActionData{
			Facts:     sumAct.Facts,
			FactsHash: sumAct.FactsHash,
			Err:       sumAct.Err,
		}
	}
	return L0NodeData{
		PackageID: an.ph.mp.ID,
		Compiles:  summary.Compiles,
		Actions:   actions,
	}
}

// Compile-time guard: the file.Hash size in our ActionFacts mirror
// matches the upstream actionSummary.FactsHash size. If gopls changes
// the hash width this will fail to compile and force an audit.
var _ = func() bool {
	var z [32]byte
	_ = file.Hash(z)
	return true
}()
