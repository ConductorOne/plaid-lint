// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"

	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// recordingScheduler is the harness's wrapper around
// scheduler.RSSBudgetScheduler. It satisfies cache.ActionScheduler
// AND l3.IRManager (via the embedded scheduler) so AttachScheduler's
// type-assertion path installs it on both fields the same way
// scheduler.AsCacheScheduler does.
//
// The only override is Observe: the wrapper captures the (NeedsIR,
// observedBytes) tuple into the recordingSampler's per-NeedsIR
// bucket before delegating to the scheduler's estimator. This is
// the harness's only way to attribute per-NeedsIR observations
// at the cache↔scheduler boundary — Sampler.Delta doesn't see
// NeedsIR, and the scheduler's estimator's internal observation
// list is not exported.
//
// The embedded *scheduler.RSSBudgetScheduler provides Pin /
// PinWithAnalyzer / Snapshot / TotalPins (the l3.IRManager surface,
// including the unexported release(pkg) the seal'd interface
// requires) plus Acquire (the scheduler.Scheduler hot path). We
// override only the cache-facing Acquire/Estimate/Observe/Sampler
// methods to satisfy cache.ActionScheduler.
type recordingScheduler struct {
	*scheduler.RSSBudgetScheduler
	rec *recordingSampler
}

func newRecordingScheduler(inner *scheduler.RSSBudgetScheduler, rec *recordingSampler) *recordingScheduler {
	return &recordingScheduler{RSSBudgetScheduler: inner, rec: rec}
}

// Acquire implements cache.ActionScheduler. We re-declare it to
// shadow the embedded scheduler.Scheduler.Acquire signature
// (scheduler.Action vs cache.ScheduledAction).
func (r *recordingScheduler) Acquire(ctx context.Context, act cache.ScheduledAction) (func(), error) {
	return r.RSSBudgetScheduler.Acquire(ctx, scheduler.Action{
		Package:     scheduler.PackageID(act.Package),
		Analyzer:    act.Analyzer,
		NeedsIR:     act.NeedsIR,
		RSSEstimate: act.RSSEstimateBytes,
	})
}

// Estimate implements cache.ActionScheduler.
func (r *recordingScheduler) Estimate(act cache.ScheduledAction) uint64 {
	return r.RSSBudgetScheduler.Estimator().Estimate(
		scheduler.PackageID(act.Package), act.Analyzer, act.NeedsIR,
	)
}

// Observe implements cache.ActionScheduler. The harness captures
// (NeedsIR, observedBytes) into its per-bucket recorder before
// delegating to the underlying estimator so the harness can report
// per-NeedsIR mean/p95/peak without re-deriving them from internal
// estimator state.
func (r *recordingScheduler) Observe(act cache.ScheduledAction, observedBytes uint64) {
	r.rec.record(act.NeedsIR, observedBytes)
	r.RSSBudgetScheduler.Estimator().Observe(
		scheduler.PackageID(act.Package), act.Analyzer, act.NeedsIR, observedBytes,
	)
}

// Sampler implements cache.ActionScheduler. Returns the embedded
// scheduler's sampler so the cache's observation path picks up the
// W10 source selection.
func (r *recordingScheduler) Sampler() cache.ObservationSampler {
	samp := r.RSSBudgetScheduler.Sampler()
	if samp == nil {
		return nil
	}
	return samp
}

// Compile-time checks.
var (
	_ cache.ActionScheduler = (*recordingScheduler)(nil)
	_ l3.IRManager          = (*recordingScheduler)(nil)
)
