// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
)

// TestAnalysisGate_NoClusteringDegradesToSemaphore confirms that
// MaxInFlightPackages == 0 behaves identically to the prior channel-
// semaphore: cap = workerCap, no per-package affinity. We launch 2 *
// workerCap goroutines on distinct packages; all must be admitted
// (eventually) and the steady-state in-flight count stays at workerCap.
func TestAnalysisGate_NoClusteringDegradesToSemaphore(t *testing.T) {
	const workerCap = 4
	g := newAnalysisGate(workerCap, 0, 100*time.Millisecond)
	var (
		inFlight    atomic.Int32
		peakInFligt atomic.Int32
		wg          sync.WaitGroup
	)
	total := 4 * workerCap
	hold := 10 * time.Millisecond
	for i := 0; i < total; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			pkg := metadata.PackageID("pkg" + itoa(i)) // distinct package per goroutine
			rel, err := g.acquire(context.Background(), pkg)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			cur := inFlight.Add(1)
			for {
				p := peakInFligt.Load()
				if cur <= p {
					break
				}
				if peakInFligt.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(hold)
			inFlight.Add(-1)
			rel()
		}()
	}
	wg.Wait()
	if peak := peakInFligt.Load(); peak > int32(workerCap) {
		t.Fatalf("peak in-flight = %d, want ≤ workerCap %d", peak, workerCap)
	}
	snap := g.Snapshot()
	if snap.FallthroughHits != 0 {
		t.Errorf("fallthrough hits = %d, want 0 (clustering disabled)", snap.FallthroughHits)
	}
	if snap.ClusterAdmits != 0 {
		t.Errorf("cluster admits = %d, want 0 (clustering disabled)", snap.ClusterAdmits)
	}
	if snap.NewPkgAdmits != uint64(total) {
		t.Errorf("new-package admits = %d, want %d", snap.NewPkgAdmits, total)
	}
}

// TestAnalysisGate_ClusterCapsInFlight verifies that with
// MaxInFlightPackages = N, the peak distinct in-flight packages
// observed by the gate is exactly N (even when 4N workers are
// launched on 4N distinct packages).
func TestAnalysisGate_ClusterCapsInFlight(t *testing.T) {
	const workerCap = 16
	const cluster = 4
	g := newAnalysisGate(workerCap, cluster, 0) // no fallthrough

	var wg sync.WaitGroup
	hold := 50 * time.Millisecond
	total := 4 * cluster
	for i := 0; i < total; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			pkg := metadata.PackageID("pkg" + itoa(i))
			rel, err := g.acquire(context.Background(), pkg)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			time.Sleep(hold)
			rel()
		}()
	}
	wg.Wait()
	snap := g.Snapshot()
	if snap.PeakInFlight > cluster {
		t.Fatalf("peak in-flight packages = %d, want ≤ %d", snap.PeakInFlight, cluster)
	}
	if snap.PeakInFlight < 2 {
		t.Errorf("peak in-flight packages = %d, want ≥ 2 (workers should overlap)", snap.PeakInFlight)
	}
	if snap.FallthroughHits != 0 {
		t.Errorf("fallthrough hits = %d, want 0 (no timeout configured)", snap.FallthroughHits)
	}
}

