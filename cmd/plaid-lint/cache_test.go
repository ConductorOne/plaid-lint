// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearCacheRootEnv unsets every env var cacheRoot consults so each
// test starts from a clean baseline. XDG_CACHE_HOME and HOME defaults
// would otherwise leak across tests.
func clearCacheRootEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PLAID_CACHE_DIR", "")
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "")
}

// TestCacheRoot_PlaidCacheDir_RawPath pins the load-bearing
// precedence: PLAID_CACHE_DIR is used verbatim, no "plaid-lint"
// suffix appended. The user picked the exact path.
func TestCacheRoot_PlaidCacheDir_RawPath(t *testing.T) {
	clearCacheRootEnv(t)
	dir := t.TempDir()
	t.Setenv("PLAID_CACHE_DIR", dir)
	if got := cacheRoot(); got != dir {
		t.Errorf("cacheRoot with PLAID_CACHE_DIR=%q: got %q, want %q (raw, no suffix)", dir, got, dir)
	}
}

// TestCacheRoot_PlaidCacheDir_BeatsGolangciLint pins precedence
// ordering: when both PLAID_CACHE_DIR and GOLANGCI_LINT_CACHE are
// set, the plaid-specific override wins. Lets users co-locate
// golangci-lint and plaid-lint caches on the same root without
// either tool taking the other's value.
func TestCacheRoot_PlaidCacheDir_BeatsGolangciLint(t *testing.T) {
	clearCacheRootEnv(t)
	plaid := t.TempDir()
	golangci := t.TempDir()
	t.Setenv("PLAID_CACHE_DIR", plaid)
	t.Setenv("GOLANGCI_LINT_CACHE", golangci)
	if got := cacheRoot(); got != plaid {
		t.Errorf("cacheRoot: got %q, want %q (PLAID_CACHE_DIR must beat GOLANGCI_LINT_CACHE)", got, plaid)
	}
}

// TestCacheRoot_PlaidCacheDir_BeatsXDGCacheHome pins that
// PLAID_CACHE_DIR also beats XDG_CACHE_HOME, the standard Linux
// fallback. Standard precedence: explicit > compat > convention.
func TestCacheRoot_PlaidCacheDir_BeatsXDGCacheHome(t *testing.T) {
	clearCacheRootEnv(t)
	plaid := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("PLAID_CACHE_DIR", plaid)
	t.Setenv("XDG_CACHE_HOME", xdg)
	if got := cacheRoot(); got != plaid {
		t.Errorf("cacheRoot: got %q, want %q (PLAID_CACHE_DIR must beat XDG_CACHE_HOME)", got, plaid)
	}
}

// TestCacheRoot_GolangciCompatFallback_NoChange pins that removing
// PLAID_CACHE_DIR from the chain doesn't break the existing
// GOLANGCI_LINT_CACHE → XDG_CACHE_HOME → UserCacheDir → tmp fallback
// order. Regression guard for the existing path.
func TestCacheRoot_GolangciCompatFallback_NoChange(t *testing.T) {
	clearCacheRootEnv(t)
	golangci := t.TempDir()
	t.Setenv("GOLANGCI_LINT_CACHE", golangci)
	if got := cacheRoot(); got != golangci {
		t.Errorf("cacheRoot with only GOLANGCI_LINT_CACHE: got %q, want %q", got, golangci)
	}
}

// TestCacheRoot_XDGCacheHome_AppendsSuffix pins the documented
// behavior that the XDG fallback appends a "plaid-lint" subdir.
// Distinguishes the user-explicit env vars (used raw) from the
// convention-derived paths (suffixed).
func TestCacheRoot_XDGCacheHome_AppendsSuffix(t *testing.T) {
	clearCacheRootEnv(t)
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	want := filepath.Join(xdg, "plaid-lint")
	if got := cacheRoot(); got != want {
		t.Errorf("cacheRoot with XDG_CACHE_HOME=%q: got %q, want %q", xdg, got, want)
	}
}

// TestCacheRoot_EmptyPlaidCacheDir_FallsThrough pins that an
// empty-string value (e.g. `PLAID_CACHE_DIR=` from a misconfigured
// .env file) is treated as unset, not as "use empty path". Standard
// env-var hygiene.
func TestCacheRoot_EmptyPlaidCacheDir_FallsThrough(t *testing.T) {
	clearCacheRootEnv(t)
	golangci := t.TempDir()
	t.Setenv("PLAID_CACHE_DIR", "")
	t.Setenv("GOLANGCI_LINT_CACHE", golangci)
	if got := cacheRoot(); got != golangci {
		t.Errorf("cacheRoot with empty PLAID_CACHE_DIR: got %q, want %q (fallthrough)", got, golangci)
	}
}

// TestCacheRoot_NoEnv_FallsBackToUserCacheDirOrTmp pins the bottom of
// the resolution chain — when nothing's set, we land on UserCacheDir
// (typical) or the tmp fallback (degenerate). The os.UserCacheDir
// fallback is platform-dependent so we assert on the suffix instead
// of the full path.
func TestCacheRoot_NoEnv_FallsBackToUserCacheDirOrTmp(t *testing.T) {
	clearCacheRootEnv(t)
	got := cacheRoot()
	// Either "plaid-lint" (from UserCacheDir) or "plaid-lint-cache"
	// (tmp fallback). Both end in plaid-lint{,-cache}.
	if !strings.HasSuffix(got, "plaid-lint") && !strings.HasSuffix(got, "plaid-lint-cache") {
		t.Errorf("cacheRoot with no env: got %q, want suffix \"plaid-lint\" or \"plaid-lint-cache\"", got)
	}
	if got == "" {
		t.Errorf("cacheRoot returned empty string with no env set")
	}
	_ = os.TempDir // keep import alive on platforms where t.TempDir isn't enough
}
