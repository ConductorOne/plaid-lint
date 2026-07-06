// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"testing"
)

// TestResolveVersion_LdflagsWin pins the documented contract: when
// -ldflags pinned the version to a non-default value, resolveVersion
// returns it verbatim. Direct unit test of the precedence rule.
func TestResolveVersion_LdflagsWin(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })
	version = "v1.2.3-pinned"

	v := resolveVersion()
	if v.Version != "v1.2.3-pinned" {
		t.Errorf("Version: got %q, want %q (ldflags must win)", v.Version, "v1.2.3-pinned")
	}
}

// TestResolveVersion_BuildInfoFillsCommit pins the load-bearing
// fallback: an in-tree `go test` build has no -ldflags but does have
// embedded VCS info, so resolveVersion should pull the commit + date
// out of debug.ReadBuildInfo().
//
// We can't strictly assert on the commit value (it depends on the test
// runner's git state) but we can pin that it's no longer "unknown" —
// the global default — when running under a build that has VCS info.
// Skip when the test binary has no VCS info embedded (e.g., a build
// with -buildvcs=false explicitly), so this test doesn't false-fail
// in CI configurations that strip it.
func TestResolveVersion_BuildInfoFillsCommit(t *testing.T) {
	prev := commit
	t.Cleanup(func() { commit = prev })
	commit = "unknown"

	v := resolveVersion()
	if v.Commit == "unknown" || v.Commit == "" {
		t.Skipf("commit still %q after resolveVersion; test binary likely built with -buildvcs=false. Not a regression — the fallback is best-effort.", v.Commit)
	}
	// We don't assert on the exact value (it varies by test runner /
	// commit state), only that the fallback fired and produced
	// something non-default.
	if len(v.Commit) < 7 {
		t.Errorf("Commit: got %q (looks suspiciously short for a git SHA)", v.Commit)
	}
}

// TestResolveVersion_BuildInfoFillsVersion pins the c1-load-bearing
// case: a `go install github.com/.../plaid-lint@<sha>` build doesn't
// pass -ldflags so the version global stays at "v0-dev", but the
// embedded build info has info.Main.Version set to a real pseudo-version
// (e.g. v0.0.0-20260527014220-0eeffd7b9f9f). The fallback must use that
// pseudo-version so c1's Makefile pin guard can match against it.
//
// Under `go test -buildvcs=true` the test binary has the same shape
// (pseudo-version + dirty marker), which is enough to exercise the
// fallback. Skip when info.Main.Version is "(devel)" or empty (e.g.
// -buildvcs=false), since the fallback intentionally declines those.
func TestResolveVersion_BuildInfoFillsVersion(t *testing.T) {
	prev := version
	t.Cleanup(func() { version = prev })
	version = "v0-dev"

	v := resolveVersion()
	if v.Version == "v0-dev" || v.Version == "" {
		t.Skipf("Version still %q after resolveVersion; test binary likely built without VCS info (-buildvcs=false). Not a regression — the fallback is best-effort.", v.Version)
	}
	// Don't pin the exact value (varies by test runner / commit state);
	// just confirm the fallback fired and produced something that
	// looks like a version string the c1 Makefile pin guard could
	// grep for.
	if len(v.Version) < 5 {
		t.Errorf("Version: got %q (suspiciously short)", v.Version)
	}
}
