// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

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

// TestL2_ShallowEquivalence pins the round-trip semantics of the
// shallow L2 path: a package round-tripped through
// l2StoreWithFiles + tryL2Lookup returns a *types.Package whose
// exported Scope (Lookup / Names / method sets) is functionally
// equivalent to the source package.
func TestL2_ShallowEquivalence(t *testing.T) {
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}

	srcDir := t.TempDir()
	srcPath := filepath.Join(srcDir, "p.go")
	src := `package p

const Pi = 3.14159

type Point struct {
	X, Y int
}

func (p Point) Add(q Point) Point {
	return Point{X: p.X + q.X, Y: p.Y + q.Y}
}

func Greet(name string) string { return "hello, " + name }

type Mode int

const (
	ModeOff Mode = iota
	ModeOn
)
`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, srcPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := types.Config{Importer: importer.Default()}
	srcPkg, err := conf.Check("example.com/p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}

	var key file.Hash
	for i := range key {
		key[i] = byte(i + 1)
	}
	ph := &packageHandle{
		mp: &metadata.Package{
			ID:      "example.com/p",
			PkgPath: "example.com/p",
			Name:    "p",
		},
		key: key,
	}
	b := &typeCheckBatch{
		fset:        fset,
		l2:          l2,
		l2BuildEnv:  "test/test/cgo0",
		l2GoVersion: "go1.26",
		l2ToolVer:   "plaid-lint-test",
		l2Metrics:   &l2Metrics{},
	}

	b.l2StoreWithFiles(ph, srcPkg, []*token.File{fset.File(f.Pos())})
	got, ok := b.tryL2Lookup(context.Background(), ph)
	if !ok || got == nil {
		t.Fatalf("tryL2Lookup: want hit, got miss")
	}

	// Path identity.
	if got.Path() != srcPkg.Path() {
		t.Errorf("Path: got %q, want %q", got.Path(), srcPkg.Path())
	}
	if got.Name() != srcPkg.Name() {
		t.Errorf("Name: got %q, want %q", got.Name(), srcPkg.Name())
	}

	// Exported names round-trip: every exported name in srcPkg must
	// resolve in got's Scope.
	srcScope := srcPkg.Scope()
	gotScope := got.Scope()
	for _, name := range srcScope.Names() {
		srcObj := srcScope.Lookup(name)
		if srcObj == nil || !srcObj.Exported() {
			continue
		}
		gotObj := gotScope.Lookup(name)
		if gotObj == nil {
			t.Errorf("rehydrated Scope missing exported name %q", name)
			continue
		}
		if gotObj.Name() != srcObj.Name() {
			t.Errorf("Scope[%q].Name: got %q, want %q", name, gotObj.Name(), srcObj.Name())
		}
	}

	// Method-set preservation: Point.Add must survive the round-trip.
	srcPoint := srcScope.Lookup("Point")
	gotPoint := gotScope.Lookup("Point")
	if srcPoint == nil || gotPoint == nil {
		t.Fatalf("Point missing in either Scope (src=%v got=%v)", srcPoint, gotPoint)
	}
	srcNamed, ok := srcPoint.Type().(*types.Named)
	if !ok {
		t.Fatalf("src Point is not *types.Named: %T", srcPoint.Type())
	}
	gotNamed, ok := gotPoint.Type().(*types.Named)
	if !ok {
		t.Fatalf("got Point is not *types.Named: %T", gotPoint.Type())
	}
	if srcNamed.NumMethods() != gotNamed.NumMethods() {
		t.Errorf("Point.NumMethods: got %d, want %d", gotNamed.NumMethods(), srcNamed.NumMethods())
	}
	if srcNamed.NumMethods() > 0 {
		srcMethod := srcNamed.Method(0).Name()
		gotMethod := gotNamed.Method(0).Name()
		if srcMethod != gotMethod {
			t.Errorf("Point method[0]: got %q, want %q", gotMethod, srcMethod)
		}
	}
}

