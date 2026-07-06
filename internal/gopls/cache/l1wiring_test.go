// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"context"
	"os"
	"testing"

	"golang.org/x/tools/go/analysis"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// fakeAnalyzer returns a minimal *analysis.Analyzer suitable for L1
// action-ID derivation. Run is set to a stub; we never call it.
func fakeAnalyzer(name string) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: name,
		Doc:  "fake",
		Run:  func(*analysis.Pass) (any, error) { return nil, nil },
	}
}

// newFakeAction builds an action plumbed against a typeCheckBatch with
// the given L1 wiring. The package's ph.key is set deterministically so
// repeated calls produce identical action IDs.
func newFakeAction(t *testing.T, b *typeCheckBatch, analyzerName, pkgID string, keySeed byte) *action {
	t.Helper()
	var key file.Hash
	for i := range key {
		key[i] = keySeed
	}
	ph := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID(pkgID),
			PkgPath: metadata.PackagePath(pkgID),
		},
		key: key,
	}
	an := &analysisNode{
		batch: b,
		ph:    ph,
	}
	a := fakeAnalyzer(analyzerName)
	return &action{
		a:          a,
		stableName: analyzerName,
		an:         an,
	}
}

// TestL1ActionIDDeterministic — same action → same actionID across calls.
func TestL1ActionIDDeterministic(t *testing.T) {
	b := &typeCheckBatch{
		l1ToolVer: "test",
	}
	act := newFakeAction(t, b, "fake", "example.com/p", 7)
	id1 := act.l1ActionID()
	id2 := act.l1ActionID()
	if id1 != id2 {
		t.Errorf("non-deterministic actionID: %x vs %x", id1, id2)
	}
}

// TestL1ActionIDInputSensitive — changing ph.key changes the actionID.
func TestL1ActionIDInputSensitive(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	a1 := newFakeAction(t, b, "fake", "example.com/p", 1).l1ActionID()
	a2 := newFakeAction(t, b, "fake", "example.com/p", 2).l1ActionID()
	if a1 == a2 {
		t.Errorf("ph.key change did not propagate to actionID")
	}
}

// TestL1ActionIDAnalyzerSensitive — changing analyzer name changes the actionID.
func TestL1ActionIDAnalyzerSensitive(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	a1 := newFakeAction(t, b, "errcheck", "example.com/p", 1).l1ActionID()
	a2 := newFakeAction(t, b, "ineffassign", "example.com/p", 1).l1ActionID()
	if a1 == a2 {
		t.Errorf("analyzer name did not propagate to actionID")
	}
}

// TestL1ActionIDToolVersionSensitive — different ToolVersion produces
// different action IDs (so a plaid-lint binary upgrade invalidates
// the L1 cache).
func TestL1ActionIDToolVersionSensitive(t *testing.T) {
	b1 := &typeCheckBatch{l1ToolVer: "v1"}
	b2 := &typeCheckBatch{l1ToolVer: "v2"}
	a1 := newFakeAction(t, b1, "fake", "example.com/p", 1).l1ActionID()
	a2 := newFakeAction(t, b2, "fake", "example.com/p", 1).l1ActionID()
	if a1 == a2 {
		t.Errorf("ToolVersion did not propagate to actionID")
	}
}

// TestL1ActionIDVdepFactsSensitive — a vdep summary's FactsHash feeds
// into DepFactsDigest. Changing the FactsHash flips the actionID.
func TestL1ActionIDVdepFactsSensitive(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "fake", "example.com/p", 1)

	var depKey file.Hash
	for i := range depKey {
		depKey[i] = 99
	}
	depPH := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID("example.com/dep"),
			PkgPath: metadata.PackagePath("example.com/dep"),
		},
		key: depKey,
	}
	depAN := &analysisNode{
		batch: b,
		ph:    depPH,
		actions: actionMap{
			"fake": &actionSummary{
				FactsHash: [32]byte{1, 2, 3},
			},
		},
	}
	act.vdeps = map[PackageID]*analysisNode{"example.com/dep": depAN}
	a1 := act.l1ActionID()

	// Change the FactsHash → actionID must change.
	depAN.actions["fake"].FactsHash[0] = 0xff
	a2 := act.l1ActionID()
	if a1 == a2 {
		t.Errorf("vdep FactsHash change did not propagate to actionID")
	}
}

