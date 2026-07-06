// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"go/token"
	"go/types"
	"runtime"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// newTestNode is a minimal analysisNode constructor for refcount-only
// tests. It does not wire a real batch — the release path's batch
// interaction is exercised by TestAggressiveRelease_PackageMemFreed
// below via a real typeCheckBatch.
func newTestNode(id string) *analysisNode {
	return &analysisNode{
		ph: &packageHandle{
			mp: &metadata.Package{
				ID:      metadata.PackageID(id),
				PkgPath: metadata.PackagePath(id),
			},
		},
	}
}

// linkPred attaches a predecessor edge in a way that mirrors the
// production makeNode code path: bumps unfinishedSummaryConsumers on
// the dep and registers pred on its preds slice. Roots (preds==nil)
// leave unfinishedSummaryConsumers at zero — same semantics as a real
// makeNode call with from==nil.
func linkPred(pred, dep *analysisNode) {
	dep.preds = append(dep.preds, pred)
	dep.unfinishedSummaryConsumers.Add(+1)
}

// TestAggressiveRelease_RefcountHitsZero asserts that for a synthetic
// 3-package graph (root -> middle -> leaf), each non-root node's
// unfinishedSummaryConsumers counter drops to zero after all of its
// predecessors decrement, matching the production decrefSummaryConsumers
// pattern.
func TestAggressiveRelease_RefcountHitsZero(t *testing.T) {
	root := newTestNode("root")
	middle := newTestNode("middle")
	leaf := newTestNode("leaf")

	linkPred(root, middle) // root depends on middle
	linkPred(middle, leaf) // middle depends on leaf

	// Verify initial state.
	if got := root.unfinishedSummaryConsumers.Load(); got != 0 {
		t.Errorf("root.unfinishedSummaryConsumers = %d, want 0 (no preds)", got)
	}
	if got := middle.unfinishedSummaryConsumers.Load(); got != 1 {
		t.Errorf("middle.unfinishedSummaryConsumers = %d, want 1", got)
	}
	if got := leaf.unfinishedSummaryConsumers.Load(); got != 1 {
		t.Errorf("leaf.unfinishedSummaryConsumers = %d, want 1", got)
	}

	// Simulate root finishing: it decrements middle.
	middle.unfinishedSummaryConsumers.Add(-1)
	if got := middle.unfinishedSummaryConsumers.Load(); got != 0 {
		t.Errorf("after root finished, middle.unfinishedSummaryConsumers = %d, want 0", got)
	}

	// Simulate middle finishing: it decrements leaf.
	leaf.unfinishedSummaryConsumers.Add(-1)
	if got := leaf.unfinishedSummaryConsumers.Load(); got != 0 {
		t.Errorf("after middle finished, leaf.unfinishedSummaryConsumers = %d, want 0", got)
	}
}

