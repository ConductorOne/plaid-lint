// Copyright 2026 The plaid-lint authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// newTestDir creates a temp directory the test owns. The cleanup
// happens automatically when the test finishes.
func newTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "workspace-test-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestNew_returnsUsableWorkspace(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	if got, want := ws.ModuleRoot(), dir; got != want {
		t.Errorf("ModuleRoot = %q, want %q", got, want)
	}

	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot() returned nil for fresh WorkspaceState")
	}
	if snap.ID() == "" {
		t.Error("Snapshot.ID is empty")
	}
	if err := snap.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestSetContent_overlayVisibleAfterInvalidate(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	path := filepath.Join(dir, "a.go")
	const body = "package a\n"
	ws.SetContent(path, []byte(body))

	id := ws.Invalidate([]string{path})
	if id == "" {
		t.Fatal("Invalidate returned empty id")
	}

	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil after Invalidate")
	}
	defer snap.Release()

	if snap.ID() != id {
		t.Errorf("Snapshot.ID = %q, Invalidate returned %q; want them equal", snap.ID(), id)
	}

	// The underlying cache should now see the overlay content.
	inner := snap.Inner()
	uri := protocol.URIFromPath(path)
	fh, err := inner.ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got, err := fh.Content()
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if string(got) != body {
		t.Errorf("overlay content = %q, want %q", got, body)
	}
}

func TestSetContent_nilClearsOverlay(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	path := filepath.Join(dir, "a.go")
	ws.SetContent(path, []byte("package a\n"))
	ws.Invalidate([]string{path})

	// Clear and write the on-disk file with different content;
	// we expect the next snapshot to surface the on-disk view.
	ws.SetContent(path, nil)
	if err := os.WriteFile(path, []byte("package ondisk\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ws.Invalidate([]string{path})

	snap := ws.Snapshot()
	defer snap.Release()
	uri := protocol.URIFromPath(path)
	fh, err := snap.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	got, _ := fh.Content()
	if string(got) != "package ondisk\n" {
		t.Errorf("after clear, content = %q; want on-disk", got)
	}
}

func TestInvalidate_oldSnapshotStillReadable(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	path := filepath.Join(dir, "a.go")
	uri := protocol.URIFromPath(path)
	ws.SetContent(path, []byte("v1"))
	id1 := ws.Invalidate([]string{path})

	oldSnap := ws.Snapshot()
	if oldSnap == nil {
		t.Fatal("Snapshot returned nil")
	}
	defer oldSnap.Release()

	// Read through the old snapshot now to lock its files map
	// into v1 state (the cache snapshots file handles on first
	// access per-snapshot).
	fh, err := oldSnap.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("old snap initial ReadFile: %v", err)
	}
	if got, _ := fh.Content(); string(got) != "v1" {
		t.Fatalf("pre-invalidate read = %q, want v1", got)
	}

	if oldSnap.ID() != id1 {
		t.Errorf("oldSnap.ID = %q, want %q", oldSnap.ID(), id1)
	}

	ws.SetContent(path, []byte("v2"))
	id2 := ws.Invalidate([]string{path})

	if id1 == id2 {
		t.Errorf("expected distinct ids across Invalidate; got %q twice", id1)
	}

	newSnap := ws.Snapshot()
	defer newSnap.Release()
	if newSnap.ID() != id2 {
		t.Errorf("newSnap.ID = %q, want %q", newSnap.ID(), id2)
	}

	// Old snapshot still reports v1 because the cache snapshot
	// holds its own per-snapshot files map keyed on first read.
	fhOld, err := oldSnap.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("old snap post-invalidate ReadFile: %v", err)
	}
	if got, _ := fhOld.Content(); string(got) != "v1" {
		t.Errorf("post-invalidate old snap content = %q, want v1", got)
	}

	// New snapshot reflects v2.
	fhNew, err := newSnap.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("new snap ReadFile: %v", err)
	}
	if got, _ := fhNew.Content(); string(got) != "v2" {
		t.Errorf("new snap content = %q, want v2", got)
	}
}

func TestRelease_doubleReleaseReturnsError(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	if err := snap.Release(); err != nil {
		t.Errorf("first Release: %v", err)
	}
	if err := snap.Release(); !errors.Is(err, workspace.ErrReleased) {
		t.Errorf("second Release = %v, want ErrReleased", err)
	}
}

