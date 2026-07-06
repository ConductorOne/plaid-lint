package cache

import (
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// TestL2WiringRoundTrip verifies the typeCheckBatch's L2 hooks store
// and look up a *types.Package correctly: a Store followed by a Lookup
// with the same handle key returns a package whose exported names match
// the original. Counters tick exactly once for each event.
func TestL2WiringRoundTrip(t *testing.T) {
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}

	// Build a real *types.Package to round-trip.
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "p.go")
	if err := os.WriteFile(srcPath, []byte(`package p

type T struct{ X int }
func New() T { return T{} }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}

	// Synthesize a minimal packageHandle. The L2 wiring touches only
	// ph.mp.ID, ph.mp.PkgPath, and ph.key.
	var key file.Hash
	for i := range key {
		key[i] = byte(i + 1)
	}
	ph := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID("example.com/p"),
			PkgPath: metadata.PackagePath("example.com/p"),
		},
		key: key,
	}

	metrics := &l2Metrics{}
	b := &typeCheckBatch{
		fset:        fset,
		l2:          l2,
		l2BuildEnv:  "test/test/cgo0",
		l2GoVersion: "go1.26",
		l2ToolVer:   "plaid-lint-test",
		l2Metrics:   metrics,
	}

	// Miss before any store.
	if got, ok := b.tryL2Lookup(context.Background(), ph); ok || got != nil {
		t.Errorf("pre-store lookup: want miss, got hit %v", got)
	}
	if got := metrics.misses.Load(); got != 1 {
		t.Errorf("misses after empty lookup = %d, want 1", got)
	}

	// Collect the per-package *token.File entries for the per-package
	// FileSetSnapshot (the production l2Store does this via the
	// syntaxPackage's parsego files; this test holds the *token.File
	// directly via fset.File on the parsed AST's position).
	pkgFile := fset.File(f.Pos())
	if pkgFile == nil {
		t.Fatalf("fset.File for parsed source returned nil")
	}

	// Store.
	b.l2StoreWithFiles(ph, pkg, []*token.File{pkgFile})
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after one l2Store = %d, want 1", got)
	}

	// Lookup again — should hit and return a usable package with the
	// same exported names.
	got, ok := b.tryL2Lookup(context.Background(), ph)
	if !ok {
		t.Fatalf("post-store lookup: want hit, got miss")
	}
	if got == nil {
		t.Fatalf("post-store lookup: nil package")
	}
	if got.Path() != "example.com/p" {
		t.Errorf("got.Path() = %q, want example.com/p", got.Path())
	}
	if got.Scope().Lookup("T") == nil {
		t.Errorf("rehydrated package missing T")
	}
	if got.Scope().Lookup("New") == nil {
		t.Errorf("rehydrated package missing New")
	}
	if got := metrics.hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1", got)
	}
	if got := metrics.errors.Load(); got != 0 {
		t.Errorf("errors = %d, want 0", got)
	}
}

// TestL2StoreSkipOnExistingEntry verifies that a second l2StoreWithFiles
// call for the same (ph, package) elides the disk write — the warm-mode
// re-write skip. The first call stores; the
// second call bumps the skipped counter and leaves the on-disk entry
// untouched.
func TestL2StoreSkipOnExistingEntry(t *testing.T) {
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "p.go")
	if err := os.WriteFile(srcPath, []byte(`package p

type T struct{ X int }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("example.com/p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	var key file.Hash
	for i := range key {
		key[i] = byte(i + 7)
	}
	ph := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID("example.com/p"),
			PkgPath: metadata.PackagePath("example.com/p"),
		},
		key: key,
	}
	metrics := &l2Metrics{}
	b := &typeCheckBatch{
		fset:        fset,
		l2:          l2,
		l2BuildEnv:  "test/test/cgo0",
		l2GoVersion: "go1.26",
		l2ToolVer:   "plaid-lint-test",
		l2Metrics:   metrics,
	}
	pkgFile := fset.File(f.Pos())
	if pkgFile == nil {
		t.Fatalf("fset.File returned nil")
	}

	// First store populates the cache.
	b.l2StoreWithFiles(ph, pkg, []*token.File{pkgFile})
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after first store = %d, want 1", got)
	}
	if got := metrics.skipped.Load(); got != 0 {
		t.Errorf("skipped after first store = %d, want 0", got)
	}

	// Capture the on-disk file's mtime to assert it is not touched
	// by the second store.
	headerID := b.l2ActionID(ph)
	entryPath := l2.L2PathForTest(headerID)
	info1, err := os.Stat(entryPath)
	if err != nil {
		t.Fatalf("stat after first store: %v", err)
	}

	// Second store must skip (entry already present).
	b.l2StoreWithFiles(ph, pkg, []*token.File{pkgFile})
	if got := metrics.stores.Load(); got != 1 {
		t.Errorf("stores after second store = %d, want 1 (skip should not increment)", got)
	}
	if got := metrics.skipped.Load(); got != 1 {
		t.Errorf("skipped after second store = %d, want 1", got)
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

// TestL2WiringDisabled verifies that a typeCheckBatch with no L2 cache
// is short-circuited cleanly and does not panic if l2Metrics is nil.
func TestL2WiringDisabled(t *testing.T) {
	b := &typeCheckBatch{
		fset: token.NewFileSet(),
		// l2: nil; l2Metrics: nil
	}
	ph := &packageHandle{
		mp: &metadata.Package{
			ID:      metadata.PackageID("example.com/p"),
			PkgPath: metadata.PackagePath("example.com/p"),
		},
	}
	if got, ok := b.tryL2Lookup(context.Background(), ph); ok || got != nil {
		t.Errorf("disabled lookup: want (nil, false), got (%v, %v)", got, ok)
	}
	// l2StoreWithFiles should be a no-op on nil l2 / nil pkg.
	b.l2StoreWithFiles(ph, nil, nil)
	b.l2StoreWithFiles(ph, types.NewPackage("p", "p"), nil)
}

// TestAttachL2 verifies the Cache-level setter records L2 state and
// that L2Metrics returns a zero snapshot before any activity.
func TestAttachL2(t *testing.T) {
	// PLAID_DISABLE_GC=1 so clcache.Open does not launch a
	// background GC goroutine that races t.TempDir cleanup.
	t.Setenv("PLAID_DISABLE_GC", "1")
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	c := New(nil)
	c.AttachL2(l2, "linux/amd64/cgo0", "go1.26", "test")
	if c.l2 != l2 {
		t.Errorf("Cache.l2 not set")
	}
	if c.l2BuildEnv != "linux/amd64/cgo0" {
		t.Errorf("BuildEnv mismatch: %q", c.l2BuildEnv)
	}
	if c.l2GoVersion != "go1.26" {
		t.Errorf("GoVersion mismatch: %q", c.l2GoVersion)
	}
	if c.l2ToolVer != "test" {
		t.Errorf("ToolVer mismatch: %q", c.l2ToolVer)
	}
	m := c.L2Metrics()
	if m.Hits != 0 || m.Misses != 0 || m.Stores != 0 || m.Errors != 0 {
		t.Errorf("fresh metrics non-zero: %+v", m)
	}
}
