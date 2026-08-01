// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// fixture is a compiled two-package module: dep declares a printf
// wrapper (a fact producer) and a deliberately unchecked error; root
// misuses the wrapper (only detectable through dep facts) and has its
// own local findings.
const (
	fixtureGoMod = "module example.com/fix\n\ngo 1.26\n"

	fixtureDep = `package dep

import "fmt"

// Logf is a printf wrapper; the printf analyzer records a fact about
// it that downstream packages consume.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}
`

	fixtureRoot = `package root

import "example.com/fix/dep"

func use() {
	dep.Logf("%d") // printf: missing arg — requires dep's wrapper fact
}
`
)

// testConfig is a minimal .golangci config enabling govet (printf) —
// the fact-flow proof — plus errcheck for a local finding.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, warnings, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - govet
    - errcheck
`), ".yml")
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	for _, w := range warnings {
		t.Logf("config warning: %s: %s", w.Field, w.Message)
	}
	return cfg
}

// buildFixture writes the module and compiles it via go/packages,
// returning the load result keyed by package path. Tests that
// GENERATE inputs use the toolchain; the unit driver under test never
// does (see TestUnit_HermeticNoToolchain).
func buildFixture(t *testing.T, files map[string]string) map[string]*packages.Package {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedExportFile |
			packages.NeedModule,
		Dir: dir,
		Env: append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	out := map[string]*packages.Package{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if len(p.Errors) > 0 {
			for _, e := range p.Errors {
				t.Logf("package %s error: %v", p.PkgPath, e)
			}
		}
		out[p.PkgPath] = p
	})
	return out
}

// writeImportcfg renders the transitive importcfg for pkg from the
// load result (every dep with export data).
func writeImportcfg(t *testing.T, dir string, pkg *packages.Package) string {
	t.Helper()
	var b strings.Builder
	seen := map[string]bool{}
	var walk func(p *packages.Package)
	walk = func(p *packages.Package) {
		for path, imp := range p.Imports {
			if seen[path] {
				continue
			}
			seen[path] = true
			if imp.ExportFile != "" {
				fmt.Fprintf(&b, "packagefile %s=%s\n", path, imp.ExportFile)
			}
			walk(imp)
		}
	}
	walk(pkg)
	p := filepath.Join(dir, strings.ReplaceAll(pkg.PkgPath, "/", "_")+".importcfg")
	if err := os.WriteFile(p, []byte(b.String()), 0o666); err != nil {
		t.Fatal(err)
	}
	return p
}

// runUnitOn drives unit.Run for one loaded package.
func runUnitOn(t *testing.T, pkg *packages.Package, mode Mode, depFacts map[string]string, golangci *config.Config) (*Result, string, string) {
	t.Helper()
	outDir := t.TempDir()
	sarifPath := filepath.Join(outDir, "out.sarif")
	factsPath := filepath.Join(outDir, "out.plaidfacts")

	ucfg := &Config{
		Schema: SchemaVersion,
		Package: PackageConfig{
			Path:    pkg.PkgPath,
			GoFiles: pkg.CompiledGoFiles,
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
		},
		Deps: DepsConfig{
			Importcfg: writeImportcfg(t, outDir, pkg),
			Facts:     depFacts,
		},
		Analysis: AnalysisConfig{Mode: mode},
		Out:      OutConfig{Sarif: sarifPath, Facts: factsPath},
	}
	if pkg.Module != nil {
		ucfg.Module.Path = pkg.Module.Path
	}

	reg, _, err := registry.BuildFromConfig(golangci)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	filter, err := exclusion.NewFilter(golangci, "", nil)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	res, err := Run(context.Background(), ucfg, golangci, reg, filter)
	if err != nil {
		t.Fatalf("unit.Run(%s): %v", pkg.PkgPath, err)
	}
	return res, sarifPath, factsPath
}

// TestUnit_FactFlow proves the load-bearing property: a fact produced
// while analyzing a dependency (printf wrapper) changes the
// diagnostics of the dependent package, flowing exclusively through the
// .plaidfacts file.
func TestUnit_FactFlow(t *testing.T) {
	golangci := testConfig(t)
	pkgs := buildFixture(t, map[string]string{
		"go.mod":       fixtureGoMod,
		"dep/dep.go":   fixtureDep,
		"root/root.go": fixtureRoot,
	})
	dep := pkgs["example.com/fix/dep"]
	root := pkgs["example.com/fix/root"]
	if dep == nil || root == nil {
		t.Fatalf("fixture packages missing: %v", pkgs)
	}

	// 1. Analyze dep in facts_only mode: no diagnostics, facts file.
	depRes, _, depFacts := runUnitOn(t, dep, ModeFactsOnly, nil, testConfig(t))
	if len(depRes.Diagnostics) != 0 {
		t.Errorf("facts_only produced diagnostics: %v", depRes.Diagnostics)
	}
	blob, err := os.ReadFile(depFacts)
	if err != nil {
		t.Fatalf("dep facts missing: %v", err)
	}
	if payload, err := unwrapFacts(blob); err != nil {
		t.Fatalf("dep facts container: %v", err)
	} else if len(payload) == 0 {
		t.Fatalf("dep produced no facts; expected printf wrapper fact")
	}

	// 2. Analyze root WITH dep facts: printf finding expected.
	withRes, _, _ := runUnitOn(t, root, ModeFull,
		map[string]string{"example.com/fix/dep": depFacts}, golangci)
	if !hasFinding(withRes, "printf", "Logf") {
		t.Errorf("expected printf diagnostic about dep.Logf with facts; got %+v", withRes.Diagnostics)
	}

	// 3. Analyze root WITHOUT dep facts: the printf finding must
	// disappear (the wrapper fact is the only path to it).
	withoutRes, _, _ := runUnitOn(t, root, ModeFull, nil, testConfig(t))
	if hasFinding(withoutRes, "printf", "Logf") {
		t.Errorf("printf diagnostic present without dep facts — facts are not flowing through files: %+v", withoutRes.Diagnostics)
	}
}

func hasFinding(res *Result, linter, substr string) bool {
	for _, d := range res.Diagnostics {
		if d.Linter == linter && strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

// TestUnit_Determinism runs the same action twice and requires
// byte-identical outputs.
func TestUnit_Determinism(t *testing.T) {
	golangci := testConfig(t)
	pkgs := buildFixture(t, map[string]string{
		"go.mod":       fixtureGoMod,
		"dep/dep.go":   fixtureDep,
		"root/root.go": fixtureRoot,
	})
	dep := pkgs["example.com/fix/dep"]

	_, sarif1, facts1 := runUnitOn(t, dep, ModeFull, nil, golangci)
	_, sarif2, facts2 := runUnitOn(t, dep, ModeFull, nil, testConfig(t))

	for _, pair := range [][2]string{{sarif1, sarif2}, {facts1, facts2}} {
		a, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("outputs differ between identical runs: %s vs %s", pair[0], pair[1])
		}
	}
}

// TestUnit_TypecheckFindings: a package that does not compile yields
// `typecheck` findings, zero analyzer findings, and all declared
// outputs.
func TestUnit_TypecheckFindings(t *testing.T) {
	golangci := testConfig(t)
	pkgs := buildFixture(t, map[string]string{
		"go.mod":       fixtureGoMod,
		"dep/dep.go":   fixtureDep,
		"root/root.go": "package root\n\nfunc use() { undefinedSymbol() }\n",
	})
	root := pkgs["example.com/fix/root"]

	res, sarifPath, factsPath := runUnitOn(t, root, ModeFull, nil, golangci)
	if !hasFinding(res, "typecheck", "undefinedSymbol") {
		t.Errorf("expected typecheck finding; got %+v", res.Diagnostics)
	}
	for _, d := range res.Diagnostics {
		if d.Linter != "typecheck" {
			t.Errorf("analyzer %s ran on a non-compiling package", d.Linter)
		}
	}
	for _, p := range []string{sarifPath, factsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("declared output missing after typecheck failure: %v", err)
		}
	}
	blob, _ := os.ReadFile(factsPath)
	if payload, err := unwrapFacts(blob); err != nil || len(payload) != 0 {
		t.Errorf("facts for non-compiling package: payload=%d err=%v", len(payload), err)
	}
}

// TestUnit_Nolint: a nolint comment suppresses a finding through the
// shared exclusion filter (which parses the real source file).
func TestUnit_Nolint(t *testing.T) {
	golangci := testConfig(t)
	pkgs := buildFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"dep/dep.go": `package dep