// TestAnalysisGate_SamePackageClusters verifies rule (A): multiple
// goroutines on the same package count as one cluster slot and all
// admit concurrently up to workerCap.
func TestAnalysisGate_SamePackageClusters(t *testing.T) {
	const workerCap = 8
	const cluster = 1
	g := newAnalysisGate(workerCap, cluster, 0)

	pkg := metadata.PackageID("same-pkg")
	var (
		started atomic.Int32
		wg      sync.WaitGroup
	)
	// Hold a long time so all callers must be in-flight simultaneously.
	hold := 100 * time.Millisecond
	for i := 0; i < workerCap; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := g.acquire(context.Background(), pkg)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			started.Add(1)
			time.Sleep(hold)
			rel()
		}()
	}
	wg.Wait()
	if started.Load() != int32(workerCap) {
		t.Fatalf("started = %d, want %d (all should have been admitted concurrently)", started.Load(), workerCap)
	}
	snap := g.Snapshot()
	// Exactly 1 admission is the new-package admit (the first one);
	// the rest are cluster admits.
	if snap.NewPkgAdmits != 1 {
		t.Errorf("new-package admits = %d, want 1", snap.NewPkgAdmits)
	}
	if snap.ClusterAdmits != uint64(workerCap-1) {
		t.Errorf("cluster admits = %d, want %d", snap.ClusterAdmits, workerCap-1)
	}
	if snap.PeakInFlight != 1 {
		t.Errorf("peak in-flight packages = %d, want 1", snap.PeakInFlight)
	}
}

// TestAnalysisGate_StreamingN1_SerializesDistinctPackages is the
// Phase 1.8 sub-path-(c'') prototype smoke test. At maxInFlight=1
// distinct packages must serialize (peakInFlight == 1) while a
// single package's workers fan out concurrently up to workerCap.
//
// The test launches workerCap goroutines on package P (rule (A)
// admits all concurrently) AND a second wave of workerCap
// goroutines on packages Q1..QworkerCap. Q* must serialize: at any
// moment exactly one Q is in flight in addition to P.
func TestAnalysisGate_StreamingN1_SerializesDistinctPackages(t *testing.T) {
	const workerCap = 8
	g := newAnalysisGate(workerCap, 1, 0) // N=1, no fallthrough

	pkgP := metadata.PackageID("P")
	var (
		wg              sync.WaitGroup
		distinctPeak    atomic.Int32
		distinctCurrent atomic.Int32
	)

	// First, occupy package P with all workerCap workers concurrently.
	// All admit via rule (B) (first one) + rule (A) (rest). They wait
	// on pSignal so the Q* wave can enter while P is in-flight and
	// exercise the strict distinct-pkg=1 invariant.
	pSignal := make(chan struct{})
	for i := 0; i < workerCap; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := g.acquire(context.Background(), pkgP)
			if err != nil {
				t.Errorf("acquire P: %v", err)
				return
			}
			<-pSignal
			rel()
		}()
	}

	// Give P's workers a chance to admit before the Q wave.
	time.Sleep(10 * time.Millisecond)

	// distinct-package observer: each Q goroutine tracks whether at
	// any moment two distinct Q packages are concurrently admitted.
	// We don't observe P here — the invariant is that *additional*
	// new packages (i.e. Q*) serialize. Since rule (B) blocks Q
	// admission while any non-P package is in flight (and P never
	// counts as a new admission for Q's wait), at most one Q can be
	// in flight at a time.
	for i := 0; i < workerCap; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			q := metadata.PackageID("Q" + itoa(i))
			rel, err := g.acquire(context.Background(), q)
			if err != nil {
				t.Errorf("acquire %s: %v", q, err)
				return
			}
			cur := distinctCurrent.Add(1)
			for {
				p := distinctPeak.Load()
				if cur <= p {
					break
				}
				if distinctPeak.CompareAndSwap(p, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			distinctCurrent.Add(-1)
			rel()
		}()
	}

	// Release P workers so the Q wave can proceed.
	time.Sleep(15 * time.Millisecond)
	close(pSignal)

	wg.Wait()

	// Q peak should be exactly 1: rule (B) admits one Q at a time.
	if peak := distinctPeak.Load(); peak != 1 {
		t.Errorf("distinct-Q peak in-flight = %d, want 1 (N=1 must serialize distinct new packages)", peak)
	}
	snap := g.Snapshot()
	if snap.PeakInFlight > 2 {
		// At most: P + 1 Q in flight at the transition moment.
		t.Errorf("gate PeakInFlight = %d, want ≤ 2 (P + 1 Q at most)", snap.PeakInFlight)
	}
	if snap.FallthroughHits != 0 {
		t.Errorf("fallthrough hits = %d, want 0 (every Q must admit via rule (B))", snap.FallthroughHits)
	}
	// Rule (A) admits: workerCap-1 same-P concurrent admissions.
	if snap.ClusterAdmits != uint64(workerCap-1) {
		t.Errorf("cluster admits = %d, want %d (P had %d workers; first is new-pkg, rest are cluster)",
			snap.ClusterAdmits, workerCap-1, workerCap)
	}
	// Rule (B) admits: 1 for P + workerCap for the Q wave.
	if snap.NewPkgAdmits != uint64(1+workerCap) {
		t.Errorf("new-pkg admits = %d, want %d (1 for P + %d for the Q wave)",
			snap.NewPkgAdmits, 1+workerCap, workerCap)
	}
}

