// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync/atomic"

	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/filecache"
	"github.com/conductorone/plaid-lint/internal/gopls/util/bug"
)

// runEnqueueBarrierAware is the barrier-aware path for one
// analysisNode's enqueue body. Called only when PLAID_INPUT_DIGEST=1.
//
// Shape:
//
//  1. Try cache shortcuts (L0 override, gopls filecache when L1 not
//     attached). On hit: populate an.actions, fire onNodeAnalyzed,
//     signal the load-phase barrier, close analyzeDone, decrement
//     preds' unfinishedSuccs (potentially enqueuing them), drop refs.
//  2. On cache miss: typeCheck. Signal the barrier and decrement preds'
//     unfinishedSuccs (preds can now typecheck in parallel with peers).
//  3. Wait the barrier (all batch typechecks done).
//  4. Wait each vdep's analyzeDone (so vdep.actions is populated and
//     readable from this node's execActions).
//  5. analyzeFromTypeCheck → set an.actions → close analyzeDone.
//  6. Drop refs (decrefSummaryConsumers on succs, maybeReleaseAggressive
//     on self).
//
// The unfinishedSuccs decrement on preds moves earlier (step 2) so the
// barrier can ever close — the deadlock came from waiting for all
// nodes to typecheck while preds couldn't typecheck until succs
// finished analyze. The input-based cacheKey makes that decrement
// safe pre-vdep-analyze.
func runEnqueueBarrierAware(
	ctx context.Context,
	s *Snapshot,
	gate *analysisGate,
	release func(),
	an *analysisNode,
	key file.Hash,
	enqueue func(*analysisNode),
	maybeReport func(int64),
	completed *atomic.Int64,
) error {
	// 1. Cache shortcuts. Cache hits don't consume a typecheck slot
	// (no typecheck runs), so the gate slot acquired by the caller
	// is held only across the shortcut + signaling work.
	if summary, hit, err := tryCacheShortcut(ctx, an, key); err != nil {
		// On error, signal the barrier so peer waiters don't deadlock,
		// then propagate.
		signalBarrierAndPreds(an, enqueue)
		an.closeAnalyzeDone()
		return err
	} else if hit {
		maybeReport(completed.Add(1))
		an.compiles = summary.Compiles
		an.actions = summary.Actions
		if cb := an.batch.onNodeAnalyzed; cb != nil && summary != nil && summary.Actions != nil {
			cb(buildL0NodeData(an, summary))
		}
		signalBarrierAndPreds(an, enqueue)
		// Order: dropDownstreamRefs (which may set an.succs = nil via
		// an.maybeReleaseAggressive) BEFORE closeAnalyzeDone. preds
		// wait on an.analyzeDone before calling an.decrefSummaryConsumers
		// (the only path that writes an.succs from a foreign goroutine);
		// closing analyzeDone after our own write completes prevents
		// the foreign-write/local-read race on an.succs that the race
		// detector flags inside dropDownstreamRefs's iteration.
		dropDownstreamRefs(an)
		an.closeAnalyzeDone()
		return nil
	}

	// 2. Cache miss path. The barrier-aware split is:
	//   - typecheck (holds the gate slot for CPU-bound typecheck work)
	//   - release gate (the post-typecheck barrier wait + per-vdep
	//     analyzeDone wait MUST NOT hold a worker slot, or the gate
	//     deadlocks when DAG-size > GOMAXPROCS: every admitted slot
	//     blocks waiting for an unadmitted node to typecheck)
	//   - barrier wait + vdep analyzeDone wait
	//   - re-acquire gate
	//   - analyzeFromTypeCheck (holds the gate slot for analyze)
	//
	// inFlightAnalyses dedupes cross-snapshot; the leader handles all
	// signals inside its body, joiners signal here after the leader's
	// summary is returned.
	summary, err := an.runCachedBarrier(ctx, gate, release, key, enqueue)
	if err != nil {
		an.closeAnalyzeDone()
		return err
	}

	maybeReport(completed.Add(1))
	an.compiles = summary.Compiles
	an.actions = summary.Actions
	if cb := an.batch.onNodeAnalyzed; cb != nil && summary != nil && summary.Actions != nil {
		cb(buildL0NodeData(an, summary))
	}
	// Order: dropDownstreamRefs BEFORE closeAnalyzeDone. See the
	// cache-shortcut hit path above for the rationale.
	dropDownstreamRefs(an)
	an.closeAnalyzeDone()
	return nil
}

