// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// addVdep attaches a single vdep to act under id with the given
// reachability key seed. Used by the per-scope tests to vary dep
// ph.key without rebuilding the full action graph.
func addVdep(act *action, id string, keySeed byte) *analysisNode {
	var key file.Hash
	for i := range key {
		key[i] = keySeed
	}
	depPH := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID(id),
			PkgPath: metadata.PackagePath(id),
		},
		key: key,
	}
	depAN := &analysisNode{
		batch: act.an.batch,
		ph:    depPH,
	}
	if act.vdeps == nil {
		act.vdeps = map[PackageID]*analysisNode{}
	}
	act.vdeps[PackageID(id)] = depAN
	return depAN
}

// registerScope is a small helper to install a descriptor with the
// given TypeUseScope on act's batch.
func registerScope(t *testing.T, act *action, scope analyzers.TypeUseScope) {
	t.Helper()
	r := analyzers.NewRegistry()
	r.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        act.a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{} },
		AnalyzerVersion: "test-bin",
		CacheVersion:    1,
		TypeUseScope:    scope,
	})
	act.an.batch.l1Registry = r
}

// TestDepTypeDigest_SyntaxOnlyIgnoresVdepKey is the falsifiable
// per-analyzer assertion of Phase A: a SyntaxOnly analyzer's
// DepTypeDigest must NOT depend on any vdep's ph.key. Changing a
// dep's reachability key leaves the digest unchanged → L1 hits on
// cascade-edits.
func TestDepTypeDigest_SyntaxOnlyIgnoresVdepKey(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "whitespace", "example.com/p", 1)
	addVdep(act, "example.com/dep", 10)
	registerScope(t, act, analyzers.TypeUseSyntaxOnly)

	d1 := act.depTypeDigest()
	// Vary the dep's key — previously this would flip the digest;
	// SyntaxOnly must be invariant.
	for i := range act.vdeps[PackageID("example.com/dep")].ph.key {
		act.vdeps[PackageID("example.com/dep")].ph.key[i] = 99
	}
	d2 := act.depTypeDigest()
	if d1 != d2 {
		t.Errorf("SyntaxOnly depTypeDigest depends on vdep ph.key: %x vs %x", d1, d2)
	}

	// Also: an additional vdep must not change the digest.
	addVdep(act, "example.com/extra-dep", 7)
	d3 := act.depTypeDigest()
	if d1 != d3 {
		t.Errorf("SyntaxOnly depTypeDigest depends on vdep set: %x vs %x", d1, d3)
	}
}

// TestDepTypeDigest_FullTypeGraphReactsToVdepKey is the back-compat
// gate: FullTypeGraph (the default, the unregistered fallback)
// preserves the prior sensitivity to every vdep's ph.key.
func TestDepTypeDigest_FullTypeGraphReactsToVdepKey(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "buildir", "example.com/p", 1)
	addVdep(act, "example.com/dep", 10)
	registerScope(t, act, analyzers.TypeUseFullTypeGraph)

	d1 := act.depTypeDigest()
	for i := range act.vdeps[PackageID("example.com/dep")].ph.key {
		act.vdeps[PackageID("example.com/dep")].ph.key[i] = 99
	}
	d2 := act.depTypeDigest()
	if d1 == d2 {
		t.Errorf("FullTypeGraph depTypeDigest invariant to vdep ph.key: %x", d1)
	}
}

// TestDepTypeDigest_UnregisteredFallsBackToFullTypeGraph pins the
// back-compat contract: an analyzer with NO descriptor (e.g. a test
// analyzer that bypasses the registry) keeps the prior behavior.
func TestDepTypeDigest_UnregisteredFallsBackToFullTypeGraph(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "unregistered", "example.com/p", 1)
	addVdep(act, "example.com/dep", 10)
	// No registry, no descriptor.

	d1 := act.depTypeDigest()
	for i := range act.vdeps[PackageID("example.com/dep")].ph.key {
		act.vdeps[PackageID("example.com/dep")].ph.key[i] = 99
	}
	d2 := act.depTypeDigest()
	if d1 == d2 {
		t.Errorf("unregistered analyzer fell into SyntaxOnly; want FullTypeGraph fallback")
	}
}

// TestDepTypeDigest_SyntaxAndFullAreDistinct is the domain-tag
// invariant: SyntaxOnly's empty-body digest must never collide with
// FullTypeGraph's empty-vdep digest. Catches the accidental-toggle
// bug where flipping the scope leaves the on-disk L1 entry stale-
// hitting.
func TestDepTypeDigest_SyntaxAndFullAreDistinct(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}

	actSyn := newFakeAction(t, b, "fake", "example.com/p", 1)
	registerScope(t, actSyn, analyzers.TypeUseSyntaxOnly)
	dSyn := actSyn.depTypeDigest()

	actFull := newFakeAction(t, b, "fake", "example.com/p", 1)
	// FullTypeGraph default; same batch's registry currently holds
	// SyntaxOnly for this analyzer pointer — register a Full one on
	// a fresh batch to keep scopes separated.
	bFull := &typeCheckBatch{l1ToolVer: "test"}
	actFull.an.batch = bFull
	registerScope(t, actFull, analyzers.TypeUseFullTypeGraph)
	dFull := actFull.depTypeDigest()

	if dSyn == dFull {
		t.Errorf("SyntaxOnly and FullTypeGraph empty-vdep digests collided: %x", dSyn)
	}
}

