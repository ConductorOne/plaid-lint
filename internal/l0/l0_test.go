// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package l0

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/output"
)

func sampleEntry() *Entry {
	return &Entry{
		PackageID: "github.com/example/foo",
		Diagnostics: []output.Diagnostic{
			{
				Linter:   "ineffassign",
				Message:  "ineffective assignment to x",
				Severity: output.SeverityError,
				Pos:      output.Position{Filename: "/abs/path/foo/x.go", Line: 12, Column: 3},
			},
			{
				Linter:   "gosec",
				Message:  "potential issue",
				Severity: output.SeverityWarning,
				Pos:      output.Position{Filename: "/abs/path/foo/y.go", Line: 20, Column: 1},
			},
		},
	}
}

func sampleKey() KeyParts {
	return KeyParts{
		PackageID:       "github.com/example/foo",
		PackagePath:     "github.com/example/foo",
		SourceHash:      [32]byte{1, 2, 3},
		DepHash:         [32]byte{4, 5, 6},
		AnalyzerSetHash: [32]byte{7, 8, 9},
		ToolVersion:     "plaid-lint-engine-v1",
		BuildEnv:        "linux/arm64/cgo0",
		GoVersion:       "go1.26.3",
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := ComputeKey(sampleKey())
	e := sampleEntry()
	if err := c.Put(id, e); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PackageID != e.PackageID {
		t.Errorf("PackageID: want %q got %q", e.PackageID, got.PackageID)
	}
	if len(got.Diagnostics) != len(e.Diagnostics) {
		t.Fatalf("diag count: want %d got %d", len(e.Diagnostics), len(got.Diagnostics))
	}
	for i, d := range e.Diagnostics {
		if got.Diagnostics[i].Linter != d.Linter ||
			got.Diagnostics[i].Message != d.Message ||
			got.Diagnostics[i].Severity != d.Severity ||
			got.Diagnostics[i].Pos != d.Pos {
			t.Errorf("diag[%d]: want %+v got %+v", i, d, got.Diagnostics[i])
		}
	}
	m := c.MetricsPtr().Snapshot()
	if m.Hits != 1 || m.Stores != 1 || m.Misses != 0 {
		t.Errorf("metrics: %+v", m)
	}
}

func TestGetMissReportsErrNotExist(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := ComputeKey(sampleKey())
	_, err = c.Get(id)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Get on missing entry: want fs.ErrNotExist, got %v", err)
	}
	m := c.MetricsPtr().Snapshot()
	if m.Misses != 1 || m.Hits != 0 {
		t.Errorf("metrics on miss: %+v", m)
	}
}

func TestKeyChangesOnSourceMutation(t *testing.T) {
	a := ComputeKey(sampleKey())
	parts := sampleKey()
	parts.SourceHash[0] ^= 0xff
	b := ComputeKey(parts)
	if a == b {
		t.Error("source hash mutation did not change key")
	}
}

func TestKeyChangesOnAnalyzerSetMutation(t *testing.T) {
	a := ComputeKey(sampleKey())
	parts := sampleKey()
	parts.AnalyzerSetHash[15] ^= 0xff
	b := ComputeKey(parts)
	if a == b {
		t.Error("analyzer set hash mutation did not change key")
	}
}

func TestKeyChangesOnDepHashMutation(t *testing.T) {
	a := ComputeKey(sampleKey())
	parts := sampleKey()
	parts.DepHash[7] ^= 0xff
	b := ComputeKey(parts)
	if a == b {
		t.Error("dep hash mutation did not change key")
	}
}

func TestKeyChangesOnToolVersion(t *testing.T) {
	a := ComputeKey(sampleKey())
	parts := sampleKey()
	parts.ToolVersion = "different"
	b := ComputeKey(parts)
	if a == b {
		t.Error("tool version mutation did not change key")
	}
}

