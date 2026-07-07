// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestDropTestSupersededPackages asserts the package-set filter that
// feeds the whole-program `unused` analyzer keeps exactly the variant
// that sees test-file usage:
//
//   - a package with an in-package test variant → the plain variant is
//     dropped (its superset "p [p.test]" survives and carries p.go +
//     p_test.go, so a symbol used only from p_test.go is not flagged);
//   - an external-test-only package → the plain variant is KEPT (there
//     is no in-package superset that contains p.go, so dropping it
//     would leave p.go unanalyzed — a false negative);
//   - a package with no tests → kept.
func TestDropTestSupersededPackages(t *testing.T) {
	pkg := func(id, pkgPath, forTest string) *metadata.Package {
		return &metadata.Package{
			ID:      metadata.PackageID(id),
			PkgPath: metadata.PackagePath(pkgPath),
			ForTest: metadata.PackagePath(forTest),
		}
	}

	pkgs := map[metadata.PackageID]*metadata.Package{
		// Package with in-package tests: plain + in-package variant.
		"m/foo":              pkg("m/foo", "m/foo", ""),
		"m/foo [m/foo.test]": pkg("m/foo [m/foo.test]", "m/foo", "m/foo"),

		// Package with only external tests: plain + external variant.
		// No in-package "m/ext [m/ext.test]" superset exists.
		"m/ext":                   pkg("m/ext", "m/ext", ""),
		"m/ext_test [m/ext.test]": pkg("m/ext_test [m/ext.test]", "m/ext_test", "m/ext"),

		// Package with no tests at all.
		"m/notest": pkg("m/notest", "m/notest", ""),
	}

	dropTestSupersededPackages(pkgs)

	if _, ok := pkgs["m/foo"]; ok {
		t.Errorf("plain m/foo not dropped despite in-package test variant present")
	}
	if _, ok := pkgs["m/foo [m/foo.test]"]; !ok {
		t.Errorf("in-package test variant m/foo [m/foo.test] must be retained")
	}
	if _, ok := pkgs["m/ext"]; !ok {
		t.Errorf("plain m/ext dropped although only an external test variant exists; p.go would go unanalyzed")
	}
	if _, ok := pkgs["m/ext_test [m/ext.test]"]; !ok {
		t.Errorf("external test package must be retained")
	}
	if _, ok := pkgs["m/notest"]; !ok {
		t.Errorf("test-less package m/notest must be retained")
	}
}

// TestRun_UnusedCountsTestFileUsage is the end-to-end regression test
// for the "unused ignores _test.go" false positive. It drives
// engine.Run (the production path, including the test-variant package
// filter) over a real module with the `unused` linter enabled and
// asserts BOTH directions:
//
//   - a symbol used only from an in-package _test.go is NOT reported
//     (the bug: it used to be flagged unused);
//   - a genuinely unused symbol in the same file IS still reported
//     (guards against overcorrecting into a false negative);
//   - a genuinely unused unexported symbol in an external-test-only
//     package IS still reported (guards the filter from dropping the
//     only variant that contains the source file).
func TestRun_UnusedCountsTestFileUsage(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %q: %v", full, err)
		}
	}

	mustWrite("go.mod", "module unusedtest\n\ngo 1.21\n")

	// foo: in-package test uses helperUsedOnlyInTest; trulyUnused is
	// dead even accounting for the test file.
	mustWrite("foo/foo.go", "package foo\n\n"+
		"func helperUsedOnlyInTest() int { return 42 }\n\n"+
		"func trulyUnused() int { return 7 }\n")
	mustWrite("foo/foo_test.go", "package foo\n\n"+
		"import \"testing\"\n\n"+
		"func TestX(t *testing.T) { _ = helperUsedOnlyInTest() }\n")

	// ext: only an external test package; extUnused is genuinely dead
	// and must still be flagged after the filter runs.
	mustWrite("ext/ext.go", "package ext\n\n"+
		"func Exported() int { return 1 }\n\n"+
		"func extUnused() int { return 3 }\n")
	mustWrite("ext/ext_x_test.go", "package ext_test\n\n"+
		"import (\n\t\"testing\"\n\n\t\"unusedtest/ext\"\n)\n\n"+
		"func TestExported(t *testing.T) { _ = ext.Exported() }\n")

	l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
	if err != nil {
		t.Fatalf("open L1: %v", err)
	}
	l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
	if err != nil {
		t.Fatalf("open L2: %v", err)
	}

	cfg := config.NewDefault()
	cfg.Linters.Default = config.GroupNone
	cfg.Linters.Enable = []string{"unused"}
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}

	res, err := Run(context.Background(), RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: dir},
		L1:        l1,
		L2:        l2,
	})
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	var unusedMsgs []string
	for _, d := range res.Diagnostics {
		if d.Linter == "unused" {
			unusedMsgs = append(unusedMsgs, d.Message)
		}
	}

	reported := func(name string) bool {
		for _, m := range unusedMsgs {
			if strings.Contains(m, " "+name+" is unused") {
				return true
			}
		}
		return false
	}

	if reported("helperUsedOnlyInTest") {
		t.Errorf("helperUsedOnlyInTest reported unused, but it is used from foo_test.go; got %v", unusedMsgs)
	}
	if !reported("trulyUnused") {
		t.Errorf("trulyUnused not reported unused; got %v", unusedMsgs)
	}
	if !reported("extUnused") {
		t.Errorf("extUnused not reported unused (false negative in external-test-only package); got %v", unusedMsgs)
	}
}