// tryCacheShortcut consults the L0 dep-override map and the gopls-
// shared filecache (only when L1 is not attached). Returns (summary,
// true, nil) on hit, (nil, false, nil) on miss, (nil, false, err) on
// fatal cache error.
func tryCacheShortcut(ctx context.Context, an *analysisNode, key file.Hash) (*analyzeSummary, bool, error) {
	if an.batch != nil {
		if synth := an.batch.l0OverrideFor(an.ph.mp.ID); synth != nil {
			l0OverrideHits.add(1)
			return synth, true, nil
		}
	}
	const cacheKind = "analysis"
	l1Attached := an.batch != nil && an.batch.l1 != nil
	if !l1Attached {
		if summary, err := filecache.Get(cacheKind, key, analyzeSummaryCodec.Decode); err == nil {
			return summary, true, nil
		} else if err != filecache.ErrNotFound {
			return nil, false, bug.Errorf("internal error reading shared cache: %v", err)
		}
	}
	return nil, false, nil
}

// runCachedBarrier is the cache-miss path under the barrier semantics.
// Coordinates with inFlightAnalyses for cross-snapshot dedup, but
// splits the work so that typecheck signals the barrier early and
// execActions waits for the barrier + per-vdep analyzeDone.
//
// gate / release ownership: the caller's release is consumed inside
// the leader body after typecheck completes. The leader re-acquires
// a new gate slot before analyzeFromTypeCheck so the analyze portion
// runs under the same worker-cap contract as the prior path. The
// joiner path consumes release in its callback below since the
// joiner doesn't run typecheck or analyze.
func (an *analysisNode) runCachedBarrier(
	ctx context.Context,
	gate *analysisGate,
	release func(),
	key file.Hash,
	enqueue func(*analysisNode),
) (*analyzeSummary, error) {
	const cacheKind = "analysis"
	l1Attached := an.batch != nil && an.batch.l1 != nil

	leaderRan := false
	summary, err := inFlightAnalyses.get(ctx, key, func(ctx context.Context) (*analyzeSummary, error) {
		leaderRan = true

		pkg, err := an.typeCheck(ctx)
		// Set an.compiles BEFORE signaling preds. Preds' typeCheck
		// reads vdep.compiles to determine transitive error status
		// (analysis.go: typeCheck's "if !vdep.compiles" loop). Under
		// the prior dispatch, vdep.compiles was populated by the
		// enqueue body's "an.compiles = summary.Compiles" AFTER
		// runCached returned, which is also after pred enqueued.
		// Under the barrier dispatch the enqueue happens earlier; we must mirror the
		// compiles propagation here so preds see the right value.
		if err == nil && pkg != nil {
			an.compiles = pkg.compiles
		}
		// Signal barrier + decrement preds as soon as typecheck
		// returns (success or failure). Preds were waiting on this
		// to start their own typecheck.
		signalBarrierAndPreds(an, enqueue)
		if err != nil {
			release()
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}

		// Release the gate slot before the barrier wait + per-vdep
		// analyzeDone wait. Holding a worker slot here would deadlock
		// the gate when DAG-size > GOMAXPROCS: every admitted slot
		// would be blocked waiting for a peer that hasn't been
		// admitted yet.
		release()

		// Wait for the batch's barrier to open: every node has
		// signalled typecheck (or shortcut) completion. Once open,
		// the load-phase counter has dropped to zero (modulo other
		// batches), so analyzer RLocks elide.
		if err := an.batch.loadPhase.waitForAnalyze(ctx); err != nil {
			return nil, err
		}

		// Wait for each vdep's analyze to complete so vdep.actions
		// is populated before this node's execActions reads it
		// (action.exec line that reads vdep.actions[stableName]).
		if err := waitForVdepAnalyzes(ctx, an); err != nil {
			return nil, err
		}

		// Re-acquire the gate slot for the analyze portion. The
		// outer caller's defer has already become a no-op (release
		// fired above via sync.Once); a fresh release for the
		// analyze slot fires when this body returns.
		analyzeRelease, gerr := gate.acquire(ctx, an.ph.mp.ID)
		if gerr != nil {
			return nil, gerr
		}
		defer analyzeRelease()

		summary, err := an.analyzeFromTypeCheck(ctx, pkg)
		if err != nil {
			return nil, err
		}

		if !l1Attached {
			go func() {
				cacheLimit <- unit{}
				defer func() { <-cacheLimit }()
				data := analyzeSummaryCodec.Encode(summary)
				if err := filecache.Set(cacheKind, key, data); err != nil {
					// Best-effort; failure does not affect correctness.
					_ = err
				}
			}()
		}
		return summary, nil
	})
	if err != nil {
		// Leader error: barrier + preds already signalled inside the
		// leader body; release also fired inside.
		// Joiner error: leader signalled its own batch barrier; this
		// joiner needs to signal its own and release its own gate slot.
		if !leaderRan {
			signalBarrierAndPreds(an, enqueue)
			release()
		}
		return nil, err
	}
	// Joiner success: signal own batch's barrier + decrement own preds.
	// The summary itself was produced by another snapshot's leader.
	if !leaderRan {
		signalBarrierAndPreds(an, enqueue)
		release()
	}
	return summary, nil
}