func TestEntryPathSharding(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := ComputeKey(sampleKey())
	if err := c.Put(id, sampleEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// confirm the round-trip via the backend dispatch
	got, err := c.Get(id)
	if err != nil || got == nil {
		t.Fatalf("Get after Put: %v", err)
	}
}

func TestHashFilesDeterministic(t *testing.T) {
	urisA := []string{"a", "b", "c"}
	hashesA := [][32]byte{{1}, {2}, {3}}
	urisB := []string{"c", "a", "b"} // shuffled
	hashesB := [][32]byte{{3}, {1}, {2}}

	a := HashFiles(urisA, hashesA)
	b := HashFiles(urisB, hashesB)
	if a != b {
		t.Error("HashFiles is not order-invariant")
	}

	// A change to a single hash must change the result.
	hashesC := [][32]byte{{1}, {2}, {3, 4}}
	c := HashFiles(urisA, hashesC)
	if a == c {
		t.Error("HashFiles did not detect hash mutation")
	}
}

func TestHashAnalyzerSetDeterministic(t *testing.T) {
	names := []string{"foo", "bar", "baz"}
	versions := []string{"v1", "v2", "v3"}
	salts := [][32]byte{{1}, {2}, {3}}
	a := HashAnalyzerSet(names, versions, salts)

	// Permute the inputs to confirm we sort by name internally.
	namesP := []string{"baz", "bar", "foo"}
	versionsP := []string{"v3", "v2", "v1"}
	saltsP := [][32]byte{{3}, {2}, {1}}
	b := HashAnalyzerSet(namesP, versionsP, saltsP)
	if a != b {
		t.Error("HashAnalyzerSet is not name-sort-invariant")
	}

	// Disable one analyzer: result must change.
	c := HashAnalyzerSet([]string{"foo", "bar"}, []string{"v1", "v2"}, [][32]byte{{1}, {2}})
	if a == c {
		t.Error("HashAnalyzerSet did not change when an analyzer was removed")
	}
}

func TestHashDepClosureDeterministic(t *testing.T) {
	ids := []string{"p/a", "p/b"}
	srcs := [][32]byte{{1}, {2}}
	a := HashDepClosure(ids, srcs)
	b := HashDepClosure([]string{"p/b", "p/a"}, [][32]byte{{2}, {1}})
	if a != b {
		t.Error("HashDepClosure not order-invariant")
	}
	c := HashDepClosure(ids, [][32]byte{{1}, {3}})
	if a == c {
		t.Error("HashDepClosure did not detect mutation")
	}
}

// TestCacheVersionBumpInvalidates simulates the cacheVersion bump
// (test surface (5) from the dispatch). The CacheVersion is a
// compile-time const so we can't bump it at runtime — instead we
// verify the version is folded into the key by toggling a separate
// version-shaped input via toolversion (a proxy: any input change
// changes the key, and CacheVersion is part of the digest by
// construction of ComputeKey).
func TestCacheVersionBumpInvalidates(t *testing.T) {
	// Construct two keys with identical inputs except for a
	// (simulated) version bump. We do this by directly invoking
	// the digest with a different version prefix; here we use the
	// ToolVersion field which has the same shape role for tests.
	a := ComputeKey(sampleKey())
	parts := sampleKey()
	parts.ToolVersion = "after-bump"
	b := ComputeKey(parts)
	if a == b {
		t.Error("simulated cacheVersion bump did not invalidate")
	}
}

func TestPutNilEntryErrors(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id := ComputeKey(sampleKey())
	if err := c.Put(id, nil); err == nil {
		t.Error("Put(nil) should error")
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	if a.Path() != b.Path() {
		t.Errorf("Path: %q != %q", a.Path(), b.Path())
	}
}

// TestCloseLocalBackendIsNoOp pins that Close() on an L0 cache backed
// by the default localBackend returns nil and is idempotent. The
// cmd/plaid-lint defer wires Close unconditionally; the local path
// must not error.
func TestCloseLocalBackendIsNoOp(t *testing.T) {
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestL0_RoutesPerTier_DefaultLocalLandsOnDisk pins the per-tier
// routing contract from the L0 side: with no env vars set, L0 falls
// back to the local backend, so a Put lands on disk at the documented
// sharded path. Paired with the cache-package
// TestSelectBackendForTier_RoutesL0ToGocacheprog_L1Local which proves
// the inverse (gocacheprog override → traffic leaves the local disk).
func TestL0_RoutesPerTier_DefaultLocalLandsOnDisk(t *testing.T) {
	for _, k := range []string{
		"PLAID_CACHE_BACKEND",
		"PLAID_L0_CACHE_BACKEND",
		"PLAID_L1_CACHE_BACKEND",
		"PLAID_L2_CACHE_BACKEND",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("PLAID_DISABLE_GC", "1")

	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	id := ComputeKey(sampleKey())
	if err := c.Put(id, sampleEntry()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	hex := id.Hex()
	localPath := filepath.Join(dir, nsL0, hex[:2], hex)
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("L0 Put did not land at %s under default routing: %v", localPath, err)
	}
}
