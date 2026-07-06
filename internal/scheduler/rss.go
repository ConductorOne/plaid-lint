// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/conductorone/plaid-lint/internal/l3"
)

// DefaultRSSBudgetBytes is the W9 default RSS ceiling: ≤ 5 GB peak
// RSS on c1-scale repos. The W10 benchmark
// harness can override this when measuring different hardware
// envelopes; production attachments via [Cache.AttachScheduler] pass
// the value explicitly so the default is opt-in only.
const DefaultRSSBudgetBytes uint64 = 5 * 1024 * 1024 * 1024

// RSSBudgetScheduler is the W9 production scheduler. It implements
// [Scheduler], [l3.IRManager], and [l3.AnalyzerAwareIRManager] so
// the gopls action driver can attach it via [Cache.AttachScheduler]
// and have the same instance observe both the action admit gate
// and the IR pin/release stream.
//
// Concurrency policy:
//
//  1. A semaphore caps in-flight actions at MaxConcurrency
//     (defaults to runtime.GOMAXPROCS at construction). This
//     prevents the scheduler from issuing more goroutines than
//     the gopls driver's existing limiter would have admitted.
//  2. Layered on top, a bytes-budget gate caps the sum of
//     RSSEstimate across in-flight actions at BudgetBytes. The
//     gate is bypassed when no action is in flight (so an
//     oversized action does not deadlock the run).
//
// Pin/release contract: the scheduler
// observes pins via its embedded *l3.SequentialIRManager. When
// the per-package pin count drops to zero, the scheduler treats
// that as the "IR free-after-fanin" signal — but it does NOT call
// any kind of Free() on the IR; the IR is owned by gopls's
// action.result and reclaimed by the runtime when the
// analysisNode scope returns.
type RSSBudgetScheduler struct {
	// budgetBytes is the configured RSS ceiling. Zero disables
	// the bytes-budget gate entirely (only the in-flight
	// semaphore applies); production callers should always pass
	// a non-zero value.
	budgetBytes uint64

	// maxConcurrency is the hard cap on in-flight actions,
	// expressed as a count. Defaults to runtime.GOMAXPROCS(0) at
	// construction.
	maxConcurrency uint64

	// embedded IRManager: pin/release accounting flows through
	// here, and Snapshot / TotalPins are reachable so the W9
	// tests can assert the IR pin contract without dropping back
	// to a separate SequentialIRManager.
	*l3.SequentialIRManager

	// gate enforces both caps. A request must acquire one slot
	// from the count-semaphore AND fit within the bytes budget.
	// The bytes condition is signalled via cond. The count
	// semaphore is implemented as a channel for simplicity; the
	// cond covers the dynamic-budget half.
	gate *budgetGate

	// est is the per-action RSS estimator the cache calls through
	// the adapter's Estimate/Observe methods. The scheduler owns
	// the estimator so a single AttachScheduler call wires the
	// budget gate, the IR pin/release stream, AND the estimator's
	// feedback loop.
	est *DefaultEstimator

	// samplerMu protects sampler. Sampler is allowed to change
	// after construction (the W10 benchmark harness overrides the
	// production default), so reads and writes go through the
	// mutex; the hot path (action.exec's deferred block) reads
	// sampler once per action via Sampler() and caches the result
	// locally, so the mutex's per-action contention is bounded.
	samplerMu sync.RWMutex
	sampler   Sampler

	// stats accumulates observability counters. All fields are
	// updated under atomics so Stats() is concurrent-safe.
	stats schedStats
}

// schedStats mirrors the public Stats struct but stores atomics
// for the hot-path counters. snapshot() materialises a Stats
// value the caller can read race-free.
type schedStats struct {
	actionsAcquired  atomic.Uint64
	actionsCompleted atomic.Uint64
	actionsBlocked   atomic.Uint64
	waitNS           atomic.Int64
	actionNS         atomic.Int64
	peakConcurrency  atomic.Uint64
	irPinEvents      atomic.Uint64
}

// NewRSSBudgetScheduler returns a scheduler configured with the
// given RSS ceiling. budgetBytes=0 disables the bytes gate (only
// the in-flight semaphore applies). maxConcurrency=0 selects a
// default of GOMAXPROCS(0); the caller-supplied value is honored
// as-is otherwise. See [DefaultRSSBudgetBytes] for the production
// budget.
func NewRSSBudgetScheduler(budgetBytes uint64, maxConcurrency int) *RSSBudgetScheduler {
	mc := uint64(maxConcurrency)
	if mc == 0 {
		mc = uint64(defaultGOMAXPROCS())
	}
	return &RSSBudgetScheduler{
		budgetBytes:         budgetBytes,
		maxConcurrency:      mc,
		SequentialIRManager: l3.NewSequentialIRManager(),
		gate:                newBudgetGate(budgetBytes, mc),
		est:                 NewDefaultEstimator(),
		sampler:             DefaultSampler(),
	}
}

