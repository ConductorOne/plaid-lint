// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"encoding/json"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// The constraint fixtures are self-contained (no imports), so the
// driver runs without an importcfg or compiled dependencies — the
// tests exercise only file selection and its downstream effects.
const (
	// armImpl / noasmImpl declare the SAME function under mutually
	// exclusive //go:build directives — the c1 //pkg/randkey shape
	// that made the unfiltered driver report "hexExpand redeclared".
	// Each also seeds one errcheck finding so kept-vs-excluded is
	// observable in the diagnostics, not just in the absence of a
	// typecheck error.
	armImpl = `//go:build arm64

package tagged

func hexExpand() error { return nil }

// ArmUse ignores hexExpand's error: the arm64 file's errcheck seed.
func ArmUse() { hexExpand() }
`

	noasmImpl = `//go:build !arm64

package tagged

func hexExpand() error { return nil }

// NoasmUse ignores hexExpand's error: the !arm64 file's errcheck seed.
func NoasmUse() { hexExpand() }
`
)

// runConstraintFixture writes the files, runs the unit driver over
// them with the given GOARCH/tags, and returns the result plus the
// SARIF run's goFiles property (the analyzed-file identity).
func runConstraintFixture(t *testing.T, files map[string]string, goarch string, tags []string) (*Result, []string) {
	t.Helper()
	dir := t.TempDir()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	goFiles := make([]string, 0, len(names))
	for _, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(files[name]), 0o666); err != nil {
			t.Fatal(err)
		}
		goFiles = append(goFiles, p)
	}

	outDir := t.TempDir()
	sarifPath := filepath.Join(outDir, "out.sarif")
	ucfg := &Config{
		Schema: SchemaVersion,
		Package: PackageConfig{
			Path:      "example.com/fix/tagged",
			GoFiles:   goFiles,
			GOOS:      "linux",
			GOARCH:    goarch,
			Tags:      tags,
			GoVersion: "1.26",
		},
		Analysis: AnalysisConfig{Mode: ModeFull},
		Out: OutConfig{
			Sarif: sarifPath,
			Facts: filepath.Join(outDir, "out.plaidfacts"),
		},
	}

	golangci := testConfig(t)
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
		t.Fatalf("unit.Run: %v", err)
	}
	return res, sarifGoFiles(t, sarifPath)
}

