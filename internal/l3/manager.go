// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package l3 owns the L3 streaming-IR coordination interface.
// The L3 layer is RAM-only (*ir.Program cannot be persisted) and
// lives for the lifetime of a
// single linter invocation. Within an invocation, buildir's IR for
// package P is built at most once and freed after every NeedsIR=true
// analyzer that runs on P has completed.
//
// The intra-package once-per-(package, invocation) memoization is
// already provided by the gopls action DAG: every (analyzer, package)
// pair lives behind a sync.Once in internal/gopls/cache/analysis.go's
// execActions, so buildir is run exactly once per package per
// analysisNode.run. What W8 adds on top of that is an externally
// observable lifecycle interface — the IRManager — that the W9-W10
// scheduler can implement to coordinate per-package IR pinning, RSS
// budgeting, and free-after-fanin sequencing across packages.
//
// The W8 default implementation (SequentialIRManager) is a passthrough:
// it counts pins for visibility but does nothing else. Tests assert
// the pin/release invariants the W9 scheduler will rely on.
package l3

import (
	"sync"
	"sync/atomic"
)

// PackageID identifies a package within an invocation. The string
// shape matches metadata.PackageID at the gopls boundary; the l3
// package itself does not import the gopls metadata package because
// IRManager is a coordination interface, not an analyzer driver.
type PackageID string

// PinPhase classifies the role of a pin in the IR lifecycle. The
// classification is informational for the default manager; the W9
// scheduler will key its RSS accounting off it.
type PinPhase int

const (
	// PinPhasePending — the manager has been notified that a
	// NeedsIR=true analyzer is about to run on the package. The
	// scheduler may use this to anticipate IR construction.
	PinPhasePending PinPhase = iota
	// PinPhaseActive — buildir's IR for the package has been produced
	// and at least one NeedsIR=true analyzer is consuming it.
	PinPhaseActive
)

// Pin is the handle returned by IRManager.Pin. The caller must invoke
// Release exactly once when the analyzer body that needed IR has
// completed. The handle is opaque: callers do not introspect it.
type Pin struct {
	mgr      IRManager
	pkg      PackageID
	released atomic.Bool
}

// Release marks the pin as no longer needed. Calling Release a second
// time is a no-op. Concurrency: Release is safe to call from any
// goroutine.
func (p *Pin) Release() {
	if p == nil {
		return
	}
	if p.released.Swap(true) {
		return
	}
	p.mgr.release(p.pkg)
}

// AnalyzerAwareIRManager is an optional extension interface for
// IRManager implementations that want to receive the analyzer's
// name alongside the package on each pin. The W8 baseline
// (SequentialIRManager) does not implement it; the gopls action
// driver detects the extension via a type assertion at pin time
// and falls back to plain Pin when absent. The W9 scheduler may
// implement it to key free-after-fanin on (pkg, analyzer) pairs.
// Tests use it to record analyzer identity in pin events.
type AnalyzerAwareIRManager interface {
	IRManager
	// PinWithAnalyzer is the analyzer-name-aware counterpart to
	// Pin. The returned *Pin's Release behaves identically to one
	// returned by Pin: it calls back into the manager's release
	// method (which is package-keyed).
	PinWithAnalyzer(pkg PackageID, analyzerName string) *Pin
}

