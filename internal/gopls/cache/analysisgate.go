// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
)

// analysisGate is the Phase 1.7 sub-path-c clustering admission gate.
// It replaces the bare channel-semaphore at analysis.go's per-package
// outer limiter with a gate that can optionally cap the count of
// distinct in-flight packages (the "clustering" bias).
//
// When maxInFlight == 0 the gate degrades exactly to the prior
// channel-semaphore: cap = workerCap, no clustering rule. When
// maxInFlight > 0 the gate caps both worker count AND distinct
// in-flight package count; admissions for packages already in
// flight are unaffected (rule (A)), admissions for
// new packages block when the cluster is full (rule (B)).
//
// The fallthroughT mechanism breaks the W9-budget × cluster
// pathological case where every in-flight package is blocked on
// the W9 byte-budget gate: after an admission has waited
// fallthroughT it admits a new package regardless of cluster cap.
type analysisGate struct {
	workerCap    int
	maxInFlight  int
	fallthroughT time.Duration

	mu       sync.Mutex
	cond     *sync.Cond
	workers  int
	inFlight map[metadata.PackageID]int

	stats analysisGateStats
}

// analysisGateStats is the gate's telemetry surface. All counters
// are cumulative since gate construction. Reads are race-safe via
// atomics; writes happen under mu or via atomic ops where the field
// is not map-shaped.
type analysisGateStats struct {
	clusterAdmits   atomic.Uint64
	newPkgAdmits    atomic.Uint64
	fallthroughHits atomic.Uint64
	blocks          atomic.Uint64
	waitNS          atomic.Int64
	peakInFlight    atomic.Uint64
}

// AnalysisGateStats is a snapshot of the gate's counters. Returned
// by analysisGate.Snapshot for the bench harness's trace output.
type AnalysisGateStats struct {
	ClusterAdmits   uint64
	NewPkgAdmits    uint64
	FallthroughHits uint64
	Blocks          uint64
	WaitTotal       time.Duration
	PeakInFlight    uint64
}

const defaultGateFallthroughT = 2 * time.Second

func newAnalysisGate(workerCap, maxInFlight int, fallthroughT time.Duration) *analysisGate {
	g := &analysisGate{
		workerCap:    workerCap,
		maxInFlight:  maxInFlight,
		fallthroughT: fallthroughT,
		inFlight:     map[metadata.PackageID]int{},
	}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquire blocks until the caller may run analysis on pkg. The
// returned release MUST be called exactly once when the per-package
// work returns. err is non-nil only when ctx is canceled.
func (g *analysisGate) acquire(ctx context.Context, pkg metadata.PackageID) (release func(), err error) {
	start := time.Now()

	g.mu.Lock()

	if ctx.Err() != nil {
		g.mu.Unlock()
		return nil, ctx.Err()
	}

	// Cancellation watcher: wake the cond if ctx is canceled while we
	// wait. Same shape as scheduler.budgetGate.acquire.
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

	waited := false
	for {
		if ctx.Err() != nil {
			if waited {
				g.stats.blocks.Add(1)
				g.stats.waitNS.Add(time.Since(start).Nanoseconds())
			}
			g.mu.Unlock()
			return nil, ctx.Err()
		}

		// Worker cap (= GOMAXPROCS) is always enforced first.
		if g.workers >= g.workerCap {
			waited = true
			g.cond.Wait()
			continue
		}

		// No clustering bias: admit any package.
		if g.maxInFlight == 0 {
			g.workers++
			g.inFlight[pkg]++
			g.stats.newPkgAdmits.Add(1)
			g.updatePeakLocked()
			if waited {
				g.stats.blocks.Add(1)
				g.stats.waitNS.Add(time.Since(start).Nanoseconds())
			}
			g.mu.Unlock()
			return g.releaseFn(pkg), nil
		}

		// Rule (A): same-package admission — package is already in flight.
		if g.inFlight[pkg] > 0 {
			g.workers++
			g.inFlight[pkg]++
			g.stats.clusterAdmits.Add(1)
			g.updatePeakLocked()
			if waited {
				g.stats.blocks.Add(1)
				g.stats.waitNS.Add(time.Since(start).Nanoseconds())
			}
			g.mu.Unlock()
			return g.releaseFn(pkg), nil
		}

		// Rule (B): new-package admission — room under cluster cap.
		if len(g.inFlight) < g.maxInFlight {
			g.workers++
			g.inFlight[pkg]++
			g.stats.newPkgAdmits.Add(1)
			g.updatePeakLocked()
			if waited {
				g.stats.blocks.Add(1)
				g.stats.waitNS.Add(time.Since(start).Nanoseconds())
			}
			g.mu.Unlock()
			return g.releaseFn(pkg), nil
		}

		// Rule (C): fall-through after fallthroughT — clusters x W9
		// can otherwise deadlock or starve.
		if g.fallthroughT > 0 && time.Since(start) >= g.fallthroughT {
			g.workers++
			g.inFlight[pkg]++
			g.stats.fallthroughHits.Add(1)
			g.updatePeakLocked()
			// fall-through implies we waited (otherwise we'd have admitted
			// via rule (B) before the timer expired).
			g.stats.blocks.Add(1)
			g.stats.waitNS.Add(time.Since(start).Nanoseconds())
			g.mu.Unlock()
			return g.releaseFn(pkg), nil
		}

		waited = true
		// To honour the fallthroughT bound, time-bound the wait.
		// sync.Cond doesn't support timed wait directly; we use a
		// helper goroutine that broadcasts at the deadline.
		if g.fallthroughT > 0 {
			remaining := g.fallthroughT - time.Since(start)
			if remaining > 0 {
				timerDone := make(chan struct{})
				timer := time.AfterFunc(remaining, func() {
					g.mu.Lock()
					g.cond.Broadcast()
					g.mu.Unlock()
				})
				// Wait, then cancel the timer. The Wait re-acquires mu
				// before returning.
				g.cond.Wait()
				timer.Stop()
				close(timerDone)
				continue
			}
		}
		g.cond.Wait()
	}
}

func (g *analysisGate) releaseFn(pkg metadata.PackageID) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.workers > 0 {
				g.workers--
			}
			if g.inFlight[pkg] > 0 {
				g.inFlight[pkg]--
				if g.inFlight[pkg] == 0 {
					delete(g.inFlight, pkg)
				}
			}
			g.cond.Broadcast()
			g.mu.Unlock()
		})
	}
}

// updatePeakLocked records the high-water mark of distinct
// in-flight packages. Caller holds g.mu.
func (g *analysisGate) updatePeakLocked() {
	n := uint64(len(g.inFlight))
	for {
		cur := g.stats.peakInFlight.Load()
		if n <= cur {
			return
		}
		if g.stats.peakInFlight.CompareAndSwap(cur, n) {
			return
		}
	}
}

// Snapshot returns a race-safe copy of the gate's stats.
func (g *analysisGate) Snapshot() AnalysisGateStats {
	return AnalysisGateStats{
		ClusterAdmits:   g.stats.clusterAdmits.Load(),
		NewPkgAdmits:    g.stats.newPkgAdmits.Load(),
		FallthroughHits: g.stats.fallthroughHits.Load(),
		Blocks:          g.stats.blocks.Load(),
		WaitTotal:       time.Duration(g.stats.waitNS.Load()),
		PeakInFlight:    g.stats.peakInFlight.Load(),
	}
}