// sarifGoFiles extracts runs[0].properties.plaid.goFiles from the
// written SARIF report.
func sarifGoFiles(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sarif: %v", err)
	}
	var doc struct {
		Runs []struct {
			Properties struct {
				PlaidUnit struct {
					GoFiles []string `json:"goFiles"`
				} `json:"plaidUnit"`
			} `json:"properties"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("parse sarif: %v", err)
	}
	if len(doc.Runs) != 1 {
		t.Fatalf("expected 1 sarif run, got %d", len(doc.Runs))
	}
	return doc.Runs[0].Properties.PlaidUnit.GoFiles
}

// assertKept requires that (1) no redeclaration surfaced, (2) every
// diagnostic and the SARIF goFiles identity name only files whose
// base name is in kept, and (3) when wantFinding is non-empty, at
// least one diagnostic landed in that kept file.
func assertKept(t *testing.T, res *Result, goFiles []string, kept []string, wantFinding string) {
	t.Helper()
	keptSet := map[string]bool{}
	for _, k := range kept {
		keptSet[k] = true
	}
	for _, d := range res.Diagnostics {
		if strings.Contains(d.Message, "redeclared") {
			t.Errorf("unexpected redeclaration diagnostic: %s: %s", d.Linter, d.Message)
		}
		if base := filepath.Base(d.Pos.Filename); !keptSet[base] {
			t.Errorf("diagnostic in excluded file %s: %s: %s", base, d.Linter, d.Message)
		}
	}
	if len(goFiles) != len(kept) {
		t.Fatalf("sarif goFiles = %v, want bases %v", goFiles, kept)
	}
	for _, f := range goFiles {
		if !keptSet[filepath.Base(f)] {
			t.Errorf("sarif goFiles contains excluded file %s (all: %v)", f, goFiles)
		}
	}
	if wantFinding != "" {
		found := false
		for _, d := range res.Diagnostics {
			if filepath.Base(d.Pos.Filename) == wantFinding {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a finding in kept file %s; diagnostics: %+v", wantFinding, res.Diagnostics)
		}
	}
}

// TestUnit_BuildConstraints_GoBuildDirective: two files declare the
// same function under //go:build arm64 / !arm64; only the file
// matching the configured GOARCH is parsed, type-checked, analyzed,
// and reported in the SARIF file identity.
func TestUnit_BuildConstraints_GoBuildDirective(t *testing.T) {
	files := map[string]string{
		"impl_asm.go":   armImpl,
		"impl_noasm.go": noasmImpl,
	}

	res, goFiles := runConstraintFixture(t, files, "arm64", nil)
	assertKept(t, res, goFiles, []string{"impl_asm.go"}, "impl_asm.go")

	res, goFiles = runConstraintFixture(t, files, "amd64", nil)
	assertKept(t, res, goFiles, []string{"impl_noasm.go"}, "impl_noasm.go")
}

// TestUnit_BuildConstraints_FilenameImplied: a file named
// foo_arm64.go carries the GOARCH constraint in its name alone (no
// //go:build directive), exactly like the Go toolchain's implied
// constraint rule.
func TestUnit_BuildConstraints_FilenameImplied(t *testing.T) {
	files := map[string]string{
		"foo_arm64.go": `package tagged

func hexExpand() error { return nil }

// ArmUse ignores hexExpand's error: the arm64 file's errcheck seed.
func ArmUse() { hexExpand() }
`,
		"foo_portable.go": noasmImpl,
	}

	res, goFiles := runConstraintFixture(t, files, "arm64", nil)
	assertKept(t, res, goFiles, []string{"foo_arm64.go"}, "foo_arm64.go")

	res, goFiles = runConstraintFixture(t, files, "amd64", nil)
	assertKept(t, res, goFiles, []string{"foo_portable.go"}, "foo_portable.go")
}

// TestUnit_BuildConstraints_Tags: a //go:build mytag file is kept
// only when the config declares the tag, mirroring the compile
// action's -tags.
func TestUnit_BuildConstraints_Tags(t *testing.T) {
	files := map[string]string{
		"base.go": `package tagged

// Base anchors the package on every configuration.
func Base() {}
`,
		"extra.go": `//go:build mytag

package tagged

func tagOnly() error { return nil }

// TagUse ignores tagOnly's error: the tagged file's errcheck seed.
func TagUse() { tagOnly() }
`,
	}

	res, goFiles := runConstraintFixture(t, files, "arm64", []string{"mytag"})
	assertKept(t, res, goFiles, []string{"base.go", "extra.go"}, "extra.go")

	res, goFiles = runConstraintFixture(t, files, "arm64", nil)
	assertKept(t, res, goFiles, []string{"base.go"}, "")
}

// TestUnit_BuildConstraints_AllExcluded: a package whose declared
// sources are ALL excluded by constraints behaves like the
// empty-after-test-filter case — the run succeeds with no findings,
// no warnings, and both declared outputs written (empty facts,
// empty-file SARIF).
func TestUnit_BuildConstraints_AllExcluded(t *testing.T) {
	files := map[string]string{
		"impl_asm.go": armImpl,
		"only_amd64.go": `//go:build amd64

package tagged

// Amd64Only never builds in this test's configurations.
func Amd64Only() {}
`,
	}

	// GOARCH riscv64 matches neither arm64 nor amd64; noasmImpl is
	// deliberately absent so nothing survives the filter.
	res, goFiles := runConstraintFixture(t, files, "riscv64", nil)
	if len(res.Diagnostics) != 0 {
		t.Errorf("expected no diagnostics from an all-excluded package, got %+v", res.Diagnostics)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("expected no warnings (empty-package runs are silent), got %v", res.Warnings)
	}
	if len(goFiles) != 0 {
		t.Errorf("expected empty sarif goFiles, got %v", goFiles)
	}
}

// TestFilterByConstraints_Cgo pins the builder-mirroring cgo rules:
// with cgo off, files importing "C" are dropped after constraint
// matching (rules_go filter.go: matched && (CgoEnabled || !isCgo)) and
// //go:build cgo files are excluded by constraint; with cgo on, both
// stay in the matched set (and a broken package then surfaces as
// does-not-compile rather than a silently wrong file subset).
func TestFilterByConstraints_Cgo(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
		return p
	}
	plain := write("plain.go", "package p\n")
	importC := write("uses_c.go", "package p\n\nimport \"C\"\n")
	tagged := write("cgo_tagged.go", "//go:build cgo\n\npackage p\n")

	pkg := &PackageConfig{
		GOOS:    "linux",
		GOARCH:  "arm64",
		GoFiles: []string{plain, importC, tagged},
	}
	matched, excluded, err := filterByConstraints(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 1 || filepath.Base(matched[0]) != "plain.go" {
		t.Fatalf("cgo off: matched = %v, want [plain.go]", matched)
	}
	if len(excluded) != 2 {
		t.Fatalf("cgo off: excluded = %v, want both cgo files", excluded)
	}

	pkg.Cgo = true
	matched, excluded, err = filterByConstraints(pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(matched) != 3 || len(excluded) != 0 {
		t.Fatalf("cgo on: matched = %v excluded = %v, want all matched", matched, excluded)
	}
}

// TestReleaseTags_Prerelease pins minor extraction across release and
// prerelease SDK version strings: "1.26rc1" carries go1.26 exactly
// like "1.26" (the toolchain's prerelease behavior); junk falls back
// to the compiled-in tags.
func TestReleaseTags_Prerelease(t *testing.T) {
	for _, tc := range []struct {
		version string
		last    string
	}{
		{"1.26", "go1.26"},
		{"1.26rc1", "go1.26"},
		{"1.26beta2", "go1.26"},
		{"1.26.5", "go1.26"},
	} {
		tags := releaseTags(tc.version)
		if len(tags) == 0 || tags[len(tags)-1] != tc.last {
			t.Errorf("releaseTags(%q) last = %v, want %s", tc.version, tags, tc.last)
		}
	}
	for _, junk := range []string{"", "2.0", "1.x", "1."} {
		tags := releaseTags(junk)
		if len(tags) != len(build.Default.ReleaseTags) {
			t.Errorf("releaseTags(%q) = %v, want build.Default fallback", junk, tags)
		}
	}
}
