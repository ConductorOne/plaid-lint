// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"log"
	"os"
	"strconv"
	"sync"
)

// M2 buildir dispatch cap.
//
// PLAID_BUILDIR_DISPATCH_CAP gates the number of concurrent calls into
// the buildir analyzer's `pass.Analyzer.Run` entry — i.e. how many distinct
// (analyzer, package) buildir actions may be *dispatched* at once. It does
// NOT bound the live IR working set: once dispatch is admitted, honnef's
// internal `*ir.Program` construction in `ir.(*Package).build` spawns its
// own worker goroutines, and the sibling analysisNode layer can have
// multiple packages mid-build at any moment regardless of this cap.
//
// For strict resident-set control on memory-constrained hardware, use
// `GOMEMLIMIT` (Go runtime soft limit; auto-applied from cgroup)
// and `--concurrency=1` (engine-level analyzer-batch serialization).
// This cap is a CPU-smoothing knob, not a memory-bound knob — benchmarks measured
// a 2.2× cold-seed CPU regression when the cap is unset vs cap=4, attributed
// to GC contention from unlimited buildir parallelism.
//
// Values: unset/empty/"0" = unbounded (default); N>0 caps concurrent
// dispatch at N; negative or non-numeric logs a warning and behaves as
// unset.
const buildirDispatchCapEnv = "PLAID_BUILDIR_DISPATCH_CAP"

var buildirDispatchCap = parseBuildirDispatchCapForTest()

func parseBuildirDispatchCapForTest() int {
	v, ok := os.LookupEnv(buildirDispatchCapEnv)
	if !ok || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("plaid-lint: %s=%q is not a number; treating as unset", buildirDispatchCapEnv, v)
		return 0
	}
	if n < 0 {
		log.Printf("plaid-lint: %s=%d is negative; treating as unset", buildirDispatchCapEnv, n)
		return 0
	}
	return n
}

// buildirSem is nil when cap=0 (acquire is a no-op); else a buffered channel
// of capacity cap acting as the semaphore.
var buildirSem = func() chan struct{} {
	if buildirDispatchCap <= 0 {
		return nil
	}
	return make(chan struct{}, buildirDispatchCap)
}()

var (
	buildirDispatchMu   sync.Mutex
	buildirDispatchCur  int64
	buildirDispatchPeak int64
)

// acquireBuildirSlot blocks until the cap admits this call (no-op when cap=0).
// Returns a release closure the caller MUST invoke exactly once.
func acquireBuildirSlot() func() {
	if buildirSem != nil {
		buildirSem <- struct{}{}
	}
	buildirDispatchMu.Lock()
	buildirDispatchCur++
	if buildirDispatchCur > buildirDispatchPeak {
		buildirDispatchPeak = buildirDispatchCur
	}
	buildirDispatchMu.Unlock()

	return func() {
		buildirDispatchMu.Lock()
		buildirDispatchCur--
		buildirDispatchMu.Unlock()
		if buildirSem != nil {
			<-buildirSem
		}
	}
}

type BuildirDispatchStats struct {
	Cap  int
	Cur  int64
	Peak int64
}

func BuildirDispatchSnapshot() BuildirDispatchStats {
	buildirDispatchMu.Lock()
	defer buildirDispatchMu.Unlock()
	return BuildirDispatchStats{Cap: buildirDispatchCap, Cur: buildirDispatchCur, Peak: buildirDispatchPeak}
}

func ResetBuildirDispatchStats() {
	buildirDispatchMu.Lock()
	buildirDispatchCur = 0
	buildirDispatchPeak = 0
	buildirDispatchMu.Unlock()
}

// SetBuildirDispatchCapForTest swaps in a fresh cap+sem for test scope.
// Preferred over t.Setenv because the env var is parsed once at init.
func SetBuildirDispatchCapForTest(n int) func() {
	prevCap, prevSem := buildirDispatchCap, buildirSem
	buildirDispatchCap = n
	if n <= 0 {
		buildirSem = nil
	} else {
		buildirSem = make(chan struct{}, n)
	}
	ResetBuildirDispatchStats()
	return func() {
		buildirDispatchCap, buildirSem = prevCap, prevSem
		ResetBuildirDispatchStats()
	}
}
