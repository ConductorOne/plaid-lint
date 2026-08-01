// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/conductorone/plaid-lint/internal/unit"
)

// The helpers below mirror internal/unit's driver_test fixtures
// without importing test code: tests GENERATE unit inputs with the Go
// toolchain (go/packages with NeedExportFile), and the `unit`
// subcommand under test then runs from those declared inputs only.

// buildUnitFixture writes files into a temp module dir and compiles it
// via go/packages, returning the module dir and the load result keyed
// by package path.
func buildUnitFixture(t *testing.T, files map[string]string) (string, map[string]*packages.Package) {
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
		for _, e := range p.Errors {
			t.Logf("package %s error: %v", p.PkgPath, e)
		}
		out[p.PkgPath] = p
	})
	return dir, out
}

// writeUnitImportcfg renders the transitive importcfg for pkg (every
// dep with export data) into dir.
func writeUnitImportcfg(t *testing.T, dir string, pkg *packages.Package) string {
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

// writeUnitCfg writes a complete unit.json for pkg into a fresh temp
// dir and returns the config path plus the declared output paths.
func writeUnitCfg(t *testing.T, pkg *packages.Package, golangciPath string, depFacts map[string]string) (cfgPath, sarifPath, factsPath string) {
	t.Helper()
	outDir := t.TempDir()
	sarifPath = filepath.Join(outDir, "out.sarif")
	factsPath = filepath.Join(outDir, "out.plaidfacts")

	ucfg := unit.Config{
		Schema: unit.SchemaVersion,
		Package: unit.PackageConfig{
			Path:    pkg.PkgPath,
			GoFiles: pkg.CompiledGoFiles,
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
		},
		Deps: unit.DepsConfig{
			Importcfg: writeUnitImportcfg(t, outDir, pkg),
			Facts:     depFacts,
		},
		Analysis: unit.AnalysisConfig{Config: golangciPath},
		Out:      unit.OutConfig{Sarif: sarifPath, Facts: factsPath},
	}
	if pkg.Module != nil {
		ucfg.Module.Path = pkg.Module.Path
	}

	body, err := json.MarshalIndent(ucfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal unit.json: %v", err)
	}
	cfgPath = filepath.Join(outDir, "unit.json")
	if err := os.WriteFile(cfgPath, body, 0o666); err != nil {
		t.Fatal(err)
	}
	return cfgPath, sarifPath, factsPath
}

// sarifResult is the black-box slice of a SARIF result the tests
// assert on.
type sarifResult struct {
	RuleID  string `json:"ruleId"`
	Message struct {
		Text string `json:"text"`
	} `json:"message"`
	Locations []struct {
		PhysicalLocation struct {
			ArtifactLocation struct {
				URI string `json:"uri"`
			} `json:"artifactLocation"`
			Region struct {
				StartLine int `json:"startLine"`
			} `json:"region"`
		} `json:"physicalLocation"`
	} `json:"locations"`
}

// readSarifResults parses a SARIF file and returns runs[0].results.
func readSarifResults(t *testing.T, path string) []sarifResult {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sarif %s: %v", path, err)
	}
	var doc struct {
		Runs []struct {
			Results []sarifResult `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse sarif %s: %v", path, err)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("sarif %s: got %d runs, want 1", path, len(doc.Runs))
	}
	return doc.Runs[0].Results
}

// readSarifResultsRaw returns runs[0].results as raw JSON for
// byte-identity comparisons.
func readSarifResultsRaw(t *testing.T, path string) json.RawMessage {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sarif %s: %v", path, err)
	}
	var doc struct {
		Runs []struct {
			Results json.RawMessage `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse sarif %s: %v", path, err)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("sarif %s: got %d runs, want 1", path, len(doc.Runs))
	}
	return doc.Runs[0].Results
}

// unitCLIFixtureFiles is a one-package module with a seeded errcheck
// violation (the unchecked os.Remove).
var unitCLIFixtureFiles = map[string]string{
	"go.mod": "module example.com/unitcli\n\ngo 1.26\n",
	"scratch/scratch.go": `package scratch

import "os"

// Touch has a deliberately unchecked error return (errcheck).
func Touch() {
	os.Remove("scratch-file")
}
`,
}

// writeErrcheckGolangci writes a minimal errcheck-only .golangci
// config into dir and returns its path.
func writeErrcheckGolangci(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "golangci.unit.yml")
	body := `version: "2"
linters:
  default: none
  enable:
    - errcheck
`
	if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUnitCLI_MissingCfgFlag: `unit` without --cfg is a flag error.
func TestUnitCLI_MissingCfgFlag(t *testing.T) {
	code, _, stderr := runApp(t, t.TempDir(), "unit")
	if code != exitCLIError {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitCLIError, stderr)
	}
	if !strings.Contains(stderr, "--cfg is required") {
		t.Errorf("stderr=%q; want it to mention --cfg is required", stderr)
	}
}

// TestUnitCLI_MissingCfgFile: --cfg naming a nonexistent file is an
// unusable input, not a flag error.
func TestUnitCLI_MissingCfgFile(t *testing.T) {
	dir := t.TempDir()
	code, _, stderr := runApp(t, dir, "unit", "--cfg", filepath.Join(dir, "no-such-unit.json"))
	if code != exitInternalError {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitInternalError, stderr)
	}
	if !strings.Contains(stderr, "read config") {
		t.Errorf("stderr=%q; want a read-config error", stderr)
	}
}

