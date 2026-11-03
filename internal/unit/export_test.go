// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"context"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// A three-package chain: a declares API (generics, methods, embedded
// interfaces, constants), b builds on a and declares a printf wrapper,
// c imports ONLY b yet uses a's types through b's API — so c's
// action can type-check only if b's export data is deep.
const (
	exportFixtureA = `package a

import "fmt"

const Answer = 42

type Pair[K comparable, V any] struct {
	Key K
	Val V
}

func (p Pair[K, V]) String() string { return fmt.Sprint(p.Key, "=", p.Val) }

type Named interface {
	fmt.Stringer
	Name() string
}

type Thing struct{ N int }

func (t *Thing) Name() string   { return fmt.Sprint(t.N) }
func (t *Thing) String() string { return "thing" }

func Make(n int) *Thing { return &Thing{N: n} }

var Default Named = Make(Answer)
`

	exportFixtureB = `package b

import (
	"fmt"

	"example.com/fix/a"
)

type Entry = a.Pair[string, *a.Thing]

func Lookup(k string) Entry { return Entry{Key: k, Val: a.Make(len(k))} }

func Named() a.Named { return a.Default }

// Logf is a printf wrapper (a fact producer).
func Logf(format string, args ...any) { fmt.Printf(format, args...) }
`

	exportFixtureC = `package c

import "example.com/fix/b"

func Use() int {
	e := b.Lookup("k")
	b.Logf("%d") // printf: missing arg — through b's wrapper fact
	return e.Val.N + len(b.Named().Name()) + len(e.String())
}
`
)

// plaidStdlib memoizes one ExportStdlib run of this toolchain's GOROOT
// for the test binary: the tree every fixture action resolves std
// imports against, so no compiler export data is involved anywhere.
var plaidStdlib = sync.OnceValues(func() (string, error) {
	goroot, err := testGOROOT()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "plaid-stdlib-test-*")
	if err != nil {
		return "", err
	}
	_, err = ExportStdlib(context.Background(), StdlibConfig{
		GOROOT: goroot,
		GOOS:   runtime.GOOS,
		GOARCH: runtime.GOARCH,
		Out:    dir,
	})
	return dir, err
})

// TestMain removes the shared stdlib tree after the package's tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if plaidStdlibUsed.Load() {
		if dir, err := plaidStdlib(); err == nil {
			_ = os.RemoveAll(dir)
		}
	}
	os.Exit(code)
}

// testGOROOT asks the toolchain running the tests for its GOROOT (test
// fixtures may use the toolchain; the driver under test never does).
func testGOROOT() (string, error) {
	out, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOROOT: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

var plaidStdlibUsed atomic.Bool

func stdlibTree(t *testing.T) string {
	t.Helper()
	plaidStdlibUsed.Store(true)
	dir, err := plaidStdlib()
	if err != nil {
		t.Fatalf("ExportStdlib: %v", err)
	}
	return dir
}

// runExportUnit runs one unit action whose ONLY type inputs are the
// given importcfg entries and the plaid stdlib tree, and returns its
// result plus the out.export and out.facts paths.
func runExportUnit(t *testing.T, pkg *packages.Package, mode Mode, packagefiles, depFacts map[string]string) (*Result, string, string) {
	t.Helper()
	outDir := t.TempDir()
	var b strings.Builder
	for ip, f := range packagefiles {
		fmt.Fprintf(&b, "packagefile %s=%s\n", ip, f)
	}
	importcfg := filepath.Join(outDir, "importcfg")
	if err := os.WriteFile(importcfg, []byte(b.String()), 0o666); err != nil {
		t.Fatal(err)
	}
	ucfg := &Config{
		Schema: SchemaVersion,
		Package: PackageConfig{
			Path:    pkg.PkgPath,
			GoFiles: pkg.CompiledGoFiles,
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
		},
		Deps: DepsConfig{
			Importcfg: importcfg,
			Facts:     depFacts,
			StdlibDir: stdlibTree(t),
		},
		Analysis: AnalysisConfig{Mode: mode},
		Out: OutConfig{
			Sarif:  filepath.Join(outDir, "out.sarif"),
			Facts:  filepath.Join(outDir, "out.plaidfacts"),
			Export: filepath.Join(outDir, "out.plaidexport"),
		},
	}
	golangci := testConfig(t)
	reg, _, err := registry.BuildFromConfig(golangci)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := exclusion.NewFilter(golangci, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), ucfg, golangci, reg, filter)
	if err != nil {
		t.Fatalf("unit.Run(%s): %v", pkg.PkgPath, err)
	}
	return res, ucfg.Out.Export, ucfg.Out.Facts
}

