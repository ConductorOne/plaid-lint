// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memlimit

import (
	"os"
)

// ApplyCacheBackend auto-routes plaid's L0/L2 caches through gocacheprog
// when GOCACHEPROG is set, by setting PLAID_CACHE_BACKEND=gocacheprog if
// the user hasn't picked one. The L1 carve-out applies:
// L1 stays local under the default mapping.
//
// Rationale: deployers who configure a gocacheprog side-car (typically
// pointing at an S3-backed wrapper) almost always want plaid's analyzer
// cache to flow through the same path. Cascade-l0-gocacheprog-rebench
// (2026-05-26) measured T2 warm-S3 hit at 77.55s vs 488s cold baseline —
// a 6.3× speedup users only get if their analyzer cache is wired up.
// Auto-detecting GOCACHEPROG removes the second env var they would
// otherwise have to know to set.
//
// No-op when:
//   - PLAID_CACHE_BACKEND is already set (user override; respect it),
//   - PLAID_DISABLE_AUTO_CACHE_BACKEND=1,
//   - GOCACHEPROG is unset or equal to "off" (no side-car wired).
//
// Apply must run before the first cache open in cmd/plaid-lint, since
// the backend selector reads PLAID_CACHE_BACKEND at cache-construction
// time.
func ApplyCacheBackend() {
	if os.Getenv("PLAID_CACHE_BACKEND") != "" {
		return
	}
	if os.Getenv("PLAID_DISABLE_AUTO_CACHE_BACKEND") == "1" {
		return
	}
	gcp := os.Getenv("GOCACHEPROG")
	if gcp == "" || gcp == "off" {
		return
	}
	if err := os.Setenv("PLAID_CACHE_BACKEND", "gocacheprog"); err != nil {
		// os.Setenv only errors on malformed name; this is a hardcoded
		// string so the failure path is unreachable in practice. Log
		// for diagnostics and continue without auto-routing.
		logf("auto-set PLAID_CACHE_BACKEND failed: %v", err)
		return
	}
	logf("auto-set PLAID_CACHE_BACKEND=gocacheprog (GOCACHEPROG=%s)", gcp)
}
