package cache

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
	"golang.org/x/tools/go/ast/inspector"
)

// recordedDiag is the minimal subset of analysis.Diagnostic we compare
// when verifying that an analyzer behaves the same on a non-cached vs
// L2-cached *types.Package. Filename/Line/Column are extracted from the
// pass's FileSet so the comparison is independent of token.Pos integers
// (which legitimately differ across separate FileSets).
type recordedDiag struct {
	Analyzer string
	Category string
	Message  string
	Filename string
	Line     int
	Column   int
}

func canonicalDiagnostics(t *testing.T, ds []recordedDiag) []byte {
	t.Helper()
	out := make([]recordedDiag, len(ds))
	copy(out, ds)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Analyzer != out[j].Analyzer {
			return out[i].Analyzer < out[j].Analyzer
		}
		if out[i].Filename != out[j].Filename {
			return out[i].Filename < out[j].Filename
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Column != out[j].Column {
			return out[i].Column < out[j].Column
		}
		return out[i].Message < out[j].Message
	})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// runAnalyzer runs one analyzer over the given pre-type-checked package.
// It returns the diagnostics in canonical form. The implementation
// constructs analysis.Pass values by hand: we are not running the full
// driver, but the in-tree analyzers below have no facts and only depend
// on the inspect analyzer as a prerequisite.
func runAnalyzer(t *testing.T, a *analysis.Analyzer, fset *token.FileSet, files []*ast.File, pkg *types.Package, info *types.Info) []recordedDiag {
	t.Helper()
	var diags []recordedDiag

	// Compute the inspect prerequisite, if required.
	resultOf := map[*analysis.Analyzer]any{}
	for _, req := range a.Requires {
		if req == inspect.Analyzer {
			resultOf[req] = inspector.New(files)
			continue
		}
		t.Fatalf("unhandled prerequisite analyzer %s", req.Name)
	}

	pass := &analysis.Pass{
		Analyzer:   a,
		Fset:       fset,
		Files:      files,
		Pkg:        pkg,
		TypesInfo:  info,
		TypesSizes: types.SizesFor("gc", "amd64"),
		ResultOf:   resultOf,
		Report: func(d analysis.Diagnostic) {
			p := fset.Position(d.Pos)
			diags = append(diags, recordedDiag{
				Analyzer: a.Name,
				Category: d.Category,
				Message:  d.Message,
				Filename: filepath.Base(p.Filename),
				Line:     p.Line,
				Column:   p.Column,
			})
		},
		ImportObjectFact:  func(types.Object, analysis.Fact) bool { return false },
		ImportPackageFact: func(*types.Package, analysis.Fact) bool { return false },
		ExportObjectFact:  func(types.Object, analysis.Fact) {},
		ExportPackageFact: func(analysis.Fact) {},
		AllObjectFacts:    func() []analysis.ObjectFact { return nil },
		AllPackageFacts:   func() []analysis.PackageFact { return nil },
	}
	if _, err := a.Run(pass); err != nil {
		t.Fatalf("analyzer %s: %v", a.Name, err)
	}
	return diags
}

// typeCheckConsumer parses and type-checks the consumer source against
// a fixed *types.Package for "example.com/dep". Returns everything the
// analyzer needs to run.
func typeCheckConsumer(t *testing.T, depPkg *types.Package, dir, name, src string) (*token.FileSet, []*ast.File, *types.Package, *types.Info) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Implicits:  map[ast.Node]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Scopes:     map[ast.Node]*types.Scope{},
	}
	imp := importerFromMap{pkgs: map[string]*types.Package{
		"example.com/dep": depPkg,
	}}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check("example.com/cons", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("type-check: %v", err)
	}
	return fset, []*ast.File{f}, pkg, info
}

