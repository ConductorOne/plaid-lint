// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package l3

import (
	"sync"
	"testing"
)

// TestSequentialIRManagerPinReleaseRoundTrip exercises the basic
// lifecycle invariant: Pin then Release leaves no live pins.
func TestSequentialIRManagerPinReleaseRoundTrip(t *testing.T) {
	m := NewSequentialIRManager()
	if got := m.Snapshot(); len(got) != 0 {
		t.Fatalf("fresh manager: Snapshot = %v, want empty", got)
	}
	if got := m.TotalPins(); got != 0 {
		t.Fatalf("fresh manager: TotalPins = %d, want 0", got)
	}

	p := m.Pin("example.com/foo")
	if got := m.Snapshot(); got["example.com/foo"] != 1 {
		t.Errorf("after Pin: Snapshot[foo] = %d, want 1; full snapshot = %v",
			got["example.com/foo"], got)
	}
	if got := m.TotalPins(); got != 1 {
		t.Errorf("after Pin: TotalPins = %d, want 1", got)
	}

	p.Release()
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("after Release: Snapshot = %v, want empty", got)
	}
	// TotalPins is cumulative; it MUST NOT decrement on Release.
	if got := m.TotalPins(); got != 1 {
		t.Errorf("after Release: TotalPins = %d, want 1 (cumulative)", got)
	}
}

// TestSequentialIRManagerDoubleReleaseIdempotent guards against the
// "callback registered twice" shape that the W9 scheduler will see
// when a free-after-fanin hook fires alongside the analyzer's defer.
func TestSequentialIRManagerDoubleReleaseIdempotent(t *testing.T) {
	m := NewSequentialIRManager()
	p := m.Pin("pkg")
	p.Release()
	p.Release() // must not double-decrement
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("after double Release: Snapshot = %v, want empty", got)
	}
	// Pin a fresh handle and confirm bookkeeping isn't corrupted.
	q := m.Pin("pkg")
	if got := m.Snapshot(); got["pkg"] != 1 {
		t.Errorf("after fresh Pin post-double-release: Snapshot[pkg] = %d, want 1",
			got["pkg"])
	}
	q.Release()
}

// TestSequentialIRManagerNestedPins covers the per-package fanin
// shape: multiple NeedsIR analyzers on the same package run
// concurrently and each takes its own pin. The package must remain
// pinned until the LAST analyzer releases.
func TestSequentialIRManagerNestedPins(t *testing.T) {
	m := NewSequentialIRManager()
	p1 := m.Pin("pkg")
	p2 := m.Pin("pkg")
	p3 := m.Pin("pkg")
	if got := m.Snapshot(); got["pkg"] != 3 {
		t.Errorf("after 3 Pins: Snapshot[pkg] = %d, want 3", got["pkg"])
	}
	p1.Release()
	if got := m.Snapshot(); got["pkg"] != 2 {
		t.Errorf("after 1 Release: Snapshot[pkg] = %d, want 2", got["pkg"])
	}
	p2.Release()
	if got := m.Snapshot(); got["pkg"] != 1 {
		t.Errorf("after 2 Releases: Snapshot[pkg] = %d, want 1", got["pkg"])
	}
	p3.Release()
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("after 3 Releases: Snapshot = %v, want empty", got)
	}
}

// TestSequentialIRManagerConcurrentPins is the race-detector
// regression test: N goroutines each Pin+Release the same package M
// times. Snapshot at the end must show zero live pins and TotalPins
// must equal N*M.
func TestSequentialIRManagerConcurrentPins(t *testing.T) {
	const goroutines = 16
	const itersPerGoroutine = 256
	m := NewSequentialIRManager()
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < itersPerGoroutine; j++ {
				p := m.Pin("pkg")
				p.Release()
			}
		}()
	}
	wg.Wait()
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("after %d concurrent goroutines: Snapshot = %v, want empty",
			goroutines, got)
	}
	if got, want := m.TotalPins(), int64(goroutines*itersPerGoroutine); got != want {
		t.Errorf("TotalPins = %d, want %d", got, want)
	}
}

// TestNoopIRManagerSatisfiesInterface guards the interface contract:
// NoopIRManager must implement IRManager and tolerate Pin+Release
// without bookkeeping.
func TestNoopIRManagerSatisfiesInterface(t *testing.T) {
	var m IRManager = NoopIRManager{}
	p := m.Pin("anything")
	p.Release()
	p.Release() // double-release tolerated
	if got := m.Snapshot(); len(got) != 0 {
		t.Errorf("NoopIRManager.Snapshot = %v, want empty", got)
	}
}

// TestPinNilReleaseSafe documents the nil-Pin contract: Release on a
// nil *Pin is a no-op. This matches the future scheduler's expected
// behavior when a NeedsIR=false analyzer skipped the Pin call.
func TestPinNilReleaseSafe(t *testing.T) {
	var p *Pin
	p.Release() // must not panic
}