// TestUnitCLI_UnsupportedSchema: a unit.json declaring a future schema
// version is rejected loudly as an unusable input.
func TestUnitCLI_UnsupportedSchema(t *testing.T) {
	dir := t.TempDir()
	ucfg := unit.Config{
		Schema: 99,
		Package: unit.PackageConfig{
			Path:    "example.com/x",
			GoFiles: []string{"x.go"},
			GOARCH:  runtime.GOARCH,
		},
		Out: unit.OutConfig{Sarif: "out.sarif", Facts: "out.plaidfacts"},
	}
	body, err := json.Marshal(ucfg)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "unit.json")
	if err := os.WriteFile(cfgPath, body, 0o666); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := runApp(t, dir, "unit", "--cfg", cfgPath)
	if code != exitInternalError {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitInternalError, stderr)
	}
	if !strings.Contains(stderr, "schema 99") {
		t.Errorf("stderr=%q; want a schema-version rejection", stderr)
	}
}

// TestUnitCLI_SuccessOutputsAndFindings: a valid one-package action
// exits 0, writes both declared outputs, and records the seeded
// errcheck violation in the SARIF — findings are data, never an exit
// code.
func TestUnitCLI_SuccessOutputsAndFindings(t *testing.T) {
	dir, pkgs := buildUnitFixture(t, unitCLIFixtureFiles)
	pkg := pkgs["example.com/unitcli/scratch"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeErrcheckGolangci(t, dir)
	cfgPath, sarifPath, factsPath := writeUnitCfg(t, pkg, golangci, nil)

	code, _, stderr := runApp(t, dir, "unit", "--cfg", cfgPath)
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitSuccess, stderr)
	}

	results := readSarifResults(t, sarifPath)
	var found bool
	for _, r := range results {
		if r.RuleID != "errcheck" {
			continue
		}
		if len(r.Locations) != 1 {
			t.Fatalf("errcheck result has %d locations: %+v", len(r.Locations), r)
		}
		loc := r.Locations[0].PhysicalLocation
		if filepath.Base(loc.ArtifactLocation.URI) != "scratch.go" {
			t.Errorf("errcheck at %q; want scratch.go", loc.ArtifactLocation.URI)
		}
		if loc.Region.StartLine != 7 {
			t.Errorf("errcheck at line %d; want 7 (the os.Remove call)", loc.Region.StartLine)
		}
		found = true
	}
	if !found {
		t.Errorf("seeded errcheck violation missing from SARIF: %+v", results)
	}

	// The facts output must exist and carry the .plaidfacts framing.
	facts, err := os.ReadFile(factsPath)
	if err != nil {
		t.Fatalf("declared facts output missing: %v", err)
	}
	if len(facts) < 4 || string(facts[:3]) != "PLF" {
		t.Errorf("facts output %q lacks the PLF header", facts[:min(len(facts), 8)])
	}
}

// TestUnitCLI_HermeticNoToolchainEnv proves the driver never reaches
// through the environment: after the inputs are generated with the
// toolchain, the same action re-run with PATH/GOROOT/GOPATH/HOME all
// pointing at a nonexistent directory must still exit 0 with identical
// SARIF results. runApp runs in-process, so an exec.Command escape
// (which resolves via PATH) or a toolchain/homedir lookup would
// surface here as a failure or a diverging result.
func TestUnitCLI_HermeticNoToolchainEnv(t *testing.T) {
	dir, pkgs := buildUnitFixture(t, unitCLIFixtureFiles)
	pkg := pkgs["example.com/unitcli/scratch"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeErrcheckGolangci(t, dir)
	cfg1, sarif1, _ := writeUnitCfg(t, pkg, golangci, nil)
	cfg2, sarif2, _ := writeUnitCfg(t, pkg, golangci, nil)

	// Baseline run with the real environment.
	code, _, stderr := runApp(t, dir, "unit", "--cfg", cfg1)
	if code != exitSuccess {
		t.Fatalf("baseline exit=%d stderr=%q", code, stderr)
	}

	// Poison everything discovery could reach. The importcfg and the
	// unit.json paths are absolute, so nothing below needs the env.
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("GOROOT", "/nonexistent")
	t.Setenv("GOPATH", "/nonexistent")
	t.Setenv("HOME", "/nonexistent")

	code, _, stderr = runApp(t, dir, "unit", "--cfg", cfg2)
	if code != exitSuccess {
		t.Fatalf("hermetic exit=%d stderr=%q — unit reached through the environment", code, stderr)
	}

	res1 := readSarifResultsRaw(t, sarif1)
	res2 := readSarifResultsRaw(t, sarif2)
	if !bytes.Equal(res1, res2) {
		t.Errorf("SARIF results differ with a poisoned environment:\nbaseline: %s\nhermetic: %s", res1, res2)
	}
	if len(readSarifResults(t, sarif2)) == 0 {
		t.Errorf("hermetic run produced no findings; expected the seeded errcheck violation")
	}
}