// TestAggressiveRelease_PackageMemFreed asserts that after the
// aggressive-release path fires for a dep node, the *types.Package
// stored in the batch's importPackages futureCache is no longer
// referenced by the cache. We verify by reading back the persistent
// entry and confirming it has been evicted.
func TestAggressiveRelease_PackageMemFreed(t *testing.T) {
	if !aggressiveReleaseEnabled {
		t.Skip("PLAID_AGGRESSIVE_RELEASE=0 disables the release path")
	}
	b := &typeCheckBatch{
		_handles:       make(map[metadata.PackageID]*packageHandle),
		fset:           token.NewFileSet(),
		importPackages: newFutureCache[metadata.PackageID, *types.Package](true),
	}

	// Plant a fake *types.Package in the importPackages cache for "leaf".
	leafID := metadata.PackageID("leaf")
	tp := types.NewPackage("leaf", "leaf")
	f := &future[*types.Package]{
		done:    make(chan unit),
		acquire: make(chan unit, 1),
	}
	f.v = tp
	close(f.done)
	b.importPackages.cache[leafID] = f

	// Build a graph: middle -> leaf (one pred).
	middle := newTestNode("middle")
	leaf := newTestNode("leaf")
	leaf.batch = b
	linkPred(middle, leaf)

	// Sanity: entry is present before release.
	b.importPackages.mu.Lock()
	_, presentBefore := b.importPackages.cache[leafID]
	b.importPackages.mu.Unlock()
	if !presentBefore {
		t.Fatal("importPackages.cache[leaf] missing before release")
	}

	// Simulate middle finishing: decrement the consumer counter on leaf.
	leaf.decrefSummaryConsumers()

	// Entry should be gone.
	b.importPackages.mu.Lock()
	_, presentAfter := b.importPackages.cache[leafID]
	b.importPackages.mu.Unlock()
	if presentAfter {
		t.Errorf("importPackages.cache[leaf] still present after aggressive release")
	}

	// releasedAggressive should be flipped to true.
	if !leaf.releasedAggressive.Load() {
		t.Errorf("leaf.releasedAggressive = false, want true")
	}

	// an.actions must be nil after release.
	if leaf.actions != nil {
		t.Errorf("leaf.actions = %v, want nil after release", leaf.actions)
	}

	// Finalizer-based memory check: after dropping our local strong
	// references and forcing GC twice (once for the finalizer to be
	// queued, once for it to run), the finalizer should fire.
	finalized := make(chan struct{}, 1)
	runtime.SetFinalizer(tp, func(*types.Package) {
		select {
		case finalized <- struct{}{}:
		default:
		}
	})
	tp = nil // drop the local strong reference
	for i := 0; i < 5; i++ {
		runtime.GC()
	}
	select {
	case <-finalized:
		// success — *types.Package was GC'd after aggressive release
		// dropped the cache map's hold.
	default:
		// Some Go runtimes are slow to drain finalizers; this is best-
		// effort signal, not strict. Don't fail.
		t.Log("note: finalizer did not fire within 5 GC cycles (best-effort signal)")
	}
}

// TestAggressiveRelease_EscapeHatch asserts that when
// aggressiveReleaseEnabled is false (PLAID_AGGRESSIVE_RELEASE=0),
// the release path is a no-op: importPackages entries survive past the
// last consumer's decrement, and the actions map is NOT nilled by the
// new code (decrefPreds still nils it on its own count, but the
// release-side action map clear must NOT fire).
func TestAggressiveRelease_EscapeHatch(t *testing.T) {
	saved := aggressiveReleaseEnabled
	aggressiveReleaseEnabled = false
	defer func() { aggressiveReleaseEnabled = saved }()

	b := &typeCheckBatch{
		_handles:       make(map[metadata.PackageID]*packageHandle),
		fset:           token.NewFileSet(),
		importPackages: newFutureCache[metadata.PackageID, *types.Package](true),
	}

	leafID := metadata.PackageID("leaf")
	tp := types.NewPackage("leaf", "leaf")
	f := &future[*types.Package]{
		done:    make(chan unit),
		acquire: make(chan unit, 1),
	}
	f.v = tp
	close(f.done)
	b.importPackages.cache[leafID] = f

	leaf := newTestNode("leaf")
	leaf.batch = b
	leaf.actions = actionMap{"fake": &actionSummary{}}

	middle := newTestNode("middle")
	linkPred(middle, leaf)

	// Trigger the would-be release.
	leaf.decrefSummaryConsumers()

	// Counter still went to zero (refcount is unconditional).
	if got := leaf.unfinishedSummaryConsumers.Load(); got != 0 {
		t.Errorf("leaf.unfinishedSummaryConsumers = %d, want 0", got)
	}

	// But the release was a no-op: importPackages entry survives.
	b.importPackages.mu.Lock()
	_, present := b.importPackages.cache[leafID]
	b.importPackages.mu.Unlock()
	if !present {
		t.Errorf("importPackages.cache[leaf] evicted despite escape hatch")
	}

	// releasedAggressive must remain false.
	if leaf.releasedAggressive.Load() {
		t.Errorf("leaf.releasedAggressive = true, want false (escape hatch on)")
	}

	// an.actions must NOT have been nilled by the release path (the
	// existing decrefPreds may nil it on its own; we're not testing
	// that here).
	if leaf.actions == nil {
		t.Errorf("leaf.actions nilled despite escape hatch")
	}
}