import "os"

func mk() {
	os.Mkdir("x", 0o777) //nolint:errcheck // test suppression
}
`,
	})
	dep := pkgs["example.com/fix/dep"]
	res, _, _ := runUnitOn(t, dep, ModeFull, nil, golangci)
	for _, d := range res.Diagnostics {
		if d.Linter == "errcheck" {
			t.Errorf("nolint did not suppress errcheck: %+v", d)
		}
	}
}

// TestUnit_ModuleMode: gomoddirectives fires on a disallowed replace
// from declared inputs only.
func TestUnit_ModuleMode(t *testing.T) {
	dir := t.TempDir()
	goMod := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(goMod, []byte(
		"module example.com/fix\n\ngo 1.26\n\nreplace example.com/other => ../other\n"), 0o666); err != nil {
		t.Fatal(err)
	}
	golangci, _, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - gomoddirectives
`), ".yml")
	if err != nil {
		t.Fatal(err)
	}
	reg, _, err := registry.BuildFromConfig(golangci)
	if err != nil {
		t.Fatal(err)
	}
	sarifPath := filepath.Join(dir, "out.sarif")
	ucfg := &Config{
		Schema:   SchemaVersion,
		Module:   ModuleConfig{GoMod: goMod, Path: "example.com/fix"},
		Analysis: AnalysisConfig{Mode: ModeModule},
		Out:      OutConfig{Sarif: sarifPath},
	}
	filter, err := exclusion.NewFilter(golangci, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), ucfg, golangci, reg, filter)
	if err != nil {
		t.Fatalf("module mode: %v", err)
	}
	found := false
	for _, d := range res.Diagnostics {
		if d.Linter == "gomoddirectives" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gomoddirectives finding for local replace; got %+v", res.Diagnostics)
	}
	if _, err := os.Stat(sarifPath); err != nil {
		t.Errorf("sarif output missing: %v", err)
	}
}

// TestUnit_ConfigRejectsUnknownFields pins schema strictness.
func TestUnit_ConfigRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "unit.json")
	body, _ := json.Marshal(map[string]any{
		"schema":  SchemaVersion,
		"package": map[string]any{"path": "x", "go_files": []string{"a.go"}, "goarch": "arm64"},
		"out":     map[string]any{"sarif": "s", "facts": "f"},
		"bogus":   true,
	})
	if err := os.WriteFile(p, body, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("unknown field accepted")
	}
}
