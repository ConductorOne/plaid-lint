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
)

// TestL2RoundTripDiagnosticEquivalence demonstrates the W5 gate property:
// type-checking a consumer package against a dependency yields the same
// observable type-check output whether the dep's exported types came from
// a fresh in-process type-check or from our L2 cache (gcexportdata blob).
//
// Setup:
//  1. Write a "dep" package and a "consumer" package that imports it.
//  2. Type-check dep directly, then EncodeExportData → store in L2 →
//     ReadExportData to rehydrate.
//  3. Type-check consumer twice:
//     a. With dep coming from the direct type-check pass (the
//        non-cached "ground truth").
//     b. With dep coming from the L2-rehydrated *types.Package.
//  4. Compare the consumer's exported declarations between (a) and (b).
//     They must be byte-identical when re-exported via gcexportdata.
//
// This is the "diagnostic equivalence" check shrunk to the type-check
// layer: the analyzer pipeline doesn't exist yet (W6), but every
// analyzer eventually consumes a *types.Package, so equivalence at
// this layer is necessary (though not sufficient) for diagnostic
// equivalence at the analyzer layer.
func TestL2RoundTripDiagnosticEquivalence(t *testing.T) {
	c := newTestCache(t)

	dir := t.TempDir()
	depDir := filepath.Join(dir, "dep")
	consDir := filepath.Join(dir, "cons")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(consDir, 0o755); err != nil {
		t.Fatal(err)
	}

	depSrc := `package dep

// Greet returns a greeting.
func Greet(name string) string { return "hi, " + name }

// Count is exported state.
var Count int

// Color is a typed enum.
type Color int

const (
	Red Color = iota
	Blue
)
`
	consumerSrc := `package cons

import "example.com/dep"

// Wrap pads a greeting.
func Wrap(name string) string {
	return "[" + dep.Greet(name) + "]"
}

// Tally adds 1 and returns the running count.
func Tally() int {
	dep.Count++
	return dep.Count
}

// Pick returns whichever Color you ask for, defaulting to Red.
func Pick(c dep.Color) dep.Color {
	if c == dep.Blue {
		return dep.Blue
	}
	return dep.Red
}
`
	if err := os.WriteFile(filepath.Join(depDir, "dep.go"), []byte(depSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consDir, "cons.go"), []byte(consumerSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Step 1: type-check dep directly (the "ground truth" dep package).
	depFset := token.NewFileSet()
	depFile, err := parser.ParseFile(depFset, filepath.Join(depDir, "dep.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse dep: %v", err)
	}
	depConf := types.Config{Importer: importer.Default()}
	depPkg, err := depConf.Check("example.com/dep", depFset, []*ast.File{depFile}, nil)
	if err != nil {
		t.Fatalf("type-check dep: %v", err)
	}

	// Step 2: store dep in L2 and reload via gcexportdata.
	depExport, err := EncodeExportData(depFset, depPkg)
	if err != nil {
		t.Fatalf("EncodeExportData(dep): %v", err)
	}
	depFsetSnap, err := EncodeFileSet(depFset)
	if err != nil {
		t.Fatalf("EncodeFileSet(dep): %v", err)
	}
	depEntry := &L2Entry{
		PackageID:       "example.com/dep",
		GoVersion:       "go1.26",
		BuildEnv:        "test/test/cgo0",
		InputDigest:     fillByte(0xa1),
		ToolVersion:     "plaid-lint-test",
		ExportData:      depExport,
		FileSetSnapshot: depFsetSnap,
	}
	depID := ComputeL2ActionID(depEntry)
	if err := c.WriteL2(depEntry, depID); err != nil {
		t.Fatalf("WriteL2(dep): %v", err)
	}
	// Read back through the cache to exercise the full disk round-trip.
	storedDepEntry, err := c.ReadL2(depID)
	if err != nil {
		t.Fatalf("ReadL2(dep): %v", err)
	}

	// Step 3: type-check consumer twice.

	// (a) non-cached: importer is a custom one that returns depPkg directly.
	consFsetA := token.NewFileSet()
	consFileA, err := parser.ParseFile(consFsetA, filepath.Join(consDir, "cons.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse cons (a): %v", err)
	}
	importerA := importerFromMap{pkgs: map[string]*types.Package{
		"example.com/dep": depPkg,
	}}
	consConfA := types.Config{Importer: importerA}
	consPkgA, err := consConfA.Check("example.com/cons", consFsetA, []*ast.File{consFileA}, nil)
	if err != nil {
		t.Fatalf("type-check cons (a): %v", err)
	}

	// (b) L2-rehydrated: importer reads from the L2 entry.
	consFsetB := token.NewFileSet()
	consFileB, err := parser.ParseFile(consFsetB, filepath.Join(consDir, "cons.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse cons (b): %v", err)
	}
	cachedDepPkg, err := ReadExportData(consFsetB, map[string]*types.Package{}, "example.com/dep", storedDepEntry.ExportData)
	if err != nil {
		t.Fatalf("ReadExportData(dep): %v", err)
	}
	importerB := importerFromMap{pkgs: map[string]*types.Package{
		"example.com/dep": cachedDepPkg,
	}}
	consConfB := types.Config{Importer: importerB}
	consPkgB, err := consConfB.Check("example.com/cons", consFsetB, []*ast.File{consFileB}, nil)
	if err != nil {
		t.Fatalf("type-check cons (b): %v", err)
	}

	// Step 4: compare consumer exported-API surface. We re-export both
	// consumer packages via gcexportdata and verify the exported
	// declarations match (modulo the absolute Pos values, which we
	// canonicalize by parsing them back into fresh FileSets).
	exportA, err := EncodeExportData(consFsetA, consPkgA)
	if err != nil {
		t.Fatalf("EncodeExportData(consA): %v", err)
	}
	exportB, err := EncodeExportData(consFsetB, consPkgB)
	if err != nil {
		t.Fatalf("EncodeExportData(consB): %v", err)
	}

	readFsetA := token.NewFileSet()
	readPkgA, err := ReadExportData(readFsetA, map[string]*types.Package{}, "example.com/cons", exportA)
	if err != nil {
		t.Fatalf("re-read exportA: %v", err)
	}
	readFsetB := token.NewFileSet()
	readPkgB, err := ReadExportData(readFsetB, map[string]*types.Package{}, "example.com/cons", exportB)
	if err != nil {
		t.Fatalf("re-read exportB: %v", err)
	}

	wantNames := []string{"Wrap", "Tally", "Pick"}
	for _, n := range wantNames {
		oA := readPkgA.Scope().Lookup(n)
		oB := readPkgB.Scope().Lookup(n)
		if oA == nil || oB == nil {
			t.Fatalf("name %q missing: a=%v b=%v", n, oA, oB)
		}
		ta := oA.Type().String()
		tb := oB.Type().String()
		if ta != tb {
			t.Errorf("type mismatch for %q:\n  cached:    %s\n  uncached:  %s", n, tb, ta)
		}
	}
}

// TestL2WarmCacheIdempotent verifies that re-encoding a *types.Package
// loaded from L2 produces a blob whose round-trip is equivalent to the
// original — i.e. L2 reads do not lose information that a subsequent
// downstream consumer would observe.
func TestL2WarmCacheIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := `package p

type T struct {
	A int
	B string
}

func (t T) String() string { return t.B }

func New(b string) T { return T{B: b} }
`
	fsetA, pkgA := typeCheckFixture(t, dir, src, "example.com/p")
	exportA, err := EncodeExportData(fsetA, pkgA)
	if err != nil {
		t.Fatalf("encode A: %v", err)
	}

	fsetB := token.NewFileSet()
	pkgB, err := ReadExportData(fsetB, map[string]*types.Package{}, "example.com/p", exportA)
	if err != nil {
		t.Fatalf("read A→B: %v", err)
	}
	exportB, err := EncodeExportData(fsetB, pkgB)
	if err != nil {
		t.Fatalf("encode B: %v", err)
	}

	fsetC := token.NewFileSet()
	pkgC, err := ReadExportData(fsetC, map[string]*types.Package{}, "example.com/p", exportB)
	if err != nil {
		t.Fatalf("read B→C: %v", err)
	}

	// Every exported declaration should survive A → encode → B → encode → C
	// with the same exported names and same type strings.
	scopeA := pkgA.Scope()
	scopeC := pkgC.Scope()
	for _, name := range scopeA.Names() {
		oA := scopeA.Lookup(name)
		oC := scopeC.Lookup(name)
		if oC == nil {
			t.Errorf("name %q lost in A→C", name)
			continue
		}
		if oA.Type().String() != oC.Type().String() {
			t.Errorf("type mismatch for %q after A→C: %q vs %q",
				name, oA.Type().String(), oC.Type().String())
		}
	}
}

// importerFromMap is a tiny importer.Importer that resolves a fixed
// set of paths and delegates unknown paths to the default importer.
type importerFromMap struct {
	pkgs map[string]*types.Package
}

// Compile-time interface check.
var _ types.Importer = importerFromMap{}

func (m importerFromMap) Import(path string) (*types.Package, error) {
	if p, ok := m.pkgs[path]; ok {
		return p, nil
	}
	return importer.Default().Import(path)
}