// setLocalKey assigns ph.localKey deterministically. Used by the
// chain-1 tests to vary the package-local input independently
// from ph.key.
func setLocalKey(act *action, seed byte) {
	for i := range act.an.ph.localKey {
		act.an.ph.localKey[i] = seed
	}
}

// TestInputDigest_FullTypeGraphTracksPHKey is the back-compat gate:
// FullTypeGraph (the default) keys L1 InputDigest off ph.key, so any
// change to ph.key flips the digest. Prior behavior.
func TestInputDigest_FullTypeGraphTracksPHKey(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "buildir", "example.com/p", 1)
	registerScope(t, act, analyzers.TypeUseFullTypeGraph)

	d1 := act.inputDigest()
	// Vary ph.key — InputDigest must flip.
	for i := range act.an.ph.key {
		act.an.ph.key[i] = 99
	}
	d2 := act.inputDigest()
	if d1 == d2 {
		t.Errorf("FullTypeGraph inputDigest invariant to ph.key change: %x", d1)
	}
}

// TestInputDigest_SyntaxOnlyTracksLocalKey is the headline
// assertion: a SyntaxOnly analyzer's InputDigest follows ph.localKey,
// NOT ph.key. So a cascade-edit that flips an importer's ph.key (via
// dep recomposition) but leaves its localKey alone produces a stable
// InputDigest → L1 hit.
func TestInputDigest_SyntaxOnlyTracksLocalKey(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "whitespace", "example.com/p", 1)
	setLocalKey(act, 0x42)
	registerScope(t, act, analyzers.TypeUseSyntaxOnly)

	d1 := act.inputDigest()

	// Simulate a cascade flip: ph.key changes (dep was edited), but
	// ph.localKey of this package did NOT change.
	for i := range act.an.ph.key {
		act.an.ph.key[i] = 99
	}
	d2 := act.inputDigest()
	if d1 != d2 {
		t.Errorf("SyntaxOnly inputDigest tracked ph.key, want localKey: %x vs %x", d1, d2)
	}

	// And conversely: changing ph.localKey MUST flip the digest
	// (otherwise a local-only edit would be missed).
	setLocalKey(act, 0xAA)
	d3 := act.inputDigest()
	if d1 == d3 {
		t.Errorf("SyntaxOnly inputDigest invariant to localKey change: %x", d1)
	}
}

// TestInputDigest_SyntaxOnlyFallsBackWhenLocalKeyUnset pins the
// defensive guard: when ph.localKey is the zero value (e.g. a test
// scaffold that bypasses the check pipeline), SyntaxOnly must still
// produce a well-defined digest — fall back to ph.key.
func TestInputDigest_SyntaxOnlyFallsBackWhenLocalKeyUnset(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "whitespace", "example.com/p", 7)
	// localKey deliberately left zero.
	registerScope(t, act, analyzers.TypeUseSyntaxOnly)

	got := act.inputDigest()
	if got != act.an.ph.key {
		t.Errorf("SyntaxOnly w/ zero localKey did not fall back to ph.key: got %x, want %x", got, act.an.ph.key)
	}
}

// TestInputDigest_ExportedTypesOnlyDegradesToFullTypeGraph pins the
// safety contract for the deferred ExportedTypesOnly path: until the
// per-vdep gcexportdata-keyed InputDigest is wired, the digest
// behaves identically to FullTypeGraph (= ph.key). When the feature
// lands, the assertion direction inverts.
func TestInputDigest_ExportedTypesOnlyDegradesToFullTypeGraph(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}
	act := newFakeAction(t, b, "fake", "example.com/p", 3)
	setLocalKey(act, 0x55)
	registerScope(t, act, analyzers.TypeUseExportedTypesOnly)

	got := act.inputDigest()
	if got != act.an.ph.key {
		t.Errorf("ExportedTypesOnly inputDigest %x != ph.key %x; expected safe degradation", got, act.an.ph.key)
	}
}

// TestDepTypeDigest_ExportedTypesOnlyDegradesToFullTypeGraph pins
// the safety contract: until the per-vdep exported-API hash is
// wired, ExportedTypesOnly behaves identically to FullTypeGraph.
// When the feature lands, the assertion direction inverts.
func TestDepTypeDigest_ExportedTypesOnlyDegradesToFullTypeGraph(t *testing.T) {
	b := &typeCheckBatch{l1ToolVer: "test"}

	actE := newFakeAction(t, b, "fake", "example.com/p", 1)
	addVdep(actE, "example.com/dep", 10)
	registerScope(t, actE, analyzers.TypeUseExportedTypesOnly)
	dE := actE.depTypeDigest()

	actF := newFakeAction(t, &typeCheckBatch{l1ToolVer: "test"}, "fake", "example.com/p", 1)
	addVdep(actF, "example.com/dep", 10)
	registerScope(t, actF, analyzers.TypeUseFullTypeGraph)
	dF := actF.depTypeDigest()

	if dE != dF {
		t.Errorf("ExportedTypesOnly digest %x != FullTypeGraph %x; expected safe degradation", dE, dF)
	}
}
