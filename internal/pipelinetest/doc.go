// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pipelinetest is a test-only host for cross-package integration
// tests of the plaid-lint analysis pipeline. It imports both
// internal/cache and internal/gopls/cache + internal/workspace, which is
// a combination that no production package depends on, so its tests live
// here to avoid creating production import cycles.
package pipelinetest

import (
	"os"
	"testing"
)

// goplsCacheDir returns a fresh temp directory wired up for use as
// GOPLSCACHE. The directory's RemoveAll is best-effort: the
// gopls filecache package starts a GC goroutine on first access
// that races with testing.T's cleanup hook, so the standard
// t.TempDir() pattern emits sporadic "directory not empty"
// errors. The W9 stdlib fix increases the analysis closure
// size (62 stdlib packages for a single fmt-importing fixture),
// which exacerbates the race. Centralising the leaky-cleanup
// pattern here keeps all pipelinetest call sites consistent.
//
// Production code never sees this helper — it's pipelinetest-only.
func goplsCacheDir(t *testing.T) string {
	return leakyTempDir(t, "plaid-gopls-cache-")
}

// leakyTempDir is the GOPLSCACHE-style cleanup helper for any
// directory the gopls cache may write to during or after a test
// run. The View's parseCache GC and the filecache GC are both
// background goroutines whose lifecycle is loosely coupled to
// the snapshot's refcount; an `unlinkat ... directory not empty`
// error from t.TempDir's cleanup hook is the canonical symptom
// of those goroutines still holding open file descriptors when
// the test's outer scope returns.
//
// The W8 baseline used t.TempDir() for modDir / l1Dir / l2Dir
// and accepted the sporadic flake; W9's wider stdlib closure
// (62 packages instead of 4) makes the race visible enough that
// running ./internal/... -race -count=2 reliably hits it. This
// helper plus the goplsCacheDir wrapper above replace t.TempDir
// at every load-bearing test call site.
func leakyTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatalf("MkdirTemp(%q): %v", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
