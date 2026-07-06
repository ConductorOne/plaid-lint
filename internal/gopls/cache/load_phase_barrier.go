// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/conductorone/plaid-lint/internal/gopls/internal/typesyncmu"
)

// loadPhaseBarrier is the typecheck-before-analyze barrier. Each
// analysisNode in a batch signals exactly once after its typecheck (or
// cache-shortcut) completes; the barrier closes its done channel when
// all signals have arrived. Analyze-phase work (execActions) waits on
// done before proceeding.
//
// The barrier owns the batch's typesyncmu load-phase claim. The
// constructor calls EnterLoadPhase; closeNow calls ExitLoadPhase and
// EnterAnalyzePhase (idempotent via sync.Once). Snapshot.Analyze
// invokes releaseAtTeardown after dispatch returns to fire the matching
// ExitAnalyzePhase and, if the barrier never closed (dispatch aborted
// before every node signalled), the deferred ExitLoadPhase.
//
// Deadlock note: the prior attempt placed this barrier with the
// unfinishedSuccs decrement still gated on execActions completion, so
// a predecessor could never typecheck (its enqueue was blocked by a
// successor whose analyze was blocked on the barrier). The fix lifts the
// decrement to fire after typeCheck, so preds enqueue and typecheck
// independently of vdep analyze. cacheKey is computed via the
// input-based derivation, which does not read vdep.actions, so the
// pre-vdep-analyze enqueue is safe.
type loadPhaseBarrier struct {
	pending atomic.Int64
	done    chan struct{}

	// closeOnce gates the ExitLoadPhase + EnterAnalyzePhase transition
	// so it fires exactly once even if pending hits zero concurrently
	// with a releaseAtTeardown abort.
	closeOnce sync.Once

	// state tracks whether the barrier has transitioned out of the load
	// phase yet (loadPhaseExited) and into the analyze phase
	// (analyzePhaseEntered). releaseAtTeardown consults these to fire
	// only the matching ExitAnalyzePhase / ExitLoadPhase calls.
	loadPhaseExited     atomic.Bool
	analyzePhaseEntered atomic.Bool
}

// newBarrierForExistingClaim constructs a barrier expecting n typecheck
// (or shortcut) signals, claiming an existing load-phase counter
// previously installed by acquireTypeChecking. Snapshot.Analyze marks
// the batch loadPhaseClaimedByBarrier before invoking; the matching
// ExitLoadPhase becomes the barrier's responsibility (fired inside
// closeNow once pending hits zero, or in releaseAtTeardown if dispatch
// aborted before all n signals arrived).
//
// EnterAnalyzePhase fires inside closeNow so analyze-phase
// instrumentation observes the transition.
func newBarrierForExistingClaim(n int) *loadPhaseBarrier {
	b := &loadPhaseBarrier{done: make(chan struct{})}
	b.pending.Store(int64(n))
	if n == 0 {
		// Empty DAG (no nodes to typecheck). Open the gate eagerly so a
		// caller that waits on done returns immediately.
		b.closeNow()
	}
	return b
}

// signalDone records that one node has completed its typecheck (or
// cache-shortcut). When the count reaches zero, the gate opens and the
// load-phase counter transitions to the analyze-phase counter.
func (b *loadPhaseBarrier) signalDone() {
	if b == nil {
		return
	}
	if b.pending.Add(-1) == 0 {
		b.closeNow()
	}
}

// closeNow is the idempotent transition that closes the gate and hands
// the typesyncmu phase claim over from load to analyze.
func (b *loadPhaseBarrier) closeNow() {
	b.closeOnce.Do(func() {
		close(b.done)
		if b.loadPhaseExited.CompareAndSwap(false, true) {
			typesyncmu.ExitLoadPhase()
		}
		if b.analyzePhaseEntered.CompareAndSwap(false, true) {
			typesyncmu.EnterAnalyzePhase()
		}
	})
}

// waitForAnalyze blocks until the gate has opened or ctx is cancelled.
func (b *loadPhaseBarrier) waitForAnalyze(ctx context.Context) error {
	if b == nil {
		return nil
	}
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseAtTeardown is invoked once by Snapshot.Analyze at the end of
// dispatch (success or error). It releases the analyze-phase claim if
// closeNow successfully entered it, and falls back to releasing the
// load-phase claim if dispatch aborted before all nodes signalled (the
// barrier never closed).
//
// Safe to call exactly once. Calling more than once panics via the
// underlying typesyncmu counter-underflow guard.
func (b *loadPhaseBarrier) releaseAtTeardown() {
	if b == nil {
		return
	}
	// If the barrier never closed, we still hold the load-phase claim;
	// release it. The analyze-phase claim was never acquired, so skip
	// EnterAnalyzePhase's matching exit.
	if b.loadPhaseExited.CompareAndSwap(false, true) {
		typesyncmu.ExitLoadPhase()
		return
	}
	// closeNow ran successfully (loadPhaseExited was already true).
	// Release the analyze-phase claim it acquired.
	if b.analyzePhaseEntered.Load() {
		typesyncmu.ExitAnalyzePhase()
	}
}

// TypesyncCounters returns the process-wide typesyncmu instrumentation
// values: (rlockHeld, rlockSkipped, lockCalls, lockCallsDuringAnalyze).
// Re-export of the typesyncmu package counters so tests outside
// internal/gopls/ (e.g. internal/bench/) can pin the phase-ordering
// invariants. Test-only.
func TypesyncCounters() (rlockHeld, rlockSkipped, lockCalls, lockDuringAnalyze int64) {
	rlockHeld, rlockSkipped, lockCalls = typesyncmu.Counters()
	lockDuringAnalyze = typesyncmu.LockCallsDuringAnalyze()
	return
}

// ResetTypesyncCounters zeroes the process-wide typesyncmu
// instrumentation counters. Test-only.
func ResetTypesyncCounters() { typesyncmu.ResetCounters() }