func noTypecheckFindings(t *testing.T, pkg string, res *Result) {
	t.Helper()
	for _, d := range res.Diagnostics {
		if d.Linter == "typecheck" {
			t.Errorf("%s: typecheck finding %s:%d: %s", pkg, d.Pos.Filename, d.Pos.Line, d.Message)
		}
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "does not compile") || strings.Contains(w, "export data") {
			t.Errorf("%s: warning: %s", pkg, w)
		}
	}
}

func exportFixture(t *testing.T) map[string]*packages.Package {
	t.Helper()
	pkgs := buildFixture(t, map[string]string{
		"go.mod":   fixtureGoMod,
		"a/a.go":   exportFixtureA,
		"b/b.go":   exportFixtureB,
		"c/c.go":   exportFixtureC,
		"c/doc.go": "// Package c is the chain's leaf.\npackage c\n",
	})
	for _, p := range []string{"a", "b", "c"} {
		if pkgs["example.com/fix/"+p] == nil {
			t.Fatalf("fixture package %s missing", p)
		}
	}
	return pkgs
}

// TestExport_ChainLintsFromPlaidExportOnly is the property the Bazel
// wiring depends on: each package of a three-package chain lints from
// its direct dependency's out.export plus the plaid stdlib tree — no
// compiler output at all — and the leaf, whose importcfg names only
// its direct dependency, still type-checks the transitive types and
// receives the dependency's facts.
func TestExport_ChainLintsFromPlaidExportOnly(t *testing.T) {
	pkgs := exportFixture(t)

	resA, exportA, factsA := runExportUnit(t, pkgs["example.com/fix/a"], ModeFactsOnly, nil, nil)
	noTypecheckFindings(t, "a", resA)

	resB, exportB, factsB := runExportUnit(t, pkgs["example.com/fix/b"], ModeFull,
		map[string]string{"example.com/fix/a": exportA},
		map[string]string{"example.com/fix/a": factsA})
	noTypecheckFindings(t, "b", resB)

	resC, _, _ := runExportUnit(t, pkgs["example.com/fix/c"], ModeFull,
		map[string]string{"example.com/fix/b": exportB},
		map[string]string{"example.com/fix/b": factsB})
	noTypecheckFindings(t, "c", resC)
	if !hasFinding(resC, "printf", "Logf") {
		t.Errorf("c: expected the printf finding through b's wrapper fact; got %+v", resC.Diagnostics)
	}

	blob, err := os.ReadFile(exportA)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) == 0 || blob[0] != 'i' {
		t.Errorf("out.export does not start with the indexed-format tag: % x", blob[:min(len(blob), 8)])
	}
}

// TestExport_MatchesCompilerExportData compares, object by object, the
// package API decoded from plaid's out.export with the one decoded
// from the Go compiler's export data for the same sources. A type a
// dependent sees must not depend on which of the two its importcfg
// names.
func TestExport_MatchesCompilerExportData(t *testing.T) {
	pkgs := exportFixture(t)
	_, exportA, _ := runExportUnit(t, pkgs["example.com/fix/a"], ModeFull, nil, nil)
	_, exportB, _ := runExportUnit(t, pkgs["example.com/fix/b"], ModeFull,
		map[string]string{"example.com/fix/a": exportA}, nil)

	for ip, plaidFile := range map[string]string{"example.com/fix/a": exportA, "example.com/fix/b": exportB} {
		compilerFile := pkgs[ip].ExportFile
		if compilerFile == "" {
			t.Fatalf("%s: go/packages produced no compiler export data", ip)
		}
		fromPlaid := describePackage(t, ip, plaidFile)
		fromCompiler := describePackage(t, ip, compilerFile)
		if fromPlaid != fromCompiler {
			t.Errorf("%s: plaid export and compiler export disagree\n--- plaid\n%s\n--- compiler\n%s", ip, fromPlaid, fromCompiler)
		}
	}
}

// TestExport_Deterministic: the same action twice writes byte-identical
// export data — it is a cached build output.
func TestExport_Deterministic(t *testing.T) {
	pkgs := exportFixture(t)
	_, e1, _ := runExportUnit(t, pkgs["example.com/fix/a"], ModeFull, nil, nil)
	_, e2, _ := runExportUnit(t, pkgs["example.com/fix/a"], ModeFactsOnly, nil, nil)
	b1, err := os.ReadFile(e1)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(e2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b1, b2) {
		t.Error("export data differs between two runs over identical inputs (full vs facts_only)")
	}
}