func TestRelease_freesResources(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)

	const n = 8
	snaps := make([]*workspace.Snapshot, n)
	for i := range snaps {
		snaps[i] = ws.Snapshot()
		if snaps[i] == nil {
			t.Fatalf("Snapshot %d: nil", i)
		}
	}
	for _, s := range snaps {
		if err := s.Release(); err != nil {
			t.Errorf("Release: %v", err)
		}
	}
	// Close drops the WS-owned ref; with n external refs now
	// also released, the underlying snapshot's refcount hits
	// zero and the parseCache goroutine exits.
	ws.Close()
}

func TestClose_idempotent(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	ws.Close()
	ws.Close() // must not panic
	if snap := ws.Snapshot(); snap != nil {
		t.Errorf("Snapshot after Close = %v, want nil", snap)
	}
}

func TestSetContent_overlaysSurviveAcrossSnapshots(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	pathA := filepath.Join(dir, "a.go")
	pathB := filepath.Join(dir, "b.go")
	ws.SetContent(pathA, []byte("package a"))
	ws.SetContent(pathB, []byte("package b"))
	ws.Invalidate([]string{pathA, pathB})

	// Change only A; B's overlay must remain visible.
	ws.SetContent(pathA, []byte("package a2"))
	ws.Invalidate([]string{pathA})

	snap := ws.Snapshot()
	defer snap.Release()

	uriB := protocol.URIFromPath(pathB)
	fh, err := snap.Inner().ReadFile(context.Background(), uriB)
	if err != nil {
		t.Fatalf("ReadFile B: %v", err)
	}
	got, _ := fh.Content()
	if string(got) != "package b" {
		t.Errorf("B overlay content = %q, want %q", got, "package b")
	}
}

// TestConcurrent_snapshotReleaseInvalidate exercises the lifecycle
// from multiple goroutines. It's the main go-test-race target.
func TestConcurrent_snapshotReleaseInvalidate(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	path := filepath.Join(dir, "a.go")
	ws.SetContent(path, []byte("v0"))
	ws.Invalidate([]string{path})

	const goroutines = 8
	const opsPerG = 50

	var wg sync.WaitGroup
	var invalidations atomic.Int64
	var snapshots atomic.Int64

	// invalidator goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < opsPerG; i++ {
			ws.SetContent(path, []byte("v"))
			ws.Invalidate([]string{path})
			invalidations.Add(1)
		}
	}()

	// snapshot+release goroutines
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerG; i++ {
				s := ws.Snapshot()
				if s == nil {
					continue
				}
				snapshots.Add(1)
				// touch the snapshot to provoke any
				// races with concurrent Invalidate.
				_ = s.ID()
				_ = s.Release()
			}
		}()
	}

	wg.Wait()

	if invalidations.Load() == 0 || snapshots.Load() == 0 {
		t.Errorf("no work happened: inv=%d snap=%d", invalidations.Load(), snapshots.Load())
	}
}

// TestInvalidate_propagatesDeletion verifies that removing a file
// from disk and then invalidating its path produces a successor
// snapshot in which the file is no longer readable, while leaving
// any pre-deletion snapshot's view of the file intact.
func TestInvalidate_propagatesDeletion(t *testing.T) {
	dir := newTestDir(t)
	path := filepath.Join(dir, "foo.go")
	const body = "package foo\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ws := workspace.New(dir)
	defer ws.Close()
	uri := protocol.URIFromPath(path)

	snap1 := ws.Snapshot()
	if snap1 == nil {
		t.Fatal("Snapshot 1 returned nil")
	}
	defer snap1.Release()

	// Read once so snap1 has a record of the file existing.
	fh1, err := snap1.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("snap1 ReadFile: %v", err)
	}
	if got, err := fh1.Content(); err != nil || string(got) != body {
		t.Fatalf("snap1 content = %q, err = %v; want %q, nil", got, err, body)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if id := ws.Invalidate([]string{path}); id == "" {
		t.Fatal("Invalidate returned empty id")
	}

	snap2 := ws.Snapshot()
	if snap2 == nil {
		t.Fatal("Snapshot 2 returned nil")
	}
	defer snap2.Release()

	if snap1.ID() == snap2.ID() {
		t.Errorf("Invalidate did not produce a new snapshot id")
	}

	// snap2 must NOT report the old content. The cache may either
	// surface an error from ReadFile or return a handle whose
	// Content errors with fs.ErrNotExist; both are acceptable
	// "deletion propagated" signals — what is NOT acceptable is
	// returning the pre-delete body.
	fh2, err := snap2.Inner().ReadFile(context.Background(), uri)
	if err == nil {
		got, cerr := fh2.Content()
		if cerr == nil && string(got) == body {
			t.Errorf("snap2 still sees old content %q after delete", got)
		}
	}

	// snap1 still reflects the pre-delete state (per the
	// per-snapshot file-handle invariant exercised in
	// TestInvalidate_oldSnapshotStillReadable). Confirm we did not
	// regress that invariant while wiring deletion through.
	fh1b, err := snap1.Inner().ReadFile(context.Background(), uri)
	if err != nil {
		t.Fatalf("snap1 re-ReadFile: %v", err)
	}
	if got, _ := fh1b.Content(); string(got) != body {
		t.Errorf("snap1 lost pre-delete content: got %q, want %q", got, body)
	}
}