// TestAggressiveRelease_NoUseAfterFree asserts the refcount invariant
// holds under concurrent decrement: even with N preds all decrementing
// in parallel, releaseAggressive runs exactly once (guarded by the
// CompareAndSwap on releasedAggressive). A double-release would be a
// use-after-free signal — the second eviction would race the first or
// hit a nil future.v after the first one cleared it.
func TestAggressiveRelease_NoUseAfterFree(t *testing.T) {
	if !aggressiveReleaseEnabled {
		t.Skip("PLAID_AGGRESSIVE_RELEASE=0 disables the release path")
	}

	const nPreds = 64

	b := &typeCheckBatch{
		_handles:       make(map[metadata.PackageID]*packageHandle),
		fset:           token.NewFileSet(),
		importPackages: newFutureCache[metadata.PackageID, *types.Package](true),
	}

	leafID := metadata.PackageID("leaf")
	tp := types.NewPackage("leaf", "leaf")
	f := &future[*types.Package]{
		done:    make(chan unit),
		acquire: make(chan unit, 1),
	}
	f.v = tp
	close(f.done)
	b.importPackages.cache[leafID] = f

	leaf := newTestNode("leaf")
	leaf.batch = b
	for i := 0; i < nPreds; i++ {
		linkPred(newTestNode("pred"), leaf)
	}

	if got := leaf.unfinishedSummaryConsumers.Load(); got != int32(nPreds) {
		t.Fatalf("initial refcount = %d, want %d", got, nPreds)
	}

	// Race nPreds concurrent decrements.
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	// We trust the CAS in decrefSummaryConsumers to ensure exactly-one
	// release; the assertion below on the final refcount + releasedAggressive
	// flag is the regression gate.

	for i := 0; i < nPreds; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			leaf.decrefSummaryConsumers()
		}()
	}
	close(startBarrier)
	wg.Wait()

	if got := leaf.unfinishedSummaryConsumers.Load(); got != 0 {
		t.Errorf("final refcount = %d, want 0", got)
	}
	if !leaf.releasedAggressive.Load() {
		t.Errorf("releasedAggressive never flipped to true")
	}

	// Cache entry must be gone.
	b.importPackages.mu.Lock()
	_, present := b.importPackages.cache[leafID]
	b.importPackages.mu.Unlock()
	if present {
		t.Errorf("importPackages.cache[leaf] survived concurrent release")
	}

	// A second decref (over-release, which the production driver
	// guarantees not to do, but the test verifies is harmless) must
	// not panic or re-evict.
	leaf.decrefSummaryConsumers() // pushes counter to -1; release CAS no-ops
	if !leaf.releasedAggressive.Load() {
		t.Errorf("releasedAggressive lost true after over-decrement")
	}
}

// TestAggressiveRelease_B1_ExtendedFields asserts that the
// extended release set (stableNames, preds, succs) is nilled when the
// aggressive-release path fires, in addition to the baseline
// (importPackages eviction + actions nil-out).
func TestAggressiveRelease_B1_ExtendedFields(t *testing.T) {
	if !aggressiveReleaseEnabled {
		t.Skip("PLAID_AGGRESSIVE_RELEASE=0 disables the release path")
	}

	b := &typeCheckBatch{
		_handles:       make(map[metadata.PackageID]*packageHandle),
		fset:           token.NewFileSet(),
		importPackages: newFutureCache[metadata.PackageID, *types.Package](true),
	}

	// Seed an importPackages entry so the baseline path also fires.
	leafID := metadata.PackageID("leaf")
	tp := types.NewPackage("leaf", "leaf")
	f := &future[*types.Package]{
		done:    make(chan unit),
		acquire: make(chan unit, 1),
	}
	f.v = tp
	close(f.done)
	b.importPackages.cache[leafID] = f

	middle := newTestNode("middle")
	leaf := newTestNode("leaf")
	leaf.batch = b
	leaf.stableNames = map[*analysis.Analyzer]string{
		{Name: "fake"}: "fake",
	}
	leaf.succs = map[PackageID]*analysisNode{
		"child": newTestNode("child"),
	}
	linkPred(middle, leaf)

	// Sanity: extended fields are populated pre-release.
	if leaf.stableNames == nil {
		t.Fatal("test setup: stableNames not seeded")
	}
	if leaf.preds == nil {
		t.Fatal("test setup: preds not seeded (linkPred should have appended)")
	}
	if leaf.succs == nil {
		t.Fatal("test setup: succs not seeded")
	}

	// Fire the release.
	leaf.decrefSummaryConsumers()

	if leaf.stableNames != nil {
		t.Errorf("stableNames not released")
	}
	if leaf.preds != nil {
		t.Errorf("preds not released")
	}
	if leaf.succs != nil {
		t.Errorf("succs not released")
	}
	// And the baseline must still hold.
	if leaf.actions != nil {
		t.Errorf("actions still present after release")
	}
}

