// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
)

// TestCacheVersion_BumpInvalidates verifies that bumping a descriptor's
// CacheVersion changes the L1 action ID. The shipped infrastructure is
// just wiring — every wrapper currently ships at 1 — but the
// invariant has to hold for any future bump to do its job.
func TestCacheVersion_BumpInvalidates(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "errcheck", "example.com/p", 1)

	// Without descriptor: baseline action ID.
	idNoDesc := act.l1ActionID()

	// Register a descriptor at CacheVersion 1.
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        act.a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{1} },
		AnalyzerVersion: "test-bin-v1",
		CacheVersion:    1,
	})
	b.l1Registry = registry
	idV1 := act.l1ActionID()
	if idV1 == idNoDesc {
		t.Errorf("registering a descriptor did not change the action ID")
	}

	// Bump to CacheVersion 2 → action ID must change.
	descV2 := &analyzers.AnalyzerDescriptor{
		Analyzer:        act.a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{1} },
		AnalyzerVersion: "test-bin-v1",
		CacheVersion:    2,
	}
	registry.Register(descV2)
	idV2 := act.l1ActionID()
	if idV1 == idV2 {
		t.Errorf("CacheVersion bump (1→2) did not invalidate the L1 action ID")
	}
}

// TestCacheVersion_StableWhenUnchanged verifies that repeated derivation
// at the same CacheVersion produces identical action IDs.
func TestCacheVersion_StableWhenUnchanged(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "errcheck", "example.com/p", 1)
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        act.a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{1} },
		AnalyzerVersion: "test-bin-v1",
		CacheVersion:    1,
	})
	b.l1Registry = registry

	a := act.l1ActionID()
	bID := act.l1ActionID()
	if a != bID {
		t.Errorf("CacheVersion-stable derivation flapped: %x vs %x", a, bID)
	}
}

// TestCacheVersion_DescriptorPropagatesToVersionString verifies that
// analyzerVersionFor folds both the per-wrapper CacheVersion and the
// engine-level EngineCacheVersion into the result. The string format
// is part of the cache-stability contract — keep .cv<N>.e<N> suffix
// stable across releases (a format change would itself need a
// CacheVersion bump).
func TestCacheVersion_DescriptorPropagatesToVersionString(t *testing.T) {
	a := fakeAnalyzer("fake")
	d := &analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{} },
		AnalyzerVersion: "base-version",
		CacheVersion:    3,
	}
	got := analyzerVersionFor(a, d)
	want := "base-version.cv3.e" // engine version varies; just check prefix
	if got[:len(want)] != want {
		t.Errorf("analyzerVersionFor: got %q, want prefix %q", got, want)
	}
	// Stub-fallback path also folds in CacheVersion.
	gotStub := analyzerVersionFor(a, nil)
	if !strings.Contains(gotStub, ".cv0.e") {
		t.Errorf("analyzerVersionFor(nil desc): expected .cv0.e suffix; got %q", gotStub)
	}
}