// TestL2_CrossPkgCanonicalIdentity is the falsifiable gate
// for the shallow-mode L2 switch. Two packages P and Q both
// import dep D; we run tryL2Lookup for P then Q in the same batch
// and assert P.Imports()[0] and Q.Imports()[0] resolve to the SAME
// *types.Package instance — i.e. the canonical D for this batch.
//
// Under the pre-shallow design that single identity was enforced by
// the shared b.l2Imports map and gcexportdata.Read's lazy stub
// synthesis. The same invariant holds via the shallow
// getPackages callback routing every reference through
// b.importPackages (the canonical-identity futureCache). If the
// callback were to bypass the futureCache, this test would
// expose the regression.
func TestL2_CrossPkgCanonicalIdentity(t *testing.T) {
	cacheDir := t.TempDir()
	l2, err := clcache.Open(cacheDir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}

	srcDir := t.TempDir()
	dDir := filepath.Join(srcDir, "d")
	pDir := filepath.Join(srcDir, "p")
	qDir := filepath.Join(srcDir, "q")
	for _, d := range []string{dDir, pDir, qDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dSrc := `package d

type Token struct{ Val int }
`
	pSrc := `package p

import "example.com/d"

func MintP() d.Token { return d.Token{} }
`
	qSrc := `package q

import "example.com/d"

func ConsumeQ(t d.Token) int { return t.Val }
`
	for path, src := range map[string]string{
		filepath.Join(dDir, "d.go"): dSrc,
		filepath.Join(pDir, "p.go"): pSrc,
		filepath.Join(qDir, "q.go"): qSrc,
	} {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	fset := token.NewFileSet()
	parseFile := func(p string) *ast.File {
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		return f
	}
	dFile := parseFile(filepath.Join(dDir, "d.go"))
	pFile := parseFile(filepath.Join(pDir, "p.go"))
	qFile := parseFile(filepath.Join(qDir, "q.go"))

	dConf := types.Config{Importer: importer.Default()}
	dPkg, err := dConf.Check("example.com/d", fset, []*ast.File{dFile}, nil)
	if err != nil {
		t.Fatalf("type-check d: %v", err)
	}
	depImporter := mapImporter{pkgs: map[string]*types.Package{
		"example.com/d": dPkg,
	}}
	pConf := types.Config{Importer: depImporter}
	pPkg, err := pConf.Check("example.com/p", fset, []*ast.File{pFile}, nil)
	if err != nil {
		t.Fatalf("type-check p: %v", err)
	}
	qConf := types.Config{Importer: depImporter}
	qPkg, err := qConf.Check("example.com/q", fset, []*ast.File{qFile}, nil)
	if err != nil {
		t.Fatalf("type-check q: %v", err)
	}

	mkHandle := func(pkg *types.Package, keyByte byte) *packageHandle {
		var key file.Hash
		for i := range key {
			key[i] = keyByte
		}
		return &packageHandle{
			mp: &metadata.Package{
				ID:      metadata.PackageID(pkg.Path()),
				PkgPath: metadata.PackagePath(pkg.Path()),
				Name:    metadata.PackageName(pkg.Name()),
				DepsByPkgPath: map[metadata.PackagePath]metadata.PackageID{
					"example.com/d": "example.com/d",
				},
			},
			key: key,
		}
	}

	// Set up the batch. The canonical D is held in importPackages
	// (seeded below); _handles[d] supplies the metadata that
	// importLookup's transitive walk asserts on.
	batchFset := token.NewFileSet()
	canonicalD := types.NewPackage("example.com/d", "d")
	tokenObj := dPkg.Scope().Lookup("Token")
	if tokenObj == nil {
		t.Fatalf("d.Token missing in source dPkg")
	}
	canonicalD.Scope().Insert(tokenObj)
	canonicalD.MarkComplete()
	b := &typeCheckBatch{
		fset:           batchFset,
		l2:             l2,
		l2BuildEnv:     "test/test/cgo0",
		l2GoVersion:    "go1.26",
		l2ToolVer:      "plaid-lint-test",
		l2Metrics:      &l2Metrics{},
		importPackages: newFutureCache[PackageID, *types.Package](true),
		_handles: map[PackageID]*packageHandle{
			"example.com/d": {
				mp: &metadata.Package{
					ID:      "example.com/d",
					PkgPath: "example.com/d",
					Name:    "d",
				},
				key: file.Hash{0xdd},
			},
		},
	}
	if _, err := b.importPackages.get(context.Background(), "example.com/d", func(ctx context.Context) (*types.Package, error) {
		return canonicalD, nil
	}); err != nil {
		t.Fatalf("seed importPackages with d: %v", err)
	}

	// Store P and Q via the production path (l2StoreWithFiles writes
	// IExportShallow blobs against b.fset == fset, the same fset the
	// source positions resolve in).
	bWrite := &typeCheckBatch{
		fset:        fset,
		l2:          l2,
		l2BuildEnv:  "test/test/cgo0",
		l2GoVersion: "go1.26",
		l2ToolVer:   "plaid-lint-test",
		l2Metrics:   &l2Metrics{},
	}
	phP := mkHandle(pPkg, 0x70)
	phQ := mkHandle(qPkg, 0x71)
	bWrite.l2StoreWithFiles(phP, pPkg, []*token.File{fset.File(pFile.Pos())})
	bWrite.l2StoreWithFiles(phQ, qPkg, []*token.File{fset.File(qFile.Pos())})

	ctx := context.Background()
	gotP, okP := b.tryL2Lookup(ctx, phP)
	if !okP {
		t.Fatalf("L2 lookup for p: want hit, got miss")
	}
	gotQ, okQ := b.tryL2Lookup(ctx, phQ)
	if !okQ {
		t.Fatalf("L2 lookup for q: want hit, got miss")
	}

	// The Named type behind d.Token resolves via
	// objectpath.Object(pkg, "Token") inside IImportShallow, where
	// pkg is whatever our getPackages callback returned for path
	// "example.com/d". The shallow callback routes through the
	// futureCache and returns the same canonical instance both
	// times, so the underlying TypeName's Pkg() is structurally
	// identical across the two L2 reads. This is the
	// invariant: two consumers seeing a shared dep see
	// types.Identical for what should be identical types.
	dFromP := canonicalRefThroughFunc(t, gotP, "MintP", refKindResult)
	dFromQ := canonicalRefThroughFunc(t, gotQ, "ConsumeQ", refKindParam)

	if dFromP != dFromQ {
		t.Errorf("cross-pkg canonical identity broken: d via p = %p, d via q = %p (want equal pointers)",
			dFromP, dFromQ)
	}
	if dFromP == nil {
		t.Errorf("d via p is nil")
	}
	if got, want := dFromP.Path(), "example.com/d"; got != want {
		t.Errorf("d via p.Path() = %q, want %q", got, want)
	}
}

// mapImporter is a types.Importer over a fixed table.
type mapImporter struct {
	pkgs map[string]*types.Package
}

func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m.pkgs[path]; ok {
		return p, nil
	}
	return nil, errMissingMapImporterPath(path)
}

type errMissingMapImporterPath string

func (e errMissingMapImporterPath) Error() string {
	return "mapImporter: unknown path " + string(e)
}

// canonicalRefThroughFunc looks up funcName in pkg and returns the
// *types.Package of the Named type at result[0] (refKindResult) or
// param[0] (refKindParam).
func canonicalRefThroughFunc(t *testing.T, pkg *types.Package, funcName string, kind refKind) *types.Package {
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
	named, ok := tup.At(0).Type().(*types.Named)
	if !ok {
		t.Fatalf("%s.%s: want *types.Named, got %T", pkg.Path(), funcName, tup.At(0).Type())
	}
	return named.Obj().Pkg()
}