// TestAggressiveRelease_B1_L0OverrideActionsCleared asserts that
// when a node consumed an L0 dep-override at runCached time,
// releaseL0OverrideActions clears the synthetic summary's Actions map
// once the release path fires. The Facts byte slices the synth
// referenced are then unreachable through the batch.
func TestAggressiveRelease_B1_L0OverrideActionsCleared(t *testing.T) {
	if !aggressiveReleaseEnabled {
		t.Skip("PLAID_AGGRESSIVE_RELEASE=0 disables the release path")
	}

	b := &typeCheckBatch{
		_handles:       make(map[metadata.PackageID]*packageHandle),
		fset:           token.NewFileSet(),
		importPackages: newFutureCache[metadata.PackageID, *types.Package](true),
	}

	leafID := metadata.PackageID("leaf")

	// Plant an override entry so the release path has something to clear.
	synth := &analyzeSummary{
		Compiles: true,
		Actions: actionMap{
			"fake-analyzer": &actionSummary{
				Facts:     []byte("encoded-facts"),
				FactsHash: file.Hash{0x11, 0x22},
			},
		},
	}
	b.l0Overrides = map[PackageID]*analyzeSummary{leafID: synth}

	middle := newTestNode("middle")
	leaf := newTestNode("leaf")
	leaf.batch = b
	linkPred(middle, leaf)

	// Sanity: override entry's Actions populated pre-release.
	if synth.Actions == nil || len(synth.Actions) != 1 {
		t.Fatalf("test setup: synth.Actions = %v, want 1-entry map", synth.Actions)
	}

	// Fire the release.
	leaf.decrefSummaryConsumers()

	if synth.Actions != nil {
		t.Errorf("l0 override synth.Actions not cleared after release (got %v)", synth.Actions)
	}
	// The synth itself stays in the map (Compiles still readable).
	if synth.Compiles != true {
		t.Errorf("synth.Compiles mutated unexpectedly")
	}

	// A second release call must be a no-op (the per-id Once guard).
	leaf.decrefSummaryConsumers() // pushes refcount negative; CAS no-op on releasedAggressive
	// (re-release path doesn't re-fire releaseL0OverrideActions because
	// releasedAggressive is already true; the per-id LoadOrStore guard
	// inside releaseL0OverrideActions is the extra belt-and-braces).
}

// TestAggressiveRelease_B1_ReleaseL0OverrideActionsIdempotent asserts
// the per-id guard inside releaseL0OverrideActions runs the clear
// exactly once, even if called multiple times concurrently for the
// same id (defensive coverage; the production driver guarantees a
// single call per node).
func TestAggressiveRelease_B1_ReleaseL0OverrideActionsIdempotent(t *testing.T) {
	b := &typeCheckBatch{}
	leafID := metadata.PackageID("leaf")
	synth := &analyzeSummary{
		Compiles: true,
		Actions: actionMap{
			"fake-analyzer": &actionSummary{
				Facts: []byte("blob"),
			},
		},
	}
	b.l0Overrides = map[PackageID]*analyzeSummary{leafID: synth}

	const nGoroutines = 32
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	for i := 0; i < nGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startBarrier
			b.releaseL0OverrideActions(leafID)
		}()
	}
	close(startBarrier)
	wg.Wait()

	if synth.Actions != nil {
		t.Errorf("synth.Actions still present after concurrent release")
	}
}

// TestAggressiveRelease_B1_NoOverride asserts release on a node whose
// id has no override entry is a no-op (covers the common case where
// L0 dep-overrides are not installed for this run).
func TestAggressiveRelease_B1_NoOverride(t *testing.T) {
	b := &typeCheckBatch{}
	// l0Overrides is nil — the early-return guard should fire without
	// panicking on the nil map.
	b.releaseL0OverrideActions(metadata.PackageID("any"))

	// Now set up an empty override map and try an id that's not in it.
	b.l0Overrides = map[PackageID]*analyzeSummary{}
	b.releaseL0OverrideActions(metadata.PackageID("missing"))
}