// TestAnalysisGate_FallthroughBreaksDeadlock simulates the W9
// pathology: every in-flight package is blocked indefinitely (the
// caller never releases). The gate's fallthrough should admit a new
// package after fallthroughT, preventing a true deadlock.
func TestAnalysisGate_FallthroughBreaksDeadlock(t *testing.T) {
	const workerCap = 8
	const cluster = 2
	fallthroughT := 50 * time.Millisecond
	g := newAnalysisGate(workerCap, cluster, fallthroughT)

	// Acquire two slots on package "a" and "b" and never release —
	// these simulate "stuck" in-flight packages.
	relA, errA := g.acquire(context.Background(), "a")
	if errA != nil {
		t.Fatalf("acquire a: %v", errA)
	}
	defer relA()
	relB, errB := g.acquire(context.Background(), "b")
	if errB != nil {
		t.Fatalf("acquire b: %v", errB)
	}
	defer relB()

	// Now try to acquire on package "c". Without fallthrough this
	// would block forever (cluster is full at 2, no release coming).
	start := time.Now()
	relC, errC := g.acquire(context.Background(), "c")
	wait := time.Since(start)
	if errC != nil {
		t.Fatalf("acquire c: %v", errC)
	}
	defer relC()
	if wait < fallthroughT {
		t.Errorf("acquire c returned in %v, want ≥ %v (fallthrough should have waited)", wait, fallthroughT)
	}
	// Bound how long the fallthrough may take. Be generous on a
	// busy CI box: 4× the timer.
	if wait > 4*fallthroughT {
		t.Errorf("acquire c returned in %v, want ≤ %v (fallthrough too slow)", wait, 4*fallthroughT)
	}
	snap := g.Snapshot()
	if snap.FallthroughHits != 1 {
		t.Errorf("fallthrough hits = %d, want 1", snap.FallthroughHits)
	}
}

// TestAnalysisGate_CtxCancellation confirms that a blocked acquire
// returns ctx.Err() promptly when ctx is canceled.
func TestAnalysisGate_CtxCancellation(t *testing.T) {
	const workerCap = 2
	g := newAnalysisGate(workerCap, 0, 0)

	relA, errA := g.acquire(context.Background(), "a")
	if errA != nil {
		t.Fatalf("acquire a: %v", errA)
	}
	defer relA()
	relB, errB := g.acquire(context.Background(), "b")
	if errB != nil {
		t.Fatalf("acquire b: %v", errB)
	}
	defer relB()

	ctx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan struct{})
	var gotErr error
	go func() {
		_, gotErr = g.acquire(ctx, "c")
		close(doneCh)
	}()

	// Give the goroutine a moment to enter the wait.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-doneCh:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("acquire did not return after ctx cancellation")
	}
	if gotErr != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", gotErr)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	negative := i < 0
	if negative {
		i = -i
	}
	var buf [20]byte
	bi := len(buf)
	for i > 0 {
		bi--
		buf[bi] = byte('0' + i%10)
		i /= 10
	}
	if negative {
		bi--
		buf[bi] = '-'
	}
	return string(buf[bi:])
}
