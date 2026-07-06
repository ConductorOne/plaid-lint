// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestM2_CapZero_Unbounded: cap=0 must never block — N parallel acquirers
// all reach peak in-flight = N.
func TestM2_CapZero_Unbounded(t *testing.T) {
	defer SetBuildirDispatchCapForTest(0)()

	const n = 8
	var wg sync.WaitGroup
	start, release := make(chan struct{}), make(chan struct{})
	holding := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			defer acquireBuildirSlot()()
			holding <- struct{}{}
			<-release
		}()
	}
	close(start)
	deadline := time.After(2 * time.Second)
	for got := 0; got < n; {
		select {
		case <-holding:
			got++
		case <-deadline:
			t.Fatalf("cap=0 blocked after %d/%d acquires; snap=%+v", got, n, BuildirDispatchSnapshot())
		}
	}
	if snap := BuildirDispatchSnapshot(); snap.Peak < int64(n) {
		t.Errorf("cap=0 peak=%d, want >=%d", snap.Peak, n)
	}
	close(release)
	wg.Wait()
	if snap := BuildirDispatchSnapshot(); snap.Cur != 0 {
		t.Errorf("after release: cur=%d, want 0", snap.Cur)
	}
}

// TestM2_CapNonZero_Throttles: cap=2 with 8 contenders holds peak <= 2.
func TestM2_CapNonZero_Throttles(t *testing.T) {
	const cap = 2
	defer SetBuildirDispatchCapForTest(cap)()

	const n = 8
	var wg sync.WaitGroup
	var acquired atomic.Int64
	start, release := make(chan struct{}), make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			defer acquireBuildirSlot()()
			acquired.Add(1)
			<-release
		}()
	}
	close(start)
	time.Sleep(50 * time.Millisecond) // let the race settle

	snap := BuildirDispatchSnapshot()
	if snap.Peak > int64(cap) {
		t.Errorf("cap=%d peak=%d (sub-gate failed)", cap, snap.Peak)
	}
	if snap.Cur > int64(cap) {
		t.Errorf("cap=%d cur=%d", cap, snap.Cur)
	}
	if got := acquired.Load(); got > int64(cap) {
		t.Errorf("cap=%d but %d goroutines crossed acquire", cap, got)
	}
	close(release)
	wg.Wait()
	if snap := BuildirDispatchSnapshot(); snap.Cur != 0 || snap.Peak > int64(cap) {
		t.Errorf("final snap=%+v", snap)
	}
}

// TestM2_EnvVarParsing pins the env-var contract:
// unset/empty/0 → 0; N>0 → N; negative or non-numeric → 0 (with a warning).
func TestM2_EnvVarParsing(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want int
	}{
		{"", 0}, {"0", 0}, {"4", 4}, {"32", 32},
		{"-1", 0}, {"abc", 0}, {"4x", 0},
	} {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv(buildirDispatchCapEnv, tc.val)
			if got := parseBuildirDispatchCapForTest(); got != tc.want {
				t.Errorf("%q: got %d, want %d", tc.val, got, tc.want)
			}
		})
	}
}
