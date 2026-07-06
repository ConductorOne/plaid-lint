// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

// cache_adapter.go bridges the W9 scheduler's Scheduler interface
// (which speaks the rich scheduler.Action type) to the minimal
// cache.ActionScheduler interface (which speaks the smaller
// cache.ScheduledAction type). The adapter keeps the import
// direction scheduler → cache one-way: the cache package owns
// ScheduledAction and ActionScheduler, the scheduler package
// provides this thin shim that satisfies them.
//
// Concretely: RSSBudgetScheduler.Acquire takes a
// scheduler.Action; the cache action driver calls Acquire with a
// cache.ScheduledAction. cacheAdapter embeds *RSSBudgetScheduler
// (so promoted methods — including the l3.IRManager surface for
// Pin / PinWithAnalyzer / Snapshot / TotalPins — flow through)
// and adds a single Acquire method that translates the cache
// argument shape into the scheduler one. AttachScheduler in the
// cache package type-asserts the adapter as l3.IRManager so the
// IR pin/release stream stays on the same instance.

import (
	"context"

	"github.com/conductorone/plaid-lint/internal/gopls/cache"
)

// cacheAdapter is the bridge returned by [AsCacheScheduler]. It
// implements cache.ActionScheduler and (via the embedded
// *RSSBudgetScheduler) l3.IRManager + l3.AnalyzerAwareIRManager.
type cacheAdapter struct {
	*RSSBudgetScheduler
}

// Acquire implements [cache.ActionScheduler] by translating the
// cache action shape into a scheduler.Action and delegating to
// the embedded scheduler. The needsIR / RSSEstimateBytes fields
// flow through verbatim; the scheduler's gate decides whether to
// admit.
func (a *cacheAdapter) Acquire(ctx context.Context, act cache.ScheduledAction) (func(), error) {
	return a.RSSBudgetScheduler.Acquire(ctx, Action{
		Package:     PackageID(act.Package),
		Analyzer:    act.Analyzer,
		NeedsIR:     act.NeedsIR,
		RSSEstimate: act.RSSEstimateBytes,
	})
}

// Estimate implements [cache.ActionScheduler] by delegating to
// the scheduler's owned [DefaultEstimator].
func (a *cacheAdapter) Estimate(act cache.ScheduledAction) uint64 {
	return a.RSSBudgetScheduler.Estimator().Estimate(
		PackageID(act.Package), act.Analyzer, act.NeedsIR,
	)
}

// Observe implements [cache.ActionScheduler] by recording the
// observed RSS against the scheduler's owned [DefaultEstimator].
func (a *cacheAdapter) Observe(act cache.ScheduledAction, observedBytes uint64) {
	a.RSSBudgetScheduler.Estimator().Observe(
		PackageID(act.Package), act.Analyzer, act.NeedsIR, observedBytes,
	)
}

// Sampler implements [cache.ActionScheduler] by delegating to the
// scheduler's owned [Sampler]. The scheduler defaults to
// [DefaultSampler]; tests and the benchmark harness override via
// [RSSBudgetScheduler.SetSampler]. Returns a nil cache-side
// interface value (not just a typed nil) when the scheduler's
// sampler is nil, so the cache's "sampler == nil" check skips
// observation entirely.
func (a *cacheAdapter) Sampler() cache.ObservationSampler {
	samp := a.RSSBudgetScheduler.Sampler()
	if samp == nil {
		return nil
	}
	return samp
}

// AsCacheScheduler wraps s in an adapter that satisfies
// [cache.ActionScheduler] AND [l3.IRManager]. The caller passes
// the result to [cache.Cache.AttachScheduler], which type-asserts
// the adapter for l3.IRManager and installs it on both the
// scheduler and IRManager fields. The IR pin/release stream and
// the action admit gate therefore observe the same scheduler
// instance — the contract the W9 stats accounting relies on.
func AsCacheScheduler(s *RSSBudgetScheduler) cache.ActionScheduler {
	return &cacheAdapter{RSSBudgetScheduler: s}
}

// Compile-time check that the adapter satisfies the
// cache.ActionScheduler interface.
var _ cache.ActionScheduler = (*cacheAdapter)(nil)
