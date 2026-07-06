// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package memlimit auto-configures the Go runtime's soft memory ceiling
// (`runtime/debug.SetMemoryLimit`) from the running cgroup's memory
// limit at process start.
//
// On c1's ~5800-package workspace the structural *ir.Program graph is
// ~50 GB of working set; without GOMEMLIMIT the cold cascade peaks at
// ~52 GB RSS on a 64 GB cgroup and risks OOM. Apply detects the
// cgroup ceiling and pins the runtime soft limit at 75% of it — the
// validated headroom from the cascade-d-m1on-gomemlimit-48g bench
// (M1+IR fit in 48 GiB on a 64 GiB cgroup).
//
// The default is transparent: users get OOM-safe behavior without
// setting an env var. A user-supplied GOMEMLIMIT wins (Apply no-ops),
// and PLAID_DISABLE_AUTO_GOMEMLIMIT=1 disables the feature entirely.
package memlimit

import (
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// readFile is the package-level filesystem hook used by detectCgroupLimit.
// Tests override it to drive the parser without touching real cgroup paths.
var readFile = os.ReadFile

// logf is the package-level logger used to announce the auto-set limit.
// Tests override it to capture output without going through the global
// log package (which writes to stderr and races with t.Parallel).
var logf = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "plaid-lint: "+format+"\n", args...)
}

// headroomFraction is the share of the cgroup ceiling we hand to the
// Go runtime as GOMEMLIMIT. Validated against cascade-d-m1on-gomemlimit-48g:
// 48 GiB on a 64 GiB cgroup (75%) lets the M1+IR working set fit with
// margin for stack/heap overhead.
const headroomFraction = 0.75

// sanityCeiling treats anything at or above this byte count as the
// cgroup-v1 "no limit" sentinel (the kernel reports values near int64
// max for unbounded cgroups). A linter workload should never want to
// GC against a 1 TiB ceiling; if we see one the host is plainly not
// the cgroup we're running in, so we no-op rather than guess.
const sanityCeiling uint64 = 256 * 1024 * 1024 * 1024 // 256 GiB

// Apply tries to detect the running cgroup's memory limit and pins the
// Go runtime's soft memory ceiling at headroomFraction of it via
// runtime/debug.SetMemoryLimit.
//
// No-op when:
//   - GOMEMLIMIT is set (user override; respect it),
//   - PLAID_DISABLE_AUTO_GOMEMLIMIT=1,
//   - no cgroup ceiling is detectable, or
//   - the detected ceiling looks like the kernel's "unlimited" sentinel.
//
// Apply must run before the first cache or analyzer initialization
// because SetMemoryLimit only affects subsequent allocations.
func Apply() {
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	if os.Getenv("PLAID_DISABLE_AUTO_GOMEMLIMIT") == "1" {
		return
	}
	limit, ok := detectCgroupLimit()
	if !ok {
		return
	}
	target := int64(float64(limit) * headroomFraction)
	if target <= 0 {
		return
	}
	debug.SetMemoryLimit(target)
	logf("auto-set GOMEMLIMIT to %s (%.0f%% of cgroup %s)",
		formatGiB(target), headroomFraction*100, formatGiB(int64(limit)))
}

// detectCgroupLimit returns the running cgroup's memory ceiling in bytes
// or (0, false) if no sane limit is detectable. Tries cgroup v2 first
// (the modern unified hierarchy used by EKS / k8s on recent kernels),
// then falls back to v1's memory.limit_in_bytes.
func detectCgroupLimit() (uint64, bool) {
	if b, err := readFile("/sys/fs/cgroup/memory.max"); err == nil {
		if v, ok := parseV2Limit(b); ok {
			return v, true
		}
	}
	if b, err := readFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		if v, ok := parseV1Limit(b); ok {
			return v, true
		}
	}
	return 0, false
}

// parseV2Limit reads the contents of /sys/fs/cgroup/memory.max. The
// file holds either "max" (no limit) or a decimal byte count.
func parseV2Limit(b []byte) (uint64, bool) {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return saneLimit(v)
}

// parseV1Limit reads /sys/fs/cgroup/memory/memory.limit_in_bytes — a
// decimal byte count. The kernel's v1 "no limit" sentinel is a very
// large number (9223372036854775807 on modern kernels, 9223372036854771712
// on older ones); saneLimit treats anything past sanityCeiling as
// "unlimited" without enumerating the specific sentinels.
func parseV1Limit(b []byte) (uint64, bool) {
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return saneLimit(v)
}

// saneLimit returns the input if it looks like a real cgroup memory
// limit, or (0, false) if it's zero (degenerate) or above sanityCeiling
// (kernel "no limit" sentinel).
func saneLimit(v uint64) (uint64, bool) {
	if v == 0 || v >= sanityCeiling {
		return 0, false
	}
	return v, true
}

// formatGiB renders b as a "%.1f GiB" string for the auto-set log line.
func formatGiB(b int64) string {
	return fmt.Sprintf("%.1f GiB", float64(b)/(1024*1024*1024))
}
