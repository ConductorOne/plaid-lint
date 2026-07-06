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
	"github.com/conductorone/plaid-lint/internal/gopls/internal/gcimporter"
)

// TestL2SharedImportIdentity is the cross-flow identity regression
// test for the L2 path: two L2 lookups in the same batch that both
// reference a third package via their shallow blobs must canonicalize
// to a single *types.Package for the shared dep.
//
// Earlier history: the original test (Codex review for W5) verified
// that a shared b.l2Imports map produced single identity for the
// shared dep under gcexportdata.Read's lazy-synthesis scheme. The fix
// replaces that mechanism with the b.importPackages futureCache; the
// invariant the test pins is unchanged, but the mechanism under test
// is now the shallow getPackages callback routing through the
// futureCache.
//
// Fixture:
//
//	pkgc: defines type Token struct{ X int }
//	pkga: imports pkgc; exports func GetToken() pkgc.Token
//	pkgb: imports pkgc; exports func TakeToken(t pkgc.Token) bool
//
// We pre-populate L2 with shallow entries for pkga and pkgb (pkgc is
// the shared third package — referenced in both blobs as an
// objectpath.Object reference). In one typeCheckBatch we
// tryL2Lookup both pkga and pkgb, then assert:
//
//  1. Both lookups hit.
//  2. The *types.Package for "example.com/pkgc" referenced by the
//     return type of pkga.GetToken is the SAME pointer as the
//     *types.Package referenced by the parameter type of
//     pkgb.TakeToken. This is the load-bearing invariant.
func TestL2SharedImportIdentity(t *testing.T) {
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}

	// --- Build pkgc, pkga, pkgb. ---
	srcDir := t.TempDir()
	pkgcDir := filepath.Join(srcDir, "pkgc")
	pkgaDir := filepath.Join(srcDir, "pkga")
	pkgbDir := filepath.Join(srcDir, "pkgb")
	for _, d := range []string{pkgcDir, pkgaDir, pkgbDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	pkgcSrc := `package pkgc

type Token struct{ X int }
`
	pkgaSrc := `package pkga

import "example.com/pkgc"

func GetToken() pkgc.Token { return pkgc.Token{} }
`
	pkgbSrc := `package pkgb

import "example.com/pkgc"

func TakeToken(t pkgc.Token) bool { return t.X == 0 }
`
	if err := os.WriteFile(filepath.Join(pkgcDir, "pkgc.go"), []byte(pkgcSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgaDir, "pkga.go"), []byte(pkgaSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgbDir, "pkgb.go"), []byte(pkgbSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- Type-check pkgc first; then pkga and pkgb using pkgc as an
	// importer-resolvable dep. ---
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

	depImporter := importerFromGopls{pkgs: map[string]*types.Package{
		"example.com/pkgc": pkgcPkg,
	}}

	pkgaFile, err := parser.ParseFile(fset, filepath.Join(pkgaDir, "pkga.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse pkga: %v", err)
	}
	pkgaConf := types.Config{Importer: depImporter}
	pkgaPkg, err := pkgaConf.Check("example.com/pkga", fset, []*ast.File{pkgaFile}, nil)
	if err != nil {
		t.Fatalf("type-check pkga: %v", err)
	}

	pkgbFile, err := parser.ParseFile(fset, filepath.Join(pkgbDir, "pkgb.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse pkgb: %v", err)
	}
	pkgbConf := types.Config{Importer: depImporter}
	pkgbPkg, err := pkgbConf.Check("example.com/pkgb", fset, []*ast.File{pkgbFile}, nil)
	if err != nil {
		t.Fatalf("type-check pkgb: %v", err)
	}

	// --- Encode pkga and pkgb into L2 entries as shallow blobs. ---
	storePackage := func(t *testing.T, pkg *types.Package, keyByte byte) (*packageHandle, clcache.ActionID) {
		t.Helper()
		exportData, err := gcimporter.IExportShallow(fset, pkg, nil)
		if err != nil {
			t.Fatalf("IExportShallow(%s): %v", pkg.Path(), err)
		}
		var key file.Hash
		for i := range key {
			key[i] = keyByte
		}
		ph := &packageHandle{
			mp: &metadata.Package{
				ID:      metadata.PackageID(pkg.Path()),
				PkgPath: metadata.PackagePath(pkg.Path()),
				Name:    metadata.PackageName(pkg.Name()),
				DepsByPkgPath: map[metadata.PackagePath]metadata.PackageID{
					"example.com/pkgc": "example.com/pkgc",
				},
			},
			key: key,
		}
		entry := &clcache.L2Entry{
			PackageID:   pkg.Path(),
			GoVersion:   "go1.26",
			BuildEnv:    "test/test/cgo0",
			ToolVersion: "plaid-lint-test",
			ExportData:  exportData,
		}
		copy(entry.InputDigest[:], ph.key[:])
		id := clcache.ComputeL2ActionID(entry)
		if err := l2.WriteL2(entry, id); err != nil {
			t.Fatalf("WriteL2(%s): %v", pkg.Path(), err)
		}
		return ph, id
	}

	phA, _ := storePackage(t, pkgaPkg, 0xa1)
	phB, _ := storePackage(t, pkgbPkg, 0xb2)

	// --- Set up a typeCheckBatch with a futureCache that resolves
	// pkgc to a single canonical *types.Package. Routing every
	// shallow-mode cross-package reference through this cache is what
	// preserves identity.
	batchFset := token.NewFileSet()
	metrics := &l2Metrics{}
	canonicalPkgc := types.NewPackage("example.com/pkgc", "pkgc")
	// Pre-populate Scope so objectpath.Object("Token") can resolve.
	tokenObj := pkgcPkg.Scope().Lookup("Token")
	if tokenObj == nil {
		t.Fatalf("pkgc.Token missing in source pkgcPkg")
	}
	canonicalPkgc.Scope().Insert(tokenObj)
	canonicalPkgc.MarkComplete()
	b := &typeCheckBatch{
		fset:           batchFset,
		l2:             l2,
		l2BuildEnv:     "test/test/cgo0",
		l2GoVersion:    "go1.26",
		l2ToolVer:      "plaid-lint-test",
		l2Metrics:      metrics,
		importPackages: newFutureCache[PackageID, *types.Package](true),
		_handles: map[PackageID]*packageHandle{
			"example.com/pkgc": {
				mp: &metadata.Package{
					ID:      "example.com/pkgc",
					PkgPath: "example.com/pkgc",
					Name:    "pkgc",
				},
				key: file.Hash{0xc3},
			},
		},
	}
	// Seed the futureCache so a getImportPackage("example.com/pkgc")
	// call from inside the shallow callback returns canonicalPkgc
	// rather than re-running the synthesis cost path.
	if _, err := b.importPackages.get(context.Background(), "example.com/pkgc", func(ctx context.Context) (*types.Package, error) {
		return canonicalPkgc, nil
	}); err != nil {
		t.Fatalf("seed importPackages: %v", err)
	}

	ctx := context.Background()

	gotA, okA := b.tryL2Lookup(ctx, phA)
	if !okA {
		t.Fatalf("L2 lookup for pkga: want hit, got miss")
	}
	gotB, okB := b.tryL2Lookup(ctx, phB)
	if !okB {
		t.Fatalf("L2 lookup for pkgb: want hit, got miss")
	}
	if got := metrics.hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}

	// --- Extract the *types.Package referenced by GetToken's return
	// type (pkga) and by TakeToken's parameter type (pkgb). ---
	pkgcFromA := pkgcRefThroughFunc(t, gotA, "GetToken", refKindResult)
	pkgcFromB := pkgcRefThroughFunc(t, gotB, "TakeToken", refKindParam)

	// Sanity: both refer to a package named "pkgc" at path
	// "example.com/pkgc".
	for _, p := range []*types.Package{pkgcFromA, pkgcFromB} {
		if p == nil {
			t.Fatalf("nil pkgc reference")
		}
		if p.Path() != "example.com/pkgc" {
			t.Errorf("pkgc.Path() = %q, want example.com/pkgc", p.Path())
		}
	}

	// --- Load-bearing assertion: same *types.Package pointer. ---
	if pkgcFromA != pkgcFromB {
		t.Errorf("shared dep identity broken: pkgc via pkga = %p, pkgc via pkgb = %p (want equal pointers)",
			pkgcFromA, pkgcFromB)
	}
}

// importerFromGopls is a tiny types.Importer that resolves a fixed
// table. We reimplement (rather than reuse internal/cache's
// importerFromMap) because that helper is in a different package.
type importerFromGopls struct {
	pkgs map[string]*types.Package
}

var _ types.Importer = importerFromGopls{}

func (m importerFromGopls) Import(path string) (*types.Package, error) {
	if p, ok := m.pkgs[path]; ok {
		return p, nil
	}
	return nil, errImporterPath(path)
}

type errImporterPath string

func (e errImporterPath) Error() string { return "importerFromGopls: unknown path " + string(e) }

type refKind int

const (
	refKindResult refKind = iota
	refKindParam
)

// pkgcRefThroughFunc looks up funcName in pkg, asserts it is a
// signature with either a single result (refKindResult) or single
// parameter (refKindParam), peels the Named type out, and returns the
// *types.Package that Named lives in.
func pkgcRefThroughFunc(t *testing.T, pkg *types.Package, funcName string, kind refKind) *types.Package {
	t.Helper()
	obj := pkg.Scope().Lookup(funcName)
	if obj == nil {
		t.Fatalf("%s: %s not in scope", pkg.Path(), funcName)
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		t.Fatalf("%s.%s: want *types.Func, got %T", pkg.Path(), funcName, obj)
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		t.Fatalf("%s.%s: want *types.Signature, got %T", pkg.Path(), funcName, fn.Type())
	}
	var tup *types.Tuple
	switch kind {
	case refKindResult:
		tup = sig.Results()
	case refKindParam:
		tup = sig.Params()
	default:
		t.Fatalf("unknown refKind: %v", kind)
	}
	if tup.Len() != 1 {
		t.Fatalf("%s.%s: want tuple of 1, got %d", pkg.Path(), funcName, tup.Len())
	}
	typ := tup.At(0).Type()
	named, ok := typ.(*types.Named)
	if !ok {
		t.Fatalf("%s.%s: want *types.Named, got %T", pkg.Path(), funcName, typ)
	}
	return named.Obj().Pkg()
}
