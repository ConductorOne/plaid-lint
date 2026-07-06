package cache

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
)

// TestL2WriteThroughCanonicalIdentity is the regression test for the
// cross-flow type-identity gap. The fix is in
// typeCheckBatch.tryL2WriteThrough (check.go): on L2 miss for package P,
// we type-check P for import, encode its export data, and round-trip
// that data through gcexportdata.Read against the batch-shared
// b.l2Imports map. The round-trip guarantees that if a concurrent L2
// decode of some other entry already synthesized a stub
// *types.Package for P's path via gcexportdata.Read's internal doPkg,
// the returned package IS that stub (now populated with P's exports).
//
// Pre-fix, the equivalent code returned a fresh *types.NewPackage for
// P, producing two competing *types.Package instances for the same
// path within one batch. Type-checks that consumed both instances
// rejected assignments with "cannot use X as X" errors, which flipped
// apkg.compiles to false and silently dropped every analyzer's
// diagnostics for the consuming package. The bug fired
// intermittently under GC + CPU pressure on smaller workspaces; on
// c1-scale it fires deterministically.
//
// This test asserts the canonicalization invariant the fix relies on:
// the *types.Package returned by clcache.ReadExportData against a
// shared imports map IS the canonical instance for that path. A
// "pre-fix sim" leg constructs the same package via a fresh
// types.NewPackage to demonstrate that the same code without the
// round-trip produces a distinct *types.Package pointer.
func TestL2WriteThroughCanonicalIdentity(t *testing.T) {
	srcDir := t.TempDir()
	pkgcDir := filepath.Join(srcDir, "pkgc")
	if err := os.MkdirAll(pkgcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkgcSrc := `package pkgc

type Token struct{ X int }
`
	if err := os.WriteFile(filepath.Join(pkgcDir, "pkgc.go"), []byte(pkgcSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	pkgcFile, err := parser.ParseFile(fset, filepath.Join(pkgcDir, "pkgc.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse pkgc: %v", err)
	}
	pkgcConf := types.Config{Importer: importer.Default()}
	pkgcPkg, err := pkgcConf.Check("example.com/pkgc", fset, []*ast.File{pkgcFile}, nil)
	if err != nil {
		t.Fatalf("type-check pkgc: %v", err)
	}

	exportData, err := clcache.EncodeExportData(fset, pkgcPkg)
	if err != nil {
		t.Fatalf("EncodeExportData: %v", err)
	}

	// --- Stage 1: post-fix canonicalization. ---
	//
	// Simulate the state inside tryL2WriteThrough at the moment it
	// calls clcache.ReadExportData: a concurrent L2 decode of some
	// other entry already saw "example.com/pkgc" referenced in its
	// blob and synthesized a stub via doPkg. The stub lives in
	// b.l2Imports keyed by its path.
	preExistingStub := types.NewPackage("example.com/pkgc", "pkgc")
	l2Imports := map[string]*types.Package{
		"example.com/pkgc": preExistingStub,
	}

	got, err := clcache.ReadExportData(fset, l2Imports, "example.com/pkgc", exportData)
	if err != nil {
		t.Fatalf("ReadExportData (post-fix): %v", err)
	}
	if got != preExistingStub {
		t.Errorf("post-fix canonicalization broken: ReadExportData returned %p, want pre-existing stub %p",
			got, preExistingStub)
	}
	if l2Imports["example.com/pkgc"] != preExistingStub {
		t.Errorf("post-fix canonicalization broken: l2Imports[%q] = %p, want %p",
			"example.com/pkgc", l2Imports["example.com/pkgc"], preExistingStub)
	}
	// The stub must now actually carry the package's exports
	// (Token.X). Without this, "single instance" would be meaningless
	// because the canonical instance would have no scope.
	if obj := got.Scope().Lookup("Token"); obj == nil {
		t.Errorf("post-fix canonicalization left stub unpopulated: Token not in scope")
	}

	// --- Stage 2: pre-fix sim — fresh types.NewPackage for the
	// same path returns a distinct pointer. ---
	//
	// This leg simulates the pre-fix getImportPackage code path
	// (returning a fresh *types.NewPackage for P without
	// round-tripping through gcexportdata.Read against the shared
	// imports map). It establishes that the canonicalization is the
	// load-bearing mechanism: without it, a second *types.Package
	// for the same path exists within the batch.
	preFixSimFresh := types.NewPackage("example.com/pkgc", "pkgc")
	if preFixSimFresh == preExistingStub {
		t.Fatalf("pre-fix sim: types.NewPackage returned identity-equal instance (impossible)")
	}
	if preFixSimFresh == got {
		t.Errorf("pre-fix sim: fresh types.NewPackage matched canonical pointer (impossible)")
	}
}

// TestL2WriteThroughFreshImportsMap demonstrates the dual: when the
// imports map does NOT yet contain a stub for P's path,
// clcache.ReadExportData inserts a freshly-created *types.Package
// into the map under that path. Subsequent reads against the same
// imports map for the same path return that same instance. This is
// the second half of the canonicalization invariant: every read for a
// given path within a batch sees the same *types.Package.
func TestL2WriteThroughFreshImportsMap(t *testing.T) {
	srcDir := t.TempDir()
	pkgcDir := filepath.Join(srcDir, "pkgc")
	if err := os.MkdirAll(pkgcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pkgcSrc := `package pkgc

type Token struct{ X int }
`
	if err := os.WriteFile(filepath.Join(pkgcDir, "pkgc.go"), []byte(pkgcSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	pkgcFile, err := parser.ParseFile(fset, filepath.Join(pkgcDir, "pkgc.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse pkgc: %v", err)
	}
	pkgcConf := types.Config{Importer: importer.Default()}
	pkgcPkg, err := pkgcConf.Check("example.com/pkgc", fset, []*ast.File{pkgcFile}, nil)
	if err != nil {
		t.Fatalf("type-check pkgc: %v", err)
	}
	exportData, err := clcache.EncodeExportData(fset, pkgcPkg)
	if err != nil {
		t.Fatalf("EncodeExportData: %v", err)
	}

	l2Imports := map[string]*types.Package{}

	first, err := clcache.ReadExportData(fset, l2Imports, "example.com/pkgc", exportData)
	if err != nil {
		t.Fatalf("ReadExportData (first): %v", err)
	}
	if first == nil {
		t.Fatal("first read returned nil package")
	}
	if l2Imports["example.com/pkgc"] != first {
		t.Errorf("imports map not updated: l2Imports[%q] = %p, want %p",
			"example.com/pkgc", l2Imports["example.com/pkgc"], first)
	}

	second, err := clcache.ReadExportData(fset, l2Imports, "example.com/pkgc", exportData)
	if err != nil {
		t.Fatalf("ReadExportData (second): %v", err)
	}
	if second != first {
		t.Errorf("canonicalization broken on second read: got %p, want %p", second, first)
	}
}
