// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// scheduler.go declares the minimal ActionScheduler interface the
// gopls action driver uses to coordinate per-action concurrency
// when a W9 scheduler is attached via Cache.AttachScheduler. The
// concrete implementation lives in internal/scheduler; the
// interface is defined here so the cache package does not have to
// import internal/scheduler and we keep the dependency direction
// scheduler → cache (via the AttachScheduler call site) only.
//
// The interface is intentionally narrower than scheduler.Scheduler
// because the gopls driver only needs Acquire/Release semantics on
// the hot path; the Stats and Run methods on scheduler.Scheduler
// are read-back surfaces consumed by callers OUTSIDE the cache
// package (the W10 benchmark harness, the progress reporter), so
// they don't need to be exposed through this hook.
//
// The IR pin/release stream still flows through Cache.irManager
// (l3.IRManager); the W9 scheduler implements both interfaces and
// AttachScheduler installs it on both fields.

import (
	"context"

	"github.com/conductorone/plaid-lint/internal/analyzers"
)

// ScheduledAction is the value the cache-side action gate passes
// to an attached ActionScheduler on each Acquire call. The shape
// is intentionally identical to scheduler.Action so the
// internal/scheduler package's RSSBudgetScheduler can implement
// ActionScheduler with a one-line type-erased forward.
//
// Keeping the struct here (rather than importing it from the
// scheduler package) keeps the import direction scheduler →
// cache only, which the AttachScheduler call site already
// satisfies.
type ScheduledAction struct {
	// Package is the metadata.PackageID string the analyzer runs
	// against.
	Package string
	// Analyzer is the analyzer's wire-format name.
	Analyzer string
	// NeedsIR is the walker-fallback aware NeedsIR bit.
	NeedsIR bool
	// RSSEstimateBytes is the per-action budget reservation. The
	// cache asks the scheduler's Estimator to compute it before
	// the Acquire call; see [ActionScheduler.Estimate].
	RSSEstimateBytes uint64
}

// ActionScheduler is the minimal interface action.exec consults
// when a W9 scheduler is attached.
//
// Acquire blocks until the scheduler's budget admits the action;
// the returned release function MUST be called when the action
// body returns. The three return states are distinct:
//
//   - release != nil, err == nil — admitted; defer release().
//   - err != nil                 — refused or ctx canceled; the
//     caller MUST abort and surface err as the action's error
//     without running the analyzer body. release is nil.
//   - release == nil, err == nil — reserved; not produced by any
//     current scheduler.
//
// Estimate projects an RSS estimate the scheduler uses to gate
// the bytes-axis of admission. The cache calls Estimate before
// Acquire and threads the result into ScheduledAction.RSSEstimateBytes.
//
// Observe records the actual peak RSS the analyzer consumed,
// once the analyzer body returns. The scheduler's estimator
// folds the observation into subsequent estimates (sliding-window
// median). Estimators without a learning loop ignore the call.
//
// Sampler returns the [ObservationSampler] the cache should use to
// capture per-action observation deltas. Implementations may return
// nil to disable observation (the W9 behavior was a HeapAlloc-delta
// hard-coded at the call site); W10 routes the choice through the
// scheduler so the harness can A/B alternative sources without
// touching analysis.go.
type ActionScheduler interface {
	Acquire(ctx context.Context, act ScheduledAction) (release func(), err error)
	Estimate(act ScheduledAction) uint64
	Observe(act ScheduledAction, observedBytes uint64)
	Sampler() ObservationSampler
}

// ObservationSampler is the cache-side view of the per-action
// observation source. The scheduler package's Sampler implements
// this interface verbatim; routing through this interface keeps the
// cache → scheduler import direction one-way.
//
// NewSample captures the "before" reading at gate-admit; Delta
// returns the observed bytes for the matching "after" reading. The
// returned bytes value is the input to [ActionScheduler.Observe].
type ObservationSampler interface {
	NewSample() any
	Delta(before any) uint64
}

// acquireSchedulerSlot consults the batch's attached
// ActionScheduler (if any) and returns the slot's release
// function plus an error. The three return states are:
//
//   - sched == nil          → (nil, nil): no scheduler attached;
//     the caller runs the analyzer body unchanged (W7/W8 fast path).
//   - Acquire admitted      → (release, nil): the caller MUST
//     defer release() and proceed.
//   - Acquire returned err  → (nil, err): admission refused or ctx
//     canceled; the caller MUST abort and propagate err as the
//     action's error without running the analyzer body.
func acquireSchedulerSlot(ctx context.Context, act *action) (func(), ScheduledAction, error) {
	if act == nil || act.an == nil || act.an.batch == nil {
		return nil, ScheduledAction{}, nil
	}
	s := act.an.batch.scheduler
	if s == nil {
		return nil, ScheduledAction{}, nil
	}
	sa := ScheduledAction{
		Package:  string(act.pkg.pkg.metadata.ID),
		Analyzer: act.a.Name,
		NeedsIR:  analyzerNeedsIR(act),
	}
	sa.RSSEstimateBytes = s.Estimate(sa)
	release, err := s.Acquire(ctx, sa)
	if err != nil {
		return nil, sa, err
	}
	return release, sa, nil
}

// analyzerNeedsIR re-derives the NeedsIR bit using the same
// walker-fallback contract pinIRForAction uses: registered
// descriptor wins; unregistered transitive prereq falls back to
// the AnalyzerRequiresIR walker. The duplication is small and
// avoids exporting more of the action struct.
func analyzerNeedsIR(act *action) bool {
	if d := act.descriptor(); d != nil {
		return d.NeedsIR
	}
	return analyzers.AnalyzerRequiresIR(act.a)
}