// TestL2AnalyzerDiagnosticEquivalence runs several upstream analyzers
// against the same consumer source twice — once where the dep package
// is the fresh in-process type-check result, once where it has gone
// through our L2 cache (EncodeExportData → WriteL2 → ReadL2 →
// ReadExportData) — and verifies the analyzers' diagnostics are
// byte-identical between the two runs.
//
// This is the W5 gate evidence test described in the task spec:
// "confirm gcexportdata round-trips cleanly for the analyzer set."
// The analyzer pipeline upstream of W6 isn't wired yet, so we
// exercise the analyzers directly through their Run functions; the
// thing we're checking — that the cached *types.Package preserves
// everything analyzers depend on — is independent of which pipeline
// drives them.
func TestL2AnalyzerDiagnosticEquivalence(t *testing.T) {
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

	// Dep package exports things consumer-side analyzers should trip on:
	//   - a Printf-shaped function (for printf analyzer).
	//   - a function returning an error (for unusedresult analyzer).
	depSrc := `package dep

import "fmt"

// Errorf is printf-style; the consumer compares it to nil so the
// nilfunc analyzer triggers.
func Errorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// MustString returns its argument; useful for unused-result analyzers.
func MustString(s string) string { return s }
`
	depPath := filepath.Join(depDir, "dep.go")
	if err := os.WriteFile(depPath, []byte(depSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Consumer triggers diagnostics from multiple analyzers:
	//   - printf:       wrong number of format args
	//   - nilfunc:      compares a function value to nil illegitimately
	//   - (control)     unusedresult will need configuration; we
	//                   skip configuring it and instead include a
	//                   no-op call to keep the set diverse.
	consSrc := `package cons

import (
	"fmt"

	"example.com/dep"
)

// NilCompareNamedFunc triggers the nilfunc analyzer: comparing a
// named function to nil is always false / useless.
func NilCompareNamedFunc() bool {
	return dep.Errorf == nil
}

// SelfAssign triggers the assign analyzer.
func SelfAssign(x int) int {
	x = x //nolint
	return x
}

// BadPrintf triggers the printf analyzer: %d with a string arg, via
// the well-known fmt.Sprintf entry (the printf analyzer recognises
// standard library wrappers without configuration).
func BadPrintf() string {
	return fmt.Sprintf("count=%d", "not-an-int")
}

// SilenceMustString consumes MustString's result so the analyzer set
// doesn't have to depend on configuration we haven't wired.
var _ = dep.MustString("x")
`
	// Step 1: type-check dep once.
	depFset := token.NewFileSet()
	depFile, err := parser.ParseFile(depFset, depPath, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse dep: %v", err)
	}
	depConf := types.Config{Importer: importer.Default()}
	depPkgGroundTruth, err := depConf.Check("example.com/dep", depFset, []*ast.File{depFile}, nil)
	if err != nil {
		t.Fatalf("type-check dep: %v", err)
	}

	// Step 2: write the dep into our L2 cache and read it back.
	depExport, err := EncodeExportData(depFset, depPkgGroundTruth)
	if err != nil {
		t.Fatalf("EncodeExportData(dep): %v", err)
	}
	depEntry := &L2Entry{
		PackageID:   "example.com/dep",
		GoVersion:   "go1.26",
		BuildEnv:    "test/test/cgo0",
		InputDigest: fillByte(0xc3),
		ToolVersion: "plaid-lint-test",
		ExportData:  depExport,
	}
	depID := ComputeL2ActionID(depEntry)
	if err := c.WriteL2(depEntry, depID); err != nil {
		t.Fatalf("WriteL2(dep): %v", err)
	}
	storedDep, err := c.ReadL2(depID)
	if err != nil {
		t.Fatalf("ReadL2(dep): %v", err)
	}

	// Step 3: type-check the consumer twice and run a set of analyzers
	// over each.

	analyzers := []*analysis.Analyzer{
		printf.Analyzer,
		nilfunc.Analyzer,
		assign.Analyzer,
		unusedresult.Analyzer,
	}

	// (a) ground truth: dep is the in-process *types.Package.
	fsetA, filesA, pkgA, infoA := typeCheckConsumer(t, depPkgGroundTruth, consDir, "cons.go", consSrc)
	var diagsA []recordedDiag
	for _, a := range analyzers {
		diagsA = append(diagsA, runAnalyzer(t, a, fsetA, filesA, pkgA, infoA)...)
	}

	// (b) L2-cached: dep is rehydrated from gcexportdata in a fresh FileSet.
	depFsetB := token.NewFileSet()
	cachedDepPkg, err := ReadExportData(depFsetB, map[string]*types.Package{}, "example.com/dep", storedDep.ExportData)
	if err != nil {
		t.Fatalf("ReadExportData(dep): %v", err)
	}
	fsetB, filesB, pkgB, infoB := typeCheckConsumer(t, cachedDepPkg, consDir, "cons.go", consSrc)
	var diagsB []recordedDiag
	for _, a := range analyzers {
		diagsB = append(diagsB, runAnalyzer(t, a, fsetB, filesB, pkgB, infoB)...)
	}

	// Step 4: canonicalize + compare.
	canonA := canonicalDiagnostics(t, diagsA)
	canonB := canonicalDiagnostics(t, diagsB)
	if !reflect.DeepEqual(canonA, canonB) {
		t.Errorf("diagnostics differ between non-cached and L2-cached runs\n  uncached: %s\n  cached:   %s",
			canonA, canonB)
	}

	// Also assert at least one diagnostic fired: a no-diagnostic test
	// would silently pass even if analyzers were broken.
	if len(diagsA) == 0 {
		t.Errorf("expected at least one diagnostic from the uncached run; got none")
	}

	t.Logf("L2 diagnostic-equivalence summary: %d analyzers run, %d diagnostics, equal across cached/uncached",
		len(analyzers), len(diagsA))
	for _, d := range diagsA {
		t.Logf("  %s: %s:%d:%d %s", d.Analyzer, d.Filename, d.Line, d.Column, d.Message)
	}
}

