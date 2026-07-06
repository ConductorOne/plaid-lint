// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memlimit

import (
	"os"
	"testing"
)

// silenceLogf swaps in a no-op logf so ApplyCacheBackend's stderr line
// doesn't pollute test output. Restored on cleanup.
func silenceLogf(t *testing.T) {
	t.Helper()
	orig := logf
	logf = func(string, ...any) {}
	t.Cleanup(func() { logf = orig })
}

// clearCacheBackendEnv un-sets every env var ApplyCacheBackend reads so
// the test starts from a known baseline.
func clearCacheBackendEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PLAID_CACHE_BACKEND", "")
	t.Setenv("PLAID_DISABLE_AUTO_CACHE_BACKEND", "")
	t.Setenv("GOCACHEPROG", "")
	// t.Setenv with "" leaves the var present with empty value; the
	// production code treats empty as unset (os.Getenv returns ""), so
	// the contract holds.
}

// TestApplyCacheBackend_AutoRoutesWhenGOCACHEPROGSet is the load-bearing
// pin: with GOCACHEPROG set and no PLAID_CACHE_BACKEND, ApplyCacheBackend
// flips the cache backend to gocacheprog. This is the deployer-zero-config
// path: set GOCACHEPROG once and plaid picks it up.
func TestApplyCacheBackend_AutoRoutesWhenGOCACHEPROGSet(t *testing.T) {
	clearCacheBackendEnv(t)
	silenceLogf(t)
	t.Setenv("GOCACHEPROG", "/some/wrapper")
	ApplyCacheBackend()
	if got := os.Getenv("PLAID_CACHE_BACKEND"); got != "gocacheprog" {
		t.Errorf("PLAID_CACHE_BACKEND after Apply: got %q, want %q", got, "gocacheprog")
	}
}

// TestApplyCacheBackend_NoOpWhenCacheBackendSet pins the user-override
// path: any non-empty PLAID_CACHE_BACKEND value (including "local") is
// respected, never overwritten.
func TestApplyCacheBackend_NoOpWhenCacheBackendSet(t *testing.T) {
	clearCacheBackendEnv(t)
	silenceLogf(t)
	t.Setenv("PLAID_CACHE_BACKEND", "local")
	t.Setenv("GOCACHEPROG", "/some/wrapper")
	ApplyCacheBackend()
	if got := os.Getenv("PLAID_CACHE_BACKEND"); got != "local" {
		t.Errorf("PLAID_CACHE_BACKEND after Apply: got %q, want %q (user-override must be respected)", got, "local")
	}
}

// TestApplyCacheBackend_NoOpWhenDisabled pins the opt-out: PLAID_DISABLE_AUTO_CACHE_BACKEND=1
// short-circuits Apply, leaving PLAID_CACHE_BACKEND unset even when
// GOCACHEPROG would have triggered auto-routing.
func TestApplyCacheBackend_NoOpWhenDisabled(t *testing.T) {
	clearCacheBackendEnv(t)
	silenceLogf(t)
	t.Setenv("PLAID_DISABLE_AUTO_CACHE_BACKEND", "1")
	t.Setenv("GOCACHEPROG", "/some/wrapper")
	ApplyCacheBackend()
	if got := os.Getenv("PLAID_CACHE_BACKEND"); got != "" {
		t.Errorf("PLAID_CACHE_BACKEND after Apply with opt-out: got %q, want \"\"", got)
	}
}

// TestApplyCacheBackend_NoOpWhenGOCACHEPROGUnset pins the no-signal path:
// without GOCACHEPROG, Apply doesn't guess a backend.
func TestApplyCacheBackend_NoOpWhenGOCACHEPROGUnset(t *testing.T) {
	clearCacheBackendEnv(t)
	silenceLogf(t)
	ApplyCacheBackend()
	if got := os.Getenv("PLAID_CACHE_BACKEND"); got != "" {
		t.Errorf("PLAID_CACHE_BACKEND after Apply with no GOCACHEPROG: got %q, want \"\"", got)
	}
}

// TestApplyCacheBackend_NoOpWhenGOCACHEPROGOff pins the explicit-off path:
// GOCACHEPROG=off is the documented "disable the side-car" value in the
// Go toolchain; Apply must treat it the same as unset.
func TestApplyCacheBackend_NoOpWhenGOCACHEPROGOff(t *testing.T) {
	clearCacheBackendEnv(t)
	silenceLogf(t)
	t.Setenv("GOCACHEPROG", "off")
	ApplyCacheBackend()
	if got := os.Getenv("PLAID_CACHE_BACKEND"); got != "" {
		t.Errorf("PLAID_CACHE_BACKEND after Apply with GOCACHEPROG=off: got %q, want \"\"", got)
	}
}