// Sampler returns the scheduler's per-action observation source.
// The cache adapter calls Sampler() once per action and uses the
// returned Sampler to capture NewSample/Delta around the analyzer
// body. The W10 default is [DefaultSampler] (VmHWM on Linux,
// HeapAlloc elsewhere); the benchmark harness overrides via
// [SetSampler].
func (s *RSSBudgetScheduler) Sampler() Sampler {
	s.samplerMu.RLock()
	defer s.samplerMu.RUnlock()
	return s.sampler
}

// SetSampler replaces the scheduler's per-action observation source.
// The benchmark harness uses this hook to A/B alternative sources;
// production attachments leave the default. Passing nil disables
// observation entirely (the estimator falls back to the static
// table forever).
func (s *RSSBudgetScheduler) SetSampler(samp Sampler) {
	s.samplerMu.Lock()
	defer s.samplerMu.Unlock()
	s.sampler = samp
}

// Estimator returns the scheduler's owned per-action RSS estimator.
// The cache adapter routes Estimate/Observe calls through this
// instance so a single AttachScheduler wires the estimator's
// feedback loop alongside the budget gate.
func (s *RSSBudgetScheduler) Estimator() *DefaultEstimator {
	return s.est
}

// Acquire implements [Scheduler.Acquire]. It is the production hot
// path: the gopls action driver calls it before each action and
// defers the returned release() immediately after.
//
// Failed admission contract: when Acquire returns a non-nil
// error, the caller MUST abort the action and propagate the error.
// ActionsBlocked accounts for every Acquire that had to wait on the
// gate — including waits interrupted by ctx cancellation, so the
// counter reflects the gate's contention pressure regardless of
// whether the wait ultimately produced an admission.
func (s *RSSBudgetScheduler) Acquire(ctx context.Context, act Action) (func(), error) {
	start := time.Now()
	admitted, blocked, inFlight, err := s.gate.acquire(ctx, act.RSSEstimate)
	wait := time.Since(start)
	if blocked {
		s.stats.actionsBlocked.Add(1)
		s.stats.waitNS.Add(wait.Nanoseconds())
	}
	if !admitted || err != nil {
		// ctx cancellation; gate.acquire returns released slots before reporting.
		return nil, err
	}
	s.stats.actionsAcquired.Add(1)
	s.recordPeak(inFlight)

	startExec := time.Now()
	released := false
	var releaseMu sync.Mutex
	return func() {
		releaseMu.Lock()
		defer releaseMu.Unlock()
		if released {
			return
		}
		released = true
		s.gate.release(act.RSSEstimate)
		s.stats.actionsCompleted.Add(1)
		s.stats.actionNS.Add(time.Since(startExec).Nanoseconds())
	}, nil
}

// Run implements [Scheduler.Run]. The default Analyze flow does
// not call Run; it goes through Acquire/Release directly. Run is
// the W10 benchmark entry point.
func (s *RSSBudgetScheduler) Run(ctx context.Context, actions []Action, exec func(context.Context, Action) error) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, act := range actions {
		act := act
		g.Go(func() error {
			release, err := s.Acquire(ctx, act)
			if err != nil {
				return err
			}
			defer release()
			return exec(ctx, act)
		})
	}
	return g.Wait()
}

// Stats implements [Scheduler.Stats].
//
// IRReleaseEvents is derived from the pin counter minus the sum of
// currently-held pins (which the embedded SequentialIRManager
// exposes via Snapshot). The l3 package seals the IRManager
// interface (release is an unexported method), so the scheduler
// cannot count release events directly without forking l3; the
// derivation here gives a faithful answer modulo races between
// reading IRPinEvents and Snapshot. The W10 benchmark harness
// treats the value as an approximate counter.
func (s *RSSBudgetScheduler) Stats() Stats {
	cap, inFlight := s.gate.snapshot()
	pins := s.stats.irPinEvents.Load()
	var live uint64
	for _, n := range s.SequentialIRManager.Snapshot() {
		if n > 0 {
			live += uint64(n)
		}
	}
	releases := uint64(0)
	if pins > live {
		releases = pins - live
	}
	return Stats{
		ActionsAcquired:     s.stats.actionsAcquired.Load(),
		ActionsCompleted:    s.stats.actionsCompleted.Load(),
		ActionsBlocked:      s.stats.actionsBlocked.Load(),
		TotalWaitDuration:   time.Duration(s.stats.waitNS.Load()),
		TotalActionDuration: time.Duration(s.stats.actionNS.Load()),
		PeakConcurrency:     s.stats.peakConcurrency.Load(),
		ConcurrencyCap:      cap,
		BudgetBytes:         s.budgetBytes,
		InFlightBytes:       inFlight,
		IRPinEvents:         pins,
		IRReleaseEvents:     releases,
	}
}