// IRManager is the per-process coordination interface for L3 IR
// liveness. Implementations track which packages currently have at
// least one NeedsIR=true analyzer in flight; the W9 scheduler will
// use that information to pin RAM, evict idle IR, and order
// dependency-driven IR rebuilds.
//
// The interface intentionally does NOT own the *ir.Program itself.
// Inside the gopls action DAG, buildir.Analyzer.Run produces the IR
// as its Result; that Result lives in action.result and is consumed
// by downstream analyzers via pass.ResultOf. When the analysisNode
// scope returns, all action.result references go out of scope and
// the IR is reclaimed by the Go runtime in the normal way. IRManager
// observes that lifecycle rather than orchestrating it; the existing
// sync.Once / pass.ResultOf machinery is the single source of truth
// for which goroutines are still reading the IR.
type IRManager interface {
	// Pin records that a NeedsIR=true analyzer is about to run on pkg.
	// Returns a *Pin the caller releases when the analyzer body has
	// completed (typically in a defer immediately after the call).
	//
	// Multiple concurrent Pins for the same pkg coexist; the manager
	// counts them so the W9 scheduler can detect "last pin released"
	// to trigger its free-after-fanin hook.
	Pin(pkg PackageID) *Pin

	// Snapshot returns an unordered snapshot of every package
	// currently holding at least one live pin, along with the pin
	// count. Used by lifecycle tests and the W9 RSS dashboard.
	Snapshot() map[PackageID]int

	// release is called by Pin.Release. Unexported so callers cannot
	// double-decrement or release a pin they don't own; the *Pin
	// receiver is the only legitimate path.
	release(pkg PackageID)
}

// SequentialIRManager is the default W8 implementation: it counts
// pins for observability but does no scheduling. Concurrent Pin and
// Release calls are safe. The struct is the zero-value-usable shape;
// callers do not need to call a constructor.
//
// The W9-W10 scheduler will introduce a `ScheduledIRManager` (or
// similar) that implements the same interface plus RSS-aware
// free-after-fanin coordination. The W8 default keeps the action-DAG
// pipeline byte-equivalent to the W7 baseline by adding no
// scheduling behavior of its own.
type SequentialIRManager struct {
	mu      sync.Mutex
	pinned  map[PackageID]int
	totPins atomic.Int64
}

// NewSequentialIRManager returns a freshly initialised
// SequentialIRManager. The zero value is also usable, but constructor
// usage makes the test contract explicit.
func NewSequentialIRManager() *SequentialIRManager {
	return &SequentialIRManager{pinned: make(map[PackageID]int)}
}

// Pin implements IRManager.
func (m *SequentialIRManager) Pin(pkg PackageID) *Pin {
	m.mu.Lock()
	if m.pinned == nil {
		m.pinned = make(map[PackageID]int)
	}
	m.pinned[pkg]++
	m.mu.Unlock()
	m.totPins.Add(1)
	return &Pin{mgr: m, pkg: pkg}
}

// Snapshot implements IRManager.
func (m *SequentialIRManager) Snapshot() map[PackageID]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[PackageID]int, len(m.pinned))
	for k, v := range m.pinned {
		if v > 0 {
			out[k] = v
		}
	}
	return out
}

// TotalPins returns the cumulative count of Pin calls across the
// manager's lifetime, regardless of subsequent Release calls. This
// is observability-only: lifecycle tests assert it climbs as
// analyzers run, and the W9 scheduler may sample it to detect pin
// churn.
func (m *SequentialIRManager) TotalPins() int64 {
	return m.totPins.Load()
}

// release implements IRManager.
func (m *SequentialIRManager) release(pkg PackageID) {
	m.mu.Lock()
	n := m.pinned[pkg]
	if n <= 1 {
		delete(m.pinned, pkg)
	} else {
		m.pinned[pkg] = n - 1
	}
	m.mu.Unlock()
}

// NoopIRManager is a zero-cost IRManager that does not track pins.
// Used by tests that want to validate the call-site shape without
// the bookkeeping cost. Production callers should prefer
// SequentialIRManager.
type NoopIRManager struct{}

// Pin implements IRManager.
func (NoopIRManager) Pin(pkg PackageID) *Pin { return &Pin{mgr: NoopIRManager{}, pkg: pkg} }

// Snapshot implements IRManager.
func (NoopIRManager) Snapshot() map[PackageID]int { return map[PackageID]int{} }

// release implements IRManager.
func (NoopIRManager) release(_ PackageID) {}
