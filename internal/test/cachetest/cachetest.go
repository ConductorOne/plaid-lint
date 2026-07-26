// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package cachetest opens plaid-lint caches for tests with the
// background-goroutine drain already wired up.
//
// internal/cache's own test binary has an unexported openTestCache
// with the same body, but it is unreachable from every other package.
// This one is a non-test package (no _test.go suffix) so any test in
// the tree can import it. It deliberately depends on internal/cache
// and nothing else: internal/test/harness is the other shared test
// helper, but it imports internal/gopls/cache, which would make it
// an import cycle for that package's own in-package tests.
package cachetest

import (
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
)

// Open opens a cache rooted at dir and registers a cleanup that drains
// the background GC goroutine and closes the handle.
//
// Takes a testing.TB, not *testing.T: internal/bench opens caches from
// benchmarks as well as tests, and Helper/Fatalf/Cleanup are all on TB.
//
// Draining is the load-bearing part. clcache.Open kicks off a GC pass
// on a goroutine that stamps .last-gc at the cache root when it
// finishes; if dir lives under a t.TempDir(), that write races the
// tree walk in RemoveAll and the test fails its own cleanup with
// "directory not empty". Close is defensive — a no-op for the local
// backend, but it tears down a subprocess-backed backend the same way.
//
// Ordering takes care of itself: t.TempDir registers its RemoveAll
// once, at the first call, and t.Cleanup runs LIFO, so a drain
// registered here always runs before it.
//
// Prefer this over setting PLAID_DISABLE_GC=1. Suppressing the GC
// launch also removes it from whatever the test covers, and t.Setenv
// makes the test unparallelizable; draining keeps the production
// open-path intact and just waits for it.
func Open(t testing.TB, dir string) *clcache.Cache {
	t.Helper()
	c, err := clcache.Open(dir)
	if err != nil {
		t.Fatalf("clcache.Open(%q): %v", dir, err)
	}
	t.Cleanup(func() {
		c.WaitForGC()
		_ = c.Close()
	})
	return c
}
