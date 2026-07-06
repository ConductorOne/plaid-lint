// Package typesyncmu provides a process-wide RWMutex used to serialise
// go/types.Scope reads (via objectpath.For during fact encoding) against
// gcimporter writes (via importReader.declare → Scope.Insert).
//
// go/types.Scope is not safe for concurrent read/write: Scope.Lookup reads
// s.elems while Scope.Insert writes it. Plaid's cascade-edit workload runs
// many concurrent facts.Set.Encode calls while peer goroutines are still
// resolving imports, which trips the race.
//
// Readers (encoders) take RLock; writers (importers) take Lock. The lock is
// process-global because importers and encoders cannot statically know which
// *types.Package's scope they will touch via transitive type walks.
//
// Phase-ordering elision: when no batch in the process is in the
// load phase, no writers exist, so RLock degenerates to a no-op. Batches
// bracket their load phase with EnterLoadPhase / ExitLoadPhase. RLock
// captures the elision decision at lock-acquisition time and returns a
// token; the paired RUnlock consumes the token, guaranteeing balanced
// underlying mu.RLock / mu.RUnlock even if the load-phase counter
// transitions across the critical section.
//
// Analyze-phase tracking (r32): once a batch has closed its
// typecheck phase, EnterAnalyzePhase increments a separate counter so
// instrumentation (and Lock callers) can pin "no writer fires during
// the analyze phase". The analyze counter is observation-only; Lock is
// not blocked by it. Tests use lockCallsDuringAnalyze to assert the
// barrier invariant.
package typesyncmu

import (
	"sync"
	"sync/atomic"
)

var (
	mu                sync.RWMutex
	loadPhaseCount    atomic.Int64 // batches currently in load phase
	analyzePhaseCount atomic.Int64 // batches currently in analyze phase
)

// Token is returned by RLock and consumed by RUnlock. The zero value
// means "RLock did not acquire mu (load phase was empty); RUnlock must
// not call mu.RUnlock". A non-zero value means the underlying RLock was
// taken.
type Token struct{ held bool }

// EnterLoadPhase signals that a batch has started its load phase
// (type-check + L2-load), during which gcimporter writers will mutate
// shared *types.Scope. Must be paired with ExitLoadPhase.
func EnterLoadPhase() { loadPhaseCount.Add(1) }

// ExitLoadPhase signals that a batch has finished its load phase. Once
// the global counter returns to zero, subsequent RLock calls degenerate
// to no-ops because no writer can be in flight anywhere in the process.
func ExitLoadPhase() {
	if loadPhaseCount.Add(-1) < 0 {
		panic("typesyncmu: ExitLoadPhase without matching EnterLoadPhase")
	}
}

// inLoadPhase reports whether any batch is currently in its load phase.
// Exposed for tests; not on the steady-state hot path.
func inLoadPhase() bool { return loadPhaseCount.Load() > 0 }

// EnterAnalyzePhase signals that a batch has transitioned from its
// load phase into its analyze phase. The analyze counter is purely
// observation: instrumentation pins that Lock callers never fire while
// analyzePhaseCount > 0 (the structural invariant the barrier
// establishes). Must be paired with ExitAnalyzePhase.
func EnterAnalyzePhase() { analyzePhaseCount.Add(1) }

// ExitAnalyzePhase signals that a batch has finished its analyze phase.
func ExitAnalyzePhase() {
	if analyzePhaseCount.Add(-1) < 0 {
		panic("typesyncmu: ExitAnalyzePhase without matching EnterAnalyzePhase")
	}
}

// inAnalyzePhase reports whether any batch is currently in its analyze
// phase. Exposed for tests.
func inAnalyzePhase() bool { return analyzePhaseCount.Load() > 0 }

// RLock acquires the underlying read lock only when at least one batch
// is in its load phase; otherwise it returns immediately. The returned
// Token records the decision and MUST be passed to the paired RUnlock.
func RLock() Token {
	if loadPhaseCount.Load() == 0 {
		rlockSkipped.Add(1)
		return Token{}
	}
	mu.RLock()
	rlockHeld.Add(1)
	return Token{held: true}
}

// RUnlock releases the read lock acquired by RLock. If the Token's held
// field is false, RUnlock is a no-op (RLock skipped acquisition).
func RUnlock(t Token) {
	if !t.held {
		return
	}
	mu.RUnlock()
}

// Lock acquires the write lock. Writers always lock; the load-phase
// elision applies only to readers.
func Lock() {
	lockCalls.Add(1)
	if analyzePhaseCount.Load() > 0 {
		lockCallsDuringAnalyze.Add(1)
	}
	mu.Lock()
}

// Unlock releases the write lock acquired by Lock.
func Unlock() { mu.Unlock() }

// Instrumentation counters. Tests inspect them to pin
// the phase-ordering invariants: zero Lock calls during the analyze
// phase (no writers in analyze phase) and RLock no-op rate (all RLocks
// elided when load phase is empty).
var (
	rlockHeld              atomic.Int64 // RLocks that acquired mu.RLock
	rlockSkipped           atomic.Int64 // RLocks that skipped (load phase empty)
	lockCalls              atomic.Int64 // total Lock calls (writers)
	lockCallsDuringAnalyze atomic.Int64 // Lock calls observed while analyzePhaseCount > 0
)

// Counters returns the current instrumentation values. Test-only.
func Counters() (rlockHeldN, rlockSkippedN, lockN int64) {
	return rlockHeld.Load(), rlockSkipped.Load(), lockCalls.Load()
}

// LockCallsDuringAnalyze returns the count of Lock calls observed while
// at least one batch was in its analyze phase. Test-only; pins the
// "no writers during analyze" invariant the barrier establishes.
func LockCallsDuringAnalyze() int64 { return lockCallsDuringAnalyze.Load() }

// ResetCounters zeroes the instrumentation counters. Test-only.
func ResetCounters() {
	rlockHeld.Store(0)
	rlockSkipped.Store(0)
	lockCalls.Store(0)
	lockCallsDuringAnalyze.Store(0)
}