// signalBarrierAndPreds signals the batch's load-phase barrier (one
// node's worth of progress) and decrements unfinishedSuccs on each
// predecessor, enqueuing any pred whose unfinishedSuccs reaches zero.
//
// Under the barrier dispatch, predecessor enqueue happens after THIS NODE'S TYPECHECK,
// not after THIS NODE'S ANALYZE. That's the load-bearing change from
// the prior dispatch where unfinishedSuccs decremented post-
// execActions. The input-based cacheKey makes the early decrement
// safe: a pred's cacheKey computation doesn't read vdep.actions, so
// vdep.actions need not be populated when pred is enqueued. Pred's
// analyze phase still waits for vdep's analyzeDone (waitForVdepAnalyzes)
// before reading vdep.actions, so DAG ordering of execActions is
// preserved.
func signalBarrierAndPreds(an *analysisNode, enqueue func(*analysisNode)) {
	if an.batch != nil && an.batch.loadPhase != nil {
		an.batch.loadPhase.signalDone()
	}
	for _, pred := range an.preds {
		if pred.unfinishedSuccs.Add(-1) == 0 {
			enqueue(pred)
		}
	}
}

// waitForVdepAnalyzes blocks until every vdep of an has signalled its
// analyzeDone channel, i.e. its an.actions is populated and safe to
// read. ctx cancellation is honoured.
func waitForVdepAnalyzes(ctx context.Context, an *analysisNode) error {
	for _, vdep := range an.succs {
		vdep.ensureAnalyzeDone()
		select {
		case <-vdep.analyzeDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// dropDownstreamRefs runs the post-analyze release dance: tell
// each vdep one more consumer is done with it; maybe release self if
// it has no remaining consumers. Mirrors the tail of the prior
// enqueue body.
func dropDownstreamRefs(an *analysisNode) {
	for _, succ := range an.succs {
		succ.decrefPreds()
		succ.decrefSummaryConsumers()
	}
	an.maybeReleaseAggressive()
}