// Pin overrides [l3.SequentialIRManager.Pin] so the scheduler can
// count pin events for its telemetry. The underlying refcount is
// still kept by the embedded manager. The returned *l3.Pin's
// Release routes back through SequentialIRManager (because the
// l3 package seals IRManager via an unexported release method,
// the scheduler cannot intercept the release-side call), and
// IRReleaseEvents is derived from the embedded manager's
// Snapshot at Stats() time.
func (s *RSSBudgetScheduler) Pin(pkg PackageID) *l3.Pin {
	s.stats.irPinEvents.Add(1)
	return s.SequentialIRManager.Pin(pkg)
}

// PinWithAnalyzer implements [l3.AnalyzerAwareIRManager]. The
// analyzer name is currently ignored by the W9 telemetry surface
// (the scheduler folds analyzer-keyed counters into a future W10
// extension); the embedded SequentialIRManager records the pin
// itself.
func (s *RSSBudgetScheduler) PinWithAnalyzer(pkg PackageID, analyzerName string) *l3.Pin {
	_ = analyzerName
	s.stats.irPinEvents.Add(1)
	return s.SequentialIRManager.Pin(pkg)
}

// recordPeak updates the high-water mark for in-flight actions.
func (s *RSSBudgetScheduler) recordPeak(inFlight uint64) {
	for {
		cur := s.stats.peakConcurrency.Load()
		if inFlight <= cur {
			return
		}
		if s.stats.peakConcurrency.CompareAndSwap(cur, inFlight) {
			return
		}
	}
}

// budgetGate enforces the two-axis admission policy. The
// count-axis is a semaphore (channel); the bytes-axis is a
// dynamic counter guarded by a sync.Cond. The two are decoupled
// so a wait on bytes does NOT consume a count slot until both
// admit; otherwise a single oversized action could deadlock the
// whole batch.
type budgetGate struct {
	budgetBytes    uint64
	maxConcurrency uint64

	mu       sync.Mutex
	cond     *sync.Cond
	inFlight uint64 // count of currently-acquired actions
	bytes    uint64 // sum of RSSEstimate across in-flight actions
}

func newBudgetGate(budgetBytes uint64, maxConcurrency uint64) *budgetGate {
	g := &budgetGate{
		budgetBytes:    budgetBytes,
		maxConcurrency: maxConcurrency,
	}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquire blocks until both gates admit. It returns the post-acquire
// inFlight count for peak tracking.
//
// The blocked return reports whether the call had to wait at all
// (so the caller can bump ActionsBlocked exactly when one or both
// gates were not immediately satisfied).
func (g *budgetGate) acquire(ctx context.Context, want uint64) (admitted bool, blocked bool, inFlight uint64, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if ctx.Err() != nil {
		return false, false, g.inFlight, ctx.Err()
	}

	// Watch for context cancellation: a goroutine wakes the cond
	// when ctx is done so a blocked acquire returns promptly.
	cancelDone := make(chan struct{})
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				g.mu.Lock()
				g.cond.Broadcast()
				g.mu.Unlock()
			case <-cancelDone:
			}
		}()
	}
	defer close(cancelDone)

	wasBlocked := false
	for {
		if ctx.Err() != nil {
			return false, wasBlocked, g.inFlight, ctx.Err()
		}
		countOK := g.inFlight < g.maxConcurrency
		// Bytes gate: admit if budget is disabled, or if the
		// request fits, or if no other action is in flight
		// (deadlock-avoidance for oversized actions).
		bytesOK := g.budgetBytes == 0 ||
			g.inFlight == 0 ||
			g.bytes+want <= g.budgetBytes
		if countOK && bytesOK {
			g.inFlight++
			g.bytes += want
			return true, wasBlocked, g.inFlight, nil
		}
		wasBlocked = true
		g.cond.Wait()
	}
}

// release returns a slot. The caller is responsible for matching
// release to acquire (the scheduler enforces this via its
// returned-closure pattern).
func (g *budgetGate) release(want uint64) {
	g.mu.Lock()
	if g.inFlight > 0 {
		g.inFlight--
	}
	if g.bytes >= want {
		g.bytes -= want
	} else {
		g.bytes = 0
	}
	g.cond.Broadcast()
	g.mu.Unlock()
}

// snapshot returns (currentCap, currentInFlightBytes). The cap is
// a recent reading rather than the constructor value because a
// future extension may scale the cap dynamically based on
// observed RSS. For W9 it's effectively constant.
func (g *budgetGate) snapshot() (uint64, uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.maxConcurrency, g.bytes
}
