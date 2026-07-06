// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package typesyncmu

import (
	"sync"
	"testing"
)

// TestLoadPhaseElision pins the elision invariant: RLock outside
// any load phase is a no-op (counter == 0); RLock inside a load phase
// acquires the underlying mu.RLock. The instrumentation counters
// distinguish the two paths.
func TestLoadPhaseElision(t *testing.T) {
	ResetCounters()

	// Outside any load phase: RLock is a no-op.
	tok := RLock()
	if tok.held {
		t.Errorf("RLock outside load phase returned held token; want zero token")
	}
	RUnlock(tok)
	if held, skipped, _ := Counters(); held != 0 || skipped != 1 {
		t.Errorf("after no-op RLock: held=%d skipped=%d, want 0/1", held, skipped)
	}

	// Inside a load phase: RLock acquires.
	EnterLoadPhase()
	tok2 := RLock()
	if !tok2.held {
		t.Errorf("RLock inside load phase returned zero token; want held")
	}
	RUnlock(tok2)
	if held, skipped, _ := Counters(); held != 1 || skipped != 1 {
		t.Errorf("after held RLock: held=%d skipped=%d, want 1/1", held, skipped)
	}

	ExitLoadPhase()

	// Back outside: elision resumes.
	tok3 := RLock()
	if tok3.held {
		t.Errorf("RLock after ExitLoadPhase returned held token; want zero")
	}
	RUnlock(tok3)
	if held, skipped, _ := Counters(); held != 1 || skipped != 2 {
		t.Errorf("after final no-op RLock: held=%d skipped=%d, want 1/2", held, skipped)
	}
}

// TestNestedLoadPhases pins the counter semantics: multiple concurrent
// EnterLoadPhase calls compose; elision resumes only after the LAST
// matching ExitLoadPhase. Models the case where two Snapshot.Analyze
// invocations or a Snapshot.Analyze + concurrent forEachPackage share
// process-wide writers.
func TestNestedLoadPhases(t *testing.T) {
	ResetCounters()

	EnterLoadPhase() // counter = 1
	EnterLoadPhase() // counter = 2

	tok := RLock()
	if !tok.held {
		t.Errorf("RLock with counter=2 returned zero token; want held")
	}
	RUnlock(tok)

	ExitLoadPhase() // counter = 1

	tok2 := RLock()
	if !tok2.held {
		t.Errorf("RLock with counter=1 (one ExitLoadPhase still pending) returned zero token; want held")
	}
	RUnlock(tok2)

	ExitLoadPhase() // counter = 0

	tok3 := RLock()
	if tok3.held {
		t.Errorf("RLock with counter=0 returned held token; want zero (elision)")
	}
	RUnlock(tok3)
}

// TestExitWithoutEnterPanics pins the counter-underflow guard: calling
// ExitLoadPhase without a matching EnterLoadPhase indicates a paired-
// API violation and must panic.
func TestExitWithoutEnterPanics(t *testing.T) {
	ResetCounters()

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("ExitLoadPhase without matching EnterLoadPhase did not panic")
		}
		// Restore the counter so subsequent tests in the same binary
		// see the documented invariant (counter >= 0).
		EnterLoadPhase()
	}()
	ExitLoadPhase()
}

// TestConcurrentLockRLock exercises the writer/reader pair under load
// phase: many goroutines take Lock/Unlock and RLock/RUnlock
// concurrently; the test only asserts that the unbalanced-mu fail-fast
// is not triggered (we don't simulate a real Scope race here, just the
// lock contract). The race_test.go in internal/gopls/internal/
// facts/ covers the full Scope race.
func TestConcurrentLockRLock(t *testing.T) {
	ResetCounters()
	EnterLoadPhase()
	defer ExitLoadPhase()

	const (
		writers    = 4
		readers    = 8
		iterations = 200
	)
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				Lock()
				Unlock()
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				tok := RLock()
				RUnlock(tok)
			}
		}()
	}
	wg.Wait()

	held, _, locks := Counters()
	if int64(writers*iterations) != locks {
		t.Errorf("lockCalls=%d, want %d", locks, writers*iterations)
	}
	if int64(readers*iterations) != held {
		t.Errorf("rlockHeld=%d, want %d (all RLocks should hold inside load phase)", held, readers*iterations)
	}
}