// TestSnapshot_idMatchesInvalidate asserts the contract that
// Invalidate's return value equals the next Snapshot().ID().
func TestSnapshot_idMatchesInvalidate(t *testing.T) {
	dir := newTestDir(t)
	ws := workspace.New(dir)
	defer ws.Close()

	for i := 0; i < 5; i++ {
		path := filepath.Join(dir, "x.go")
		ws.SetContent(path, []byte("package x"))
		id := ws.Invalidate([]string{path})
		s := ws.Snapshot()
		if s.ID() != id {
			t.Errorf("round %d: Snapshot.ID=%q, Invalidate=%q", i, s.ID(), id)
		}
		_ = s.Release()
	}
}

// writeTestModule materializes a one-package module rooted at dir with a
// non-test file and a sibling `_test.go`. The returned paths point at the
// two files on disk.
func writeTestModule(t *testing.T, dir string) (nonTest, testFile string) {
	t.Helper()
	const goMod = "module example.com/m\n\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nonTest = filepath.Join(dir, "a.go")
	testFile = filepath.Join(dir, "a_test.go")
	const aGo = "package a\n\nfunc F() int { return 0 }\n"
	const aTestGo = "package a\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n"
	if err := os.WriteFile(nonTest, []byte(aGo), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(testFile, []byte(aTestGo), 0o644); err != nil {
		t.Fatalf("write a_test.go: %v", err)
	}
	return nonTest, testFile
}

// hasTestVariant reports whether the workspace package map contains a
// test-variant entry (one whose metadata.ForTest is non-empty) for at
// least one workspace package. The test variant is what carries
// `_test.go` files into the analyzer pass.
func hasTestVariant(t *testing.T, ws *workspace.WorkspaceState) bool {
	t.Helper()
	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	defer snap.Release()
	inner := snap.Inner()
	if err := inner.InitializeWorkspace(context.Background()); err != nil {
		t.Fatalf("InitializeWorkspace: %v", err)
	}
	wsPkgs := inner.WorkspacePackages()
	for id := range wsPkgs.All() {
		mp := inner.Metadata(id)
		if mp == nil {
			continue
		}
		if mp.ForTest != "" {
			return true
		}
	}
	return false
}

// TestLoader_IncludesTestFiles_Default exercises the default loader
// behavior: with no Tests opt-out in Options, the workspace load must
// produce a test variant package so `_test.go` files reach the
// analyzer pass. Mirrors golangci-lint v2's `run.tests: true` default.
func TestLoader_IncludesTestFiles_Default(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
	dir := newTestDir(t)
	writeTestModule(t, dir)

	ws := workspace.NewWithCacheAndOptions(dir, nil, nil)
	defer ws.Close()

	if !hasTestVariant(t, ws) {
		t.Fatal("expected a test-variant package (ForTest != \"\") in workspace packages with default options; got none")
	}
}

// TestLoader_OptOut covers the inverse: when Options.Tests is set to
// false, the loader skips `_test.go` files and no test-variant package
// appears in the workspace set.
func TestLoader_OptOut(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
	dir := newTestDir(t)
	writeTestModule(t, dir)

	opts := settings.DefaultOptions()
	disabled := false
	opts.Tests = &disabled
	ws := workspace.NewWithCacheAndOptions(dir, nil, opts)
	defer ws.Close()

	if hasTestVariant(t, ws) {
		t.Fatal("expected no test-variant package when Options.Tests=false; got at least one")
	}
}
