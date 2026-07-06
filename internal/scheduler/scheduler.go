// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package scheduler is the W9 RSS-aware concurrency coordinator for
// the plaid-lint action graph (phase-1 tasks 1.33-1.35).
//
// The scheduler is opt-in: when no [Scheduler] is attached on the
// owning Cache, the gopls action driver runs the existing W7/W8
// path unchanged (one goroutine per action, capped at
// runtime.GOMAXPROCS, byte-equivalent to bd11605).
//
// When a scheduler is attached, the driver consults
// [Scheduler.Acquire] before launching each action and calls the
// returned release function when the action's exec returns. The
// scheduler may block Acquire to respect an RSS budget, schedule
// actions in topological order, or both. The IR lifetime contract
// is observer-only: the scheduler may
// implement [l3.IRManager] (and the optional
// [l3.AnalyzerAwareIRManager]) so it sees the pin/release stream,
// but it never holds an *ir.Program reference itself. The
// [RSSBudgetScheduler] in rss.go is the W9 production attachment.
//
// The W10 work (benchmarking + concurrency-tuning + stretch
// checkpoint) consumes [Scheduler.Stats] to drive its experiments
// without modifying the scheduler implementation.
package scheduler

import (
	"context"
	"time"

	"github.com/conductorone/plaid-lint/internal/l3"
)

// PackageID re-exports [l3.PackageID] so callers depending only on
// the scheduler don't have to import l3. Type-aliased so an *l3.Pin
// from the IRManager half of the scheduler interoperates with the
// gopls action driver's existing pin call sites.
type PackageID = l3.PackageID

// Action is the smallest unit the scheduler reasons about: one
// (package, analyzer) pair, plus the RSS estimate the scheduler
// uses to gate concurrency. The shape mirrors what
// internal/gopls/cache/analysis.go's action struct produces, but
// is intentionally a value type so callers (including external
// benchmark harnesses) can build a synthetic action set without
// reaching into the cache package.
type Action struct {
	// Package is the metadata.PackageID of the package the
	// analyzer runs on.
	Package PackageID

	// Analyzer is the analyzer's name (e.g. "SA4017", "printf").
	// The name is the wire-format stable identifier the L1/L2
	// caches key on.
	Analyzer string

	// NeedsIR is true when the analyzer's Run consumes
	// *ir.Program via pass.ResultOf[buildir]. The scheduler uses
	// this to project RSS pressure: NeedsIR=true actions inflate
	// the package's resident-set estimate by the RSSEstimate
	// field below; NeedsIR=false actions consume only the
	// package's parsed-AST footprint.
	NeedsIR bool

	// RSSEstimate is the bytes-of-resident-set the scheduler
	// budgets against this action. The estimator owns this
	// number; the scheduler treats it as opaque input. Zero is
	// legal (the scheduler treats unestimated actions as
	// budget-neutral).
	RSSEstimate uint64
}

// Scheduler coordinates the execution of a batch of analyzer
// actions. The W9 implementation is [RSSBudgetScheduler]; the
// caller selects an implementation via [Cache.AttachScheduler].
//
// Acquire/Release are the hot-path API: the gopls action driver
// calls Acquire before each action and the returned release()
// inside a defer immediately after the action.exec body. Stats is
// the cold-path API: external callers (the W10 benchmark harness,
// progress reporters) read it to observe the scheduler's
// decisions.
//
// Run is the external entry point. The standard Analyze flow does
// NOT call Run; it goes through Acquire/Release. Run exists so
// W10 benchmarks (and Phase 2 dry-run drivers) can pass an
// explicit action set without going through the gopls driver. The
// scheduler synthesises Acquire/Release internally and calls the
// caller-supplied exec function. The W9 production driver
// implementation (RSSBudgetScheduler) wires its Acquire/Release
// path identically to Run, so the two paths are observably
// equivalent.
//
// Implementations MUST be safe for concurrent use from arbitrary
// goroutines.
type Scheduler interface {
	// Acquire blocks until the scheduler's budget admits an action
	// of the given shape. The returned release function MUST be
	// called exactly once when the action's exec returns (success
	// or failure); it is a no-op on a nil receiver so callers
	// can `defer release()` unconditionally. The release function
	// captures any per-action telemetry the scheduler records.
	Acquire(ctx context.Context, act Action) (release func(), err error)

	// Run executes a batch of actions via exec, applying the
	// scheduler's Acquire/Release semantics around each call. The
	// exec function is called concurrently; its semantics
	// (errgroup ordering, partial-failure propagation) match the
	// caller's expectations. Run returns the first error exec
	// returned, or nil if every exec returned nil.
	//
	// Run is the W10 benchmark entry; the production Analyze flow
	// reaches the scheduler via Acquire/Release instead.
	Run(ctx context.Context, actions []Action, exec func(context.Context, Action) error) error

	// Stats returns a snapshot of the scheduler's observability
	// counters. Safe for concurrent use; the returned Stats value
	// is a copy.
	Stats() Stats
}

// Stats is the observability surface the W10 benchmarking and
// progress harnesses read. Counters are cumulative since
// scheduler construction; durations are aggregate wall-time.
type Stats struct {
	// ActionsAcquired is the cumulative count of Acquire calls.
	ActionsAcquired uint64

	// ActionsCompleted is the cumulative count of release()
	// invocations from Acquire return values.
	ActionsCompleted uint64

	// ActionsBlocked is the cumulative count of Acquire calls
	// that observed the budget gate and had to wait. The
	// difference (ActionsAcquired - ActionsBlocked) is the count
	// of actions admitted without queueing.
	ActionsBlocked uint64

	// TotalWaitDuration is the aggregate time Acquire calls
	// spent blocked on the budget gate.
	TotalWaitDuration time.Duration

	// TotalActionDuration is the aggregate time between Acquire
	// returning and release() being invoked. The W10 benchmark
	// derives per-analyzer wall-time histograms from this stream
	// by sampling Stats at known checkpoints.
	TotalActionDuration time.Duration

	// PeakConcurrency is the high-water mark of in-flight
	// (Acquired - Completed) actions observed by the scheduler.
	// Useful for verifying that the budget gate is binding under
	// load.
	PeakConcurrency uint64

	// ConcurrencyCap is the scheduler's current admit cap, in
	// units of in-flight actions. For [RSSBudgetScheduler] this is
	// the derived "RSSBudget / typicalRSSPerAction" projection;
	// for synthetic schedulers it may be a fixed constant. The
	// value is a recent sample, not a guaranteed instantaneous
	// reading.
	ConcurrencyCap uint64

	// BudgetBytes is the total RSS budget the scheduler is
	// enforcing, in bytes. Mirrors the constructor argument for
	// [RSSBudgetScheduler]; exposed here so the W10 harness can
	// reconstruct the experiment parameters from a Stats snapshot.
	BudgetBytes uint64

	// InFlightBytes is the sum of RSSEstimate for currently
	// acquired-but-not-released actions. The scheduler admits an
	// Acquire iff InFlightBytes + act.RSSEstimate ≤ BudgetBytes
	// OR the in-flight set is empty (always-admit-one to avoid
	// deadlock on an oversized action).
	InFlightBytes uint64

	// IRPinEvents is the cumulative count of pin events the
	// scheduler observed via its [l3.IRManager] role. Zero when
	// the scheduler does not implement l3.IRManager or no actions
	// have NeedsIR=true.
	IRPinEvents uint64

	// IRReleaseEvents is the cumulative count of release events
	// the scheduler observed via its [l3.IRManager] role.
	IRReleaseEvents uint64
}
