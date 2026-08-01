// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"encoding/json"
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

// collectFixtureLib has an unchecked error call (errcheck, both runs)
// and an unexported func referenced only from the test file (unused
// in the library-only run, used in the test-variant run).
const (
	collectFixtureLib = `package fix

func mayFail() error { return nil }

// Used exercises errcheck: the mayFail call discards the error.
func Used() {
	mayFail()
}

// helper is referenced only from the in-package test file.
func helper() int { return 42 }
`

	collectFixtureTest = `package fix

var _ = helper()
`
)

// collectConfig enables errcheck (position-stable duplicate across
// runs) and unused (the linter the supersede rule exists for).
func collectConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, warnings, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - errcheck
    - unused
`), ".yml")
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	for _, w := range warnings {
		t.Logf("config warning: %s: %s", w.Field, w.Message)
	}
	return cfg
}

// runUnitFiles drives unit.Run for pkg over an explicit file set —
// runUnitOn with the file list decoupled from the load result, so a
// test can produce the library run and its test-variant superset run
// from one fixture.
func runUnitFiles(t *testing.T, pkg *packages.Package, goFiles []string, golangci *config.Config) (*Result, string) {
	t.Helper()
	outDir := t.TempDir()
	sarifPath := filepath.Join(outDir, "out.sarif")
	factsPath := filepath.Join(outDir, "out.plaidfacts")

	ucfg := &Config{
		Schema: SchemaVersion,
		Package: PackageConfig{
			Path:    pkg.PkgPath,
			GoFiles: goFiles,
			GOOS:    runtime.GOOS,
			GOARCH:  runtime.GOARCH,
		},
		Deps:     DepsConfig{Importcfg: writeImportcfg(t, outDir, pkg)},
		Analysis: AnalysisConfig{Mode: ModeFull},
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
	return res, sarifPath
}

// sarifResult builds one hand-written SARIF result entry.
func sarifResult(linter, msg, uri string, line, col int) map[string]any {
	return map[string]any{
		"ruleId":  linter,
		"level":   "warning",
		"message": map[string]any{"text": msg},
		"locations": []any{map[string]any{
			"physicalLocation": map[string]any{
				"artifactLocation": map[string]any{"uri": uri},
				"region":           map[string]any{"startLine": line, "startColumn": col},
			},
		}},
	}
}

// writeSarifFile writes a single-run SARIF file. An empty pkgPath
// omits the plaidUnit property bag entirely (a foreign producer).
func writeSarifFile(t *testing.T, path, pkgPath string, goFiles []string, results ...map[string]any) string {
	t.Helper()
	if results == nil {
		results = []map[string]any{}
	}
	run := map[string]any{"results": results}
	if pkgPath != "" {
		run["properties"] = map[string]any{"plaidUnit": map[string]any{
			"package": pkgPath,
			"mode":    "full",
			"goFiles": goFiles,
		}}
	}
	body, err := json.Marshal(map[string]any{"version": "2.1.0", "runs": []any{run}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o666); err != nil {
		t.Fatal(err)
	}
	return path
}

// countLinter counts collected diagnostics from one linter.
func countLinter(res *CollectResult, linter string) int {
	n := 0
	for _, d := range res.Diagnostics {
		if d.Linter == linter {
			n++
		}
	}
	return n
}

// TestCollect_SupersedeRealDriver drives the REAL producer: two unit
// runs of the same package, the second over the strict superset that
// includes the in-package test file. The library run's unused finding
// (a symbol referenced only from the test file) must be dropped; the
// errcheck finding present in both runs must dedup to one.
func TestCollect_SupersedeRealDriver(t *testing.T) {
	pkgs := buildFixture(t, map[string]string{
		"go.mod":    fixtureGoMod,
		"a.go":      collectFixtureLib,
		"a_test.go": collectFixtureTest,
	})
	pkg := pkgs["example.com/fix"]
	if pkg == nil {
		t.Fatalf("fixture package missing; loaded %v", pkgs)
	}
	cfg := collectConfig(t)

	libRes, libSarif := runUnitFiles(t, pkg, pkg.CompiledGoFiles, cfg)
	if !hasFinding(libRes, "unused", "helper") {
		t.Fatalf("library run should flag helper as unused; got %+v", libRes.Diagnostics)
	}
	if !hasFinding(libRes, "errcheck", "Error return value") {
		t.Fatalf("library run should flag the unchecked mayFail call; got %+v", libRes.Diagnostics)
	}

	testFiles := append(append([]string(nil), pkg.CompiledGoFiles...),
		filepath.Join(filepath.Dir(pkg.CompiledGoFiles[0]), "a_test.go"))
	testRes, testSarif := runUnitFiles(t, pkg, testFiles, cfg)
	if hasFinding(testRes, "unused", "helper") {
		t.Fatalf("test-variant run must not flag helper; got %+v", testRes.Diagnostics)
	}
	if !hasFinding(testRes, "errcheck", "Error return value") {
		t.Fatalf("test-variant run should still flag mayFail; got %+v", testRes.Diagnostics)
	}

	res, err := Collect([]string{libSarif, testSarif})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Superseded != 1 {
		t.Errorf("Superseded = %d, want 1", res.Superseded)
	}
	if n := countLinter(res, "unused"); n != 0 {
		t.Errorf("unused findings survived supersession: %d\n%+v", n, res.Diagnostics)
	}
	if n := countLinter(res, "errcheck"); n != 1 {
		t.Errorf("errcheck findings = %d, want 1 (position dedup)\n%+v", n, res.Diagnostics)
	}
}

// TestCollect_SupersedeStrictness pins the rule's boundary: only a
// STRICT superset of the file set, for the SAME package path,
// supersedes.
func TestCollect_SupersedeStrictness(t *testing.T) {
	unusedIn := func(uri string) map[string]any {
		return sarifResult("unused", "func helper is unused", uri, 3, 6)
	}
	cases := []struct {
		name           string
		pkgA, pkgB     string
		filesA, filesB []string
		wantSuperseded int
	}{
		{"StrictSupersetSamePkg", "example.com/p", "example.com/p",
			[]string{"a.go"}, []string{"a.go", "a_test.go"}, 1},
		{"EqualSets", "example.com/p", "example.com/p",
			[]string{"a.go", "b.go"}, []string{"b.go", "a.go"}, 0},
		{"DisjointSets", "example.com/p", "example.com/p",
			[]string{"a.go"}, []string{"b.go", "c.go"}, 0},
		{"SupersetDifferentPkg", "example.com/p", "example.com/q",
			[]string{"a.go"}, []string{"a.go", "a_test.go"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			a := writeSarifFile(t, filepath.Join(dir, "a.sarif"),
				tc.pkgA, tc.filesA, unusedIn("a.go"))
			b := writeSarifFile(t, filepath.Join(dir, "b.sarif"),
				tc.pkgB, tc.filesB)
			res, err := Collect([]string{a, b})
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if res.Superseded != tc.wantSuperseded {
				t.Errorf("Superseded = %d, want %d", res.Superseded, tc.wantSuperseded)
			}
			wantUnused := 1 - tc.wantSuperseded
			if n := countLinter(res, "unused"); n != wantUnused {
				t.Errorf("unused findings = %d, want %d\n%+v", n, wantUnused, res.Diagnostics)
			}
		})
	}
}

// TestCollect_NoPlaidUnitProps: a run without the plaidUnit property
// bag participates in position dedup but never in supersession — in
// either direction.
func TestCollect_NoPlaidUnitProps(t *testing.T) {
	dir := t.TempDir()
	// Run with properties: one unused finding, one errcheck finding.
	withProps := writeSarifFile(t, filepath.Join(dir, "props.sarif"),
		"example.com/p", []string{"a.go"},
		sarifResult("unused", "func helper is unused", "a.go", 3, 6),
		sarifResult("errcheck", "Error return value is not checked", "a.go", 8, 2),
	)
	// Foreign run, no properties, duplicating the errcheck finding.
	// It analyzed "more files" conceptually, but with no plaidUnit
	// bag it must never supersede.
	noProps := writeSarifFile(t, filepath.Join(dir, "foreign.sarif"),
		"", nil,
		sarifResult("errcheck", "Error return value is not checked", "a.go", 8, 2),
	)

	res, err := Collect([]string{withProps, noProps})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Superseded != 0 {
		t.Errorf("Superseded = %d, want 0 (props-less runs never supersede)", res.Superseded)
	}
	if n := countLinter(res, "unused"); n != 1 {
		t.Errorf("unused findings = %d, want 1\n%+v", n, res.Diagnostics)
	}
	if n := countLinter(res, "errcheck"); n != 1 {
		t.Errorf("errcheck findings = %d, want 1 (dedup across runs)\n%+v", n, res.Diagnostics)
	}
}

// TestCollect_DedupKey pins the identity of a finding:
// (file, line, column, linter, message). Any field differing keeps
// both findings.
func TestCollect_DedupKey(t *testing.T) {
	dir := t.TempDir()
	a := writeSarifFile(t, filepath.Join(dir, "a.sarif"), "", nil,
		sarifResult("errcheck", "msg A", "f.go", 3, 5),
		sarifResult("errcheck", "msg A", "f.go", 3, 5), // dup within one run
	)
	b := writeSarifFile(t, filepath.Join(dir, "b.sarif"), "", nil,
		sarifResult("errcheck", "msg A", "f.go", 3, 5), // dup across runs
		sarifResult("errcheck", "msg B", "f.go", 3, 5), // message differs
		sarifResult("govet", "msg A", "f.go", 3, 5),    // linter differs
		sarifResult("errcheck", "msg A", "f.go", 4, 5), // line differs
		sarifResult("errcheck", "msg A", "f.go", 3, 6), // column differs
		sarifResult("errcheck", "msg A", "g.go", 3, 5), // file differs
	)
	res, err := Collect([]string{a, b})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Diagnostics) != 6 {
		t.Fatalf("got %d diagnostics, want 6:\n%+v", len(res.Diagnostics), res.Diagnostics)
	}
	if n := countLinter(res, "errcheck"); n != 5 {
		t.Errorf("errcheck findings = %d, want 5", n)
	}
}

// TestCollect_Errors: unreadable or malformed inputs return an error
// (never panic) naming the failure.
func TestCollect_Errors(t *testing.T) {
	t.Run("MissingFile", func(t *testing.T) {
		_, err := Collect([]string{filepath.Join(t.TempDir(), "nope.sarif")})
		if err == nil {
			t.Fatal("want error for missing file")
		}
		if !strings.Contains(err.Error(), "collect") {
			t.Errorf("error should carry the collect prefix: %v", err)
		}
	})
	t.Run("MalformedJSON", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "bad.sarif")
		if err := os.WriteFile(p, []byte("{not sarif"), 0o666); err != nil {
			t.Fatal(err)
		}
		_, err := Collect([]string{p})
		if err == nil {
			t.Fatal("want error for malformed JSON")
		}
		if !strings.Contains(err.Error(), "parse") || !strings.Contains(err.Error(), p) {
			t.Errorf("error should name the file and the parse failure: %v", err)
		}
	})
	t.Run("MixedGoodBad", func(t *testing.T) {
		dir := t.TempDir()
		good := writeSarifFile(t, filepath.Join(dir, "good.sarif"), "", nil)
		_, err := Collect([]string{good, filepath.Join(dir, "missing.sarif")})
		if err == nil {
			t.Fatal("want error when any input is unreadable")
		}
	})
}