// TestExport_BrokenPackageStillExports: a package that does not
// type-check still writes its declared export output.
func TestExport_BrokenPackageStillExports(t *testing.T) {
	pkgs := buildFixture(t, map[string]string{
		"go.mod":         fixtureGoMod,
		"bad/bad.go":     "package bad\n\nfunc Good() int { return 1 }\n\nfunc Bad() int { return undefined }\n",
		"bad/strings.go": "package bad\n\nimport \"strings\"\n\nvar Up = strings.ToUpper\n",
	})
	res, export, _ := runExportUnit(t, pkgs["example.com/fix/bad"], ModeFactsOnly, nil, nil)
	if len(res.Warnings) == 0 {
		t.Error("expected a does-not-compile warning")
	}
	got := describePackage(t, "example.com/fix/bad", export)
	if !strings.Contains(got, "func example.com/fix/bad.Good() (int)") {
		t.Errorf("export of a broken package lost its well-typed declarations:\n%s", got)
	}
}

// TestStdlibExport_MatchesCompilerExportData compares a sample of the
// plaid stdlib tree against the compiler's export data for the same
// SDK: every exported object must render identically.
func TestStdlibExport_MatchesCompilerExportData(t *testing.T) {
	tree := stdlibTree(t)
	sample := []string{"fmt", "context", "net/http", "encoding/json", "iter", "sync/atomic", "reflect", "os"}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedExportFile,
		Env:  append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off", "CGO_ENABLED=0"),
	}
	loaded, err := packages.Load(cfg, sample...)
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	for _, p := range loaded {
		if p.ExportFile == "" {
			t.Fatalf("%s: no compiler export data", p.PkgPath)
		}
		plaidFile := stdlibPathOf(tree, runtime.GOOS, runtime.GOARCH, p.PkgPath)
		fromPlaid := describeExported(t, p.PkgPath, plaidFile)
		fromCompiler := describeExported(t, p.PkgPath, p.ExportFile)
		if fromPlaid != fromCompiler {
			t.Errorf("%s: plaid stdlib export and compiler export disagree\n%s", p.PkgPath, lineDiff(fromPlaid, fromCompiler))
		}
	}
}

// TestStdlibExport_Layout pins the tree shape deps.stdlib_dir resolves
// and the vendored-path identity of GOROOT/src/vendor packages.
func TestStdlibExport_Layout(t *testing.T) {
	tree := stdlibTree(t)
	for _, ip := range []string{"fmt", "net/http", "internal/abi", "vendor/golang.org/x/net/dns/dnsmessage"} {
		if _, err := os.Stat(stdlibPathOf(tree, runtime.GOOS, runtime.GOARCH, ip)); err != nil {
			t.Errorf("%s: %v", ip, err)
		}
	}
	for _, ip := range []string{"cmd/go", "builtin", "unsafe"} {
		if _, err := os.Stat(stdlibPathOf(tree, runtime.GOOS, runtime.GOARCH, ip)); err == nil {
			t.Errorf("%s: exported, but it is not an importable std package", ip)
		}
	}
	// net/http's deep export must name the vendored identity of its
	// golang.org/x dependencies, as the compiler's does.
	imports := map[string]*types.Package{}
	if _, err := readExportFile(token.NewFileSet(), imports, "net/http", stdlibPathOf(tree, runtime.GOOS, runtime.GOARCH, "net/http")); err != nil {
		t.Fatal(err)
	}
	var vendored []string
	for ip := range imports {
		if strings.HasPrefix(ip, "golang.org/x/") {
			t.Errorf("net/http export references %s by its unvendored path", ip)
		}
		if strings.HasPrefix(ip, "vendor/golang.org/x/") {
			vendored = append(vendored, ip)
		}
	}
	if len(vendored) == 0 {
		t.Errorf("net/http export references no vendor/golang.org/x package; have %d packages", len(imports))
	}
}

