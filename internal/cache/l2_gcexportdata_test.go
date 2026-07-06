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

// typeCheckFixture parses and type-checks a single-file package located
// in dir. Standard imports (e.g. "fmt") are resolved via importer.Default.
func typeCheckFixture(t *testing.T, dir, src, importPath string) (*token.FileSet, *types.Package) {
	t.Helper()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	conf := &types.Config{
		Importer: importer.Default(),
	}
	pkg, err := conf.Check(importPath, fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return fset, pkg
}

const exporterFixtureSrc = `package fixture

// Pi is a math-y constant.
const Pi = 3.14159

// Point is exported.
type Point struct {
	X, Y int
}

// Add adds q to p.
func (p Point) Add(q Point) Point {
	return Point{X: p.X + q.X, Y: p.Y + q.Y}
}

// Greet returns a greeting.
func Greet(name string) string {
	return "hello, " + name
}

// Mode is an enum-ish.
type Mode int

const (
	ModeOff Mode = iota
	ModeOn
)
`

func TestEncodeReadExportDataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fset, pkg := typeCheckFixture(t, dir, exporterFixtureSrc, "example.com/fixture")

	data, err := EncodeExportData(fset, pkg)
	if err != nil {
		t.Fatalf("EncodeExportData: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("EncodeExportData returned empty data")
	}

	readFset := token.NewFileSet()
	imports := make(map[string]*types.Package)
	imported, err := ReadExportData(readFset, imports, "example.com/fixture", data)
	if err != nil {
		t.Fatalf("ReadExportData: %v", err)
	}
	if imported == nil {
		t.Fatalf("ReadExportData returned nil package")
	}
	if imported.Path() != "example.com/fixture" {
		t.Errorf("imported.Path() = %q, want %q", imported.Path(), "example.com/fixture")
	}

	// Verify the exported names round-trip.
	wantNames := []string{"Pi", "Point", "Greet", "Mode", "ModeOff", "ModeOn"}
	scope := imported.Scope()
	for _, n := range wantNames {
		if scope.Lookup(n) == nil {
			t.Errorf("imported package missing exported name %q", n)
		}
	}

	// Verify Point's methods survived.
	pointObj := scope.Lookup("Point")
	if pointObj == nil {
		t.Fatalf("Point missing")
	}
	named, ok := pointObj.Type().(*types.Named)
	if !ok {
		t.Fatalf("Point is not *types.Named: %T", pointObj.Type())
	}
	if named.NumMethods() == 0 {
		t.Errorf("Point has no methods after round-trip")
	}
}

func TestEncodeExportDataRejectsNilArgs(t *testing.T) {
	dir := t.TempDir()
	fset, pkg := typeCheckFixture(t, dir, exporterFixtureSrc, "example.com/fixture")
	if _, err := EncodeExportData(nil, pkg); err == nil {
		t.Errorf("EncodeExportData(nil fset) = nil error, want error")
	}
	if _, err := EncodeExportData(fset, nil); err == nil {
		t.Errorf("EncodeExportData(nil pkg) = nil error, want error")
	}
}

func TestReadExportDataRejectsBadInput(t *testing.T) {
	fset := token.NewFileSet()
	if _, err := ReadExportData(nil, nil, "p", []byte{1, 2}); err == nil {
		t.Errorf("ReadExportData(nil fset) = nil error, want error")
	}
	if _, err := ReadExportData(fset, nil, "", []byte{1, 2}); err == nil {
		t.Errorf("ReadExportData(empty path) = nil error, want error")
	}
	if _, err := ReadExportData(fset, nil, "p", nil); err == nil {
		t.Errorf("ReadExportData(nil data) = nil error, want error")
	}
}

// TestEncodeReadExportDataPositions verifies that the export blob carries
// position information correctly when paired with an encoded FileSet.
// This is the W5-style hand-off: producer writes ExportData + FileSetSnapshot;
// consumer decodes FileSet first, then loads ExportData against it.
func TestEncodeReadExportDataPositions(t *testing.T) {
	dir := t.TempDir()
	fset, pkg := typeCheckFixture(t, dir, exporterFixtureSrc, "example.com/fixture")

	// Record the original position of Greet.
	greet := pkg.Scope().Lookup("Greet")
	if greet == nil {
		t.Fatalf("Greet not found")
	}
	wantPos := fset.Position(greet.Pos())

	exportBytes, err := EncodeExportData(fset, pkg)
	if err != nil {
		t.Fatalf("EncodeExportData: %v", err)
	}
	fsetBytes, err := EncodeFileSet(fset)
	if err != nil {
		t.Fatalf("EncodeFileSet: %v", err)
	}

	decodedFset, err := DecodeFileSet(fsetBytes)
	if err != nil {
		t.Fatalf("DecodeFileSet: %v", err)
	}
	imports := make(map[string]*types.Package)
	imported, err := ReadExportData(decodedFset, imports, "example.com/fixture", exportBytes)
	if err != nil {
		t.Fatalf("ReadExportData: %v", err)
	}

	importedGreet := imported.Scope().Lookup("Greet")
	if importedGreet == nil {
		t.Fatalf("imported Greet not found")
	}
	// Note: gcexportdata.Read assigns positions in the *target* FileSet,
	// which may differ in absolute Pos values but must resolve to the same
	// Filename/Line. (Column may differ because gcexportdata records line
	// + column separately and the reconstructed fileset's absolute offsets
	// won't match.)
	gotPos := decodedFset.Position(importedGreet.Pos())
	if gotPos.Filename != wantPos.Filename {
		t.Errorf("Greet filename: got %q, want %q", gotPos.Filename, wantPos.Filename)
	}
	if gotPos.Line != wantPos.Line {
		t.Errorf("Greet line: got %d, want %d", gotPos.Line, wantPos.Line)
	}
}
