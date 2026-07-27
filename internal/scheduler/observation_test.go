// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scheduler

import (
	"runtime"
	"runtime/debug"
	"testing"
)

// TestSamplerFromEnv_KnownSelectors covers every documented
// PLAID_RSS_OBSERVATION value plus the platform default.
func TestSamplerFromEnv_KnownSelectors(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"heapalloc", "heapalloc"},
		{"vmhwm", "vmhwm"},
		{"runtimemetrics", "runtimemetrics"},
		{"noop", "noop"},
	}
	for _, c := range cases {
		got := SamplerFromEnv(c.raw).Name()
		if got != c.want {
			t.Errorf("SamplerFromEnv(%q).Name() = %q, want %q", c.raw, got, c.want)
		}
	}
	// Platform default: vmhwm on linux, heapalloc elsewhere.
	defaultName := SamplerFromEnv("").Name()
	if runtime.GOOS == "linux" {
		if defaultName != "vmhwm" {
			t.Errorf("default on linux = %q, want vmhwm", defaultName)
		}
	} else if defaultName != "heapalloc" {
		t.Errorf("default off-linux = %q, want heapalloc", defaultName)
	}
	// Unknown value falls back to platform default.
	if got := SamplerFromEnv("garbage").Name(); got != defaultName {
		t.Errorf("SamplerFromEnv(garbage) = %q, want platform default %q", got, defaultName)
	}
}

// TestNoopSampler_Zero confirms the noop sampler returns 0 unconditionally.
func TestNoopSampler_Zero(t *testing.T) {
	s := NewNoopSampler()
	if got := s.Delta(s.NewSample()); got != 0 {
		t.Errorf("NoopSampler.Delta = %d, want 0", got)
	}
}

// TestHeapAllocSampler_DetectsAllocation: allocating ~10 MB between
// NewSample and Delta should produce a delta of about that size.
//
// HeapAlloc is a live-heap gauge, not a cumulative counter: reclaim
// landing inside the sample window can drop the "after" reading to or
// below the baseline, and [HeapAllocSampler.Delta] floors at zero.
// Two things have to be pinned to make the window deterministic:
//
//   - debug.SetGCPercent(-1) stops a new cycle from starting.
//   - runtime.GC() then flushes the cycle already in flight, sweep
//     phase included. Disabling GC alone is not enough: HeapAlloc
//     falls during sweep, which runs lazily after the mark completes,
//     so a cycle triggered before the test can still free megabytes
//     mid-window. runtime.GC drives sweepone to completion before it
//     returns, leaving the heap quiescent.
//
// With both in place nothing lowers HeapAlloc, so the 10 MB below is
// still accounted for at Delta time.
func TestHeapAllocSampler_DetectsAllocation(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	runtime.GC()

	const allocBytes = 10 * 1024 * 1024
	// With GC off none of allocBytes can be reclaimed, so the delta is
	// at least that much; the slack only absorbs runtime accounting
	// (span rounding, the sampler's own MemStats work).
	const wantAtLeast = allocBytes - 2*1024*1024

	s := NewHeapAllocSampler()
	before := s.NewSample()
	// Allocate ~10 MB. KeepAlive prevents the compiler from elision-
	// optimising it away before Delta sees it.
	junk := make([]byte, allocBytes)
	for i := range junk {
		junk[i] = byte(i)
	}
	got := s.Delta(before)
	runtime.KeepAlive(junk)
	if got < wantAtLeast {
		t.Errorf("HeapAllocSampler.Delta after %d-byte alloc = %d, want >= %d", allocBytes, got, wantAtLeast)
	}
}

// TestVmHWMSampler_LinuxOnly: on linux, the sampler must return
// non-zero values; on other platforms it returns 0 (the format
// of /proc/self/status differs or the file doesn't exist).
func TestVmHWMSampler_LinuxOnly(t *testing.T) {
	s := NewVmHWMSampler()
	got := s.NewSample()
	v, _ := got.(uint64)
	if runtime.GOOS == "linux" {
		if v == 0 {
			t.Errorf("VmHWMSampler.NewSample on linux = 0, want > 0 (process always has resident memory)")
		}
	}
	// Delta itself never panics regardless of platform.
	if d := s.Delta(got); d != 0 {
		// A small positive delta can occur if the test runner
		// allocates between calls; just log it.
		t.Logf("VmHWMSampler.Delta = %d (informational; the sampler is process-wide)", d)
	}
}

// TestRuntimeMetricsSampler_DetectsAllocation: allocating ~10 MB
// should produce a positive delta (the /gc/heap/allocs:bytes counter
// is monotonic-cumulative).
func TestRuntimeMetricsSampler_DetectsAllocation(t *testing.T) {
	s := NewRuntimeMetricsSampler()
	before := s.NewSample()
	junk := make([]byte, 10*1024*1024)
	for i := range junk {
		junk[i] = byte(i)
	}
	got := s.Delta(before)
	runtime.KeepAlive(junk)
	if got == 0 {
		t.Errorf("RuntimeMetricsSampler.Delta after 10 MB alloc = 0, want > 0")
	}
}

// TestRSSBudgetScheduler_DefaultSamplerInstalled: a freshly-
// constructed scheduler must have the platform default sampler
// installed so production callers see observation without any
// SetSampler call.
func TestRSSBudgetScheduler_DefaultSamplerInstalled(t *testing.T) {
	s := NewRSSBudgetScheduler(1<<20, 4)
	if s.Sampler() == nil {
		t.Error("NewRSSBudgetScheduler installed no default sampler")
	}
}

// TestRSSBudgetScheduler_SetSampler: SetSampler swaps the sampler.
func TestRSSBudgetScheduler_SetSampler(t *testing.T) {
	s := NewRSSBudgetScheduler(1<<20, 4)
	noop := NewNoopSampler()
	s.SetSampler(noop)
	if s.Sampler() != noop {
		t.Error("SetSampler did not replace the sampler")
	}
	s.SetSampler(nil)
	if s.Sampler() != nil {
		t.Error("SetSampler(nil) did not clear the sampler")
	}
}