// TestStdlibExport_SdkVersion: the language version comes from
// GOROOT/VERSION, prereleases included.
func TestStdlibExport_SdkVersion(t *testing.T) {
	for in, want := range map[string]string{"1.26.3": "1.26", "1.27rc1": "1.27", "1.26": "1.26"} {
		if got := majorMinor(in); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("go1.26.3\ntime 2026-01-01\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	if v, err := sdkVersion(dir); err != nil || v != "1.26" {
		t.Errorf("sdkVersion = %q, %v", v, err)
	}
	if !sameRelease(majorMinor(strings.TrimPrefix(runtime.Version(), "go")), runtime.Version()) {
		t.Error("sameRelease does not recognize this binary's own release")
	}
}

// describePackage renders every package-level object (exported or
// not) and every method of every named type, one per line, sorted.
func describePackage(t *testing.T, ip, file string) string {
	t.Helper()
	return describe(t, ip, file, false)
}

// describeExported is describePackage restricted to exported objects.
func describeExported(t *testing.T, ip, file string) string {
	t.Helper()
	return describe(t, ip, file, true)
}

func describe(t *testing.T, ip, file string, exportedOnly bool) string {
	t.Helper()
	pkg, err := readExportFile(token.NewFileSet(), map[string]*types.Package{}, ip, file)
	if err != nil {
		t.Fatalf("read %s export %s: %v", ip, file, err)
	}
	var lines []string
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if exportedOnly && !obj.Exported() {
			continue
		}
		lines = append(lines, objectString(obj))
		if tn, ok := obj.(*types.TypeName); ok {
			if named, ok := tn.Type().(*types.Named); ok {
				for m := range named.Methods() {
					if exportedOnly && !m.Exported() {
						continue
					}
					lines = append(lines, "  "+objectString(m))
				}
			}
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// objectString is types.ObjectString with function parameter and
// result NAMES dropped: they are not part of a function's type, and
// the compiler's export data names otherwise-unnamed results ("#rv1")
// where go/types leaves them unnamed.
func objectString(obj types.Object) string {
	fn, ok := obj.(*types.Func)
	if !ok {
		return types.ObjectString(obj, nil)
	}
	sig := fn.Signature()
	tuple := func(tup *types.Tuple, variadic bool) string {
		var parts []string
		for i := range tup.Len() {
			typ := tup.At(i).Type()
			if variadic && i == tup.Len()-1 {
				parts = append(parts, "..."+types.TypeString(typ.(*types.Slice).Elem(), nil))
				continue
			}
			parts = append(parts, types.TypeString(typ, nil))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	recv := ""
	if r := sig.Recv(); r != nil {
		recv = "(" + types.TypeString(r.Type(), nil) + ") "
	}
	return "func " + recv + fn.FullName() + tuple(sig.Params(), sig.Variadic()) + " " + tuple(sig.Results(), false)
}

// lineDiff lists the lines present in only one of a and b.
func lineDiff(a, b string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for l := range strings.SplitSeq(s, "\n") {
			m[l] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for l := range ma {
		if !mb[l] {
			out = append(out, "- plaid only:    "+l)
		}
	}
	for l := range mb {
		if !ma[l] {
			out = append(out, "+ compiler only: "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// TestStdlibExport_DeclaredFilesOnly: with an explicit file list, the
// run exports exactly the packages those files form — a package present
// on disk but undeclared is ignored, as a sandbox would hide it — and
// GOROOT/src/vendor resolution names vendored packages by their
// vendor/ identity.
func TestStdlibExport_DeclaredFilesOnly(t *testing.T) {
	goroot := t.TempDir()
	files := map[string]string{
		"src/top/top.go":                      "package top\n\nimport (\n\t\"golang.org/x/v\"\n\t\"mid\"\n)\n\nfunc Top() v.T { return v.T(mid.Mid()) }\n",
		"src/mid/mid.go":                      "package mid\n\nfunc Mid() int { return 1 }\n",
		"src/mid/mid_other.go":                "//go:build ignore\n\npackage mid\n\nfunc Broken() undefined\n",
		"src/vendor/golang.org/x/v/v.go":      "package v\n\ntype T int\n",
		"src/hidden/hidden.go":                "package hidden\n",
		"src/cmd/tool/tool.go":                "package main\n",
		"src/top/testdata/fixture/fixture.go": "package fixture\n",
		"src/mid/mid_test.go":                 "package mid\n",
	}
	var declared []string
	for rel, body := range files {
		p := filepath.Join(goroot, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rel, "hidden") {
			declared = append(declared, p)
		}
	}
	out := t.TempDir()
	res, err := ExportStdlib(context.Background(), StdlibConfig{
		GOROOT: goroot, GOOS: "linux", GOARCH: "amd64", GoVersion: "1.26", Out: out, Files: declared,
	})
	if err != nil {
		t.Fatalf("ExportStdlib: %v", err)
	}
	want := []string{"mid", "top", "vendor/golang.org/x/v"}
	if strings.Join(res.Packages, ",") != strings.Join(want, ",") {
		t.Errorf("exported %v, want %v", res.Packages, want)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", res.Warnings)
	}
	imports := map[string]*types.Package{}
	top, err := readExportFile(token.NewFileSet(), imports, "top", stdlibPathOf(out, "linux", "amd64", "top"))
	if err != nil {
		t.Fatal(err)
	}
	if got := objectString(top.Scope().Lookup("Top")); got != "func top.Top() (vendor/golang.org/x/v.T)" {
		t.Errorf("Top = %s", got)
	}
}