// TestL1WiringRoundTrip — store a synthetic actionSummary in L1, then
// look it up and verify the cached diagnostics + facts round-trip.
func TestL1WiringRoundTrip(t *testing.T) {
	cacheDir := t.TempDir()
	l1, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}

	metrics := &l1Metrics{}
	b := &typeCheckBatch{
		l1:        l1,
		l1ToolVer: "plaid-lint-test",
		l1Metrics: metrics,
	}
	act := newFakeAction(t, b, "fake", "example.com/p", 1)
	act.pkg = &analysisPackage{factsDecoder: nil}

	// Miss before any store.
	if result, summary, ok := act.tryL1Lookup(context.Background()); ok || summary != nil || result != nil {
		t.Errorf("pre-store lookup: want miss, got hit (result=%+v, summary=%+v)", result, summary)
	}
	if got := metrics.misses.Load(); got != 1 {
		t.Errorf("misses after empty lookup = %d, want 1", got)
	}

	// Store a synthetic summary. Facts blob is empty — empty is
	// well-formed per facts.IsWellFormed.
	in := &actionSummary{
		Diagnostics: []gobDiagnostic{
			{Source: "fake", Message: "stub-1", Code: "x"},
			{Source: "fake", Message: "stub-2", Code: "y"},
		},
		Facts:     nil,
		FactsHash: [32]byte{0xaa},
	}
	act.l1Store(in, nil)
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after one l1Store = %d, want 1", got)
	}

	// Lookup again — should hit and return a summary whose diagnostics
	// match the input.
	_, got, ok := act.tryL1Lookup(context.Background())
	if !ok {
		t.Fatalf("post-store lookup: miss")
	}
	if len(got.Diagnostics) != len(in.Diagnostics) {
		t.Fatalf("diag count: got %d want %d", len(got.Diagnostics), len(in.Diagnostics))
	}
	for i := range in.Diagnostics {
		if got.Diagnostics[i].Message != in.Diagnostics[i].Message {
			t.Errorf("diag[%d].Message: got %q want %q",
				i, got.Diagnostics[i].Message, in.Diagnostics[i].Message)
		}
	}
	if got := metrics.hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1", got)
	}
	if got := metrics.errors.Load(); got != 0 {
		t.Errorf("errors = %d, want 0", got)
	}
}

// TestL1StoreSkipOnExistingEntry verifies that a second l1Store call
// for the same action elides the disk write — warm-mode skip,
// mirroring the L2 skip-on-existing. The first call stores;
// the second call bumps the skipped counter and leaves the on-disk
// entry untouched.
func TestL1StoreSkipOnExistingEntry(t *testing.T) {
	cacheDir := t.TempDir()
	l1, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}

	metrics := &l1Metrics{}
	b := &typeCheckBatch{
		l1:        l1,
		l1ToolVer: "plaid-lint-test",
		l1Metrics: metrics,
	}
	act := newFakeAction(t, b, "fake", "example.com/p", 1)
	act.pkg = &analysisPackage{factsDecoder: nil}

	in := &actionSummary{
		Diagnostics: []gobDiagnostic{
			{Source: "fake", Message: "skip-test", Code: "z"},
		},
		Facts:     nil,
		FactsHash: [32]byte{0xab},
	}

	// First store populates the cache.
	act.l1Store(in, nil)
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after first l1Store = %d, want 1", got)
	}
	if got := metrics.skipped.Load(); got != 0 {
		t.Errorf("skipped after first l1Store = %d, want 0", got)
	}

	// Capture the on-disk file's mtime + size to assert the second
	// store does not touch it.
	id := act.l1ActionID()
	entryPath := l1.L1PathForTest(act.a.Name, id)
	info1, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("stat after first store: %v", err)
	}

	// Second store must skip (entry already present).
	act.l1Store(in, nil)
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after second l1Store = %d, want 1 (skip should not increment)", got)
	}
	if got := metrics.skipped.Load(); got != 1 {
		t.Errorf("skipped after second l1Store = %d, want 1", got)
	}
	info2, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("stat after second store: %v", err)
	}
	if !info1.ModTime().Equal(info2.ModTime()) || info1.Size() != info2.Size() {
		t.Errorf("on-disk entry was rewritten despite skip: before=%v/%d after=%v/%d",
			info1.ModTime(), info1.Size(), info2.ModTime(), info2.Size())
	}
}

// TestL1WiringDisabled — when no L1 is attached, both paths short-circuit.
func TestL1WiringDisabled(t *testing.T) {
	b := &typeCheckBatch{}
	act := newFakeAction(t, b, "fake", "example.com/p", 1)
	act.pkg = &analysisPackage{factsDecoder: nil}

	if result, summary, ok := act.tryL1Lookup(context.Background()); ok || summary != nil || result != nil {
		t.Errorf("disabled lookup: want (nil,nil,false), got (%+v,%+v,%v)", result, summary, ok)
	}
	// l1Store should be a no-op on nil l1.
	act.l1Store(&actionSummary{}, nil)
}

// TestAttachL1 — Cache-level setter records L1 state and L1Metrics
// returns a zero snapshot before any activity.
func TestAttachL1(t *testing.T) {
	// PLAID_DISABLE_GC=1 so clcache.Open does not launch a
	// background GC goroutine that races t.TempDir cleanup.
	t.Setenv("PLAID_DISABLE_GC", "1")
	cacheDir := t.TempDir()
	l1, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	c := New(nil)
	c.AttachL1(l1, "plaid-lint-test")
	if c.l1 != l1 {
		t.Errorf("Cache.l1 not set")
	}
	if c.l1ToolVer != "plaid-lint-test" {
		t.Errorf("ToolVer mismatch: %q", c.l1ToolVer)
	}
	m := c.L1Metrics()
	if m.Hits != 0 || m.Misses != 0 || m.Stores != 0 || m.Errors != 0 {
		t.Errorf("fresh metrics non-zero: %+v", m)
	}
}

// TestAttachL1AfterViewPanics — like AttachL2, AttachL1 is setup-time-only.
func TestAttachL1AfterViewPanics(t *testing.T) {
	// PLAID_DISABLE_GC=1 so clcache.Open does not launch a
	// background GC goroutine that races t.TempDir cleanup.
	t.Setenv("PLAID_DISABLE_GC", "1")
	cacheDir := t.TempDir()
	l1, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	c := New(nil)
	// Simulate a View having been created.
	c.viewCount.Add(1)
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("AttachL1 after View did not panic")
		}
	}()
	c.AttachL1(l1, "test")
}
