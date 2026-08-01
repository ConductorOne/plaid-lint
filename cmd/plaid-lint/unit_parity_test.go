// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Parity fixture: a three-package module (root → mid → leaf) seeded
// with violations across every enabled linter. The cross-package
// printf misuse in mid (leaf.Logf) is the load-bearing seed: it is
// only detectable through leaf's printf-wrapper fact, so it proves the
// unit driver's fact threading reproduces the workspace engine.
const (
	parityGoMod = "module example.com/parity\n\ngo 1.26\n"

	parityLeaf = `package leaf

import (
	"fmt"
	"os"
)

// Logf is a printf wrapper; govet records a fact about it that
// dependent packages consume.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}

// Touch has a deliberately unchecked error return (errcheck).
func Touch() {
	os.Remove("leaf-scratch")
}

// unusedLeaf is a seeded unused violation.
func unusedLeaf() {}
`

	parityMid = `package mid

import (
	"time"

	"example.com/parity/leaf"
)

// Delay carries a seeded ineffassign violation (the overwritten first
// assignment) and a seeded staticcheck SA1004 violation (the 1ns
// sleep).
func Delay() int {
	x := 1
	x = 2
	time.Sleep(1)
	return x
}

// Report misuses leaf.Logf; govet's printf check only sees it through
// leaf's wrapper fact.
func Report() {
	leaf.Logf("%d")
}
`

	parityRoot = `package root

import (
	"fmt"
	"sync"

	"example.com/parity/mid"
)

// Describe carries a seeded unconvert violation (n is already an int).
func Describe() string {
	n := mid.Delay()
	m := int(n)
	return fmt.Sprintf("%d", m)
}

// Snapshot carries a seeded govet copylocks violation — a local,
// fact-free govet family member (the cross-package printf seed lives
// in mid). copylocks rather than a printf misuse of a std fmt
// function: staticcheck's SA5009 duplicates govet's printf at the
// same (file, line), and issues.uniq-by-line (always on) would keep
// whichever diagnostic each driver happened to order first — pure
// scheduling noise, not a parity signal.
func Snapshot() {
	var mu sync.Mutex
	mu2 := mu
	_ = mu2
}
`

	// parityGolangci enables the parity linter set. No explicit
	// staticcheck.checks selector: with the selector unset, the
	// shared exclusion filter drops golangci's default-disabled ST
	// checks (ST1000, ST1003, ...) identically in both drivers.
	parityGolangci = `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unconvert
    - unused
`
)

// parityFinding is the comparison tuple: file path relative to the
// module root, line, linter.
type parityFinding struct {
	file   string
	line   int
	linter string
}

func (f parityFinding) String() string {
	return fmt.Sprintf("%s:%d %s", f.file, f.line, f.linter)
}

// relTo relativizes an absolute diagnostic path against the module
// root; non-matching paths pass through for the diff output.
func relTo(root, name string) string {
	if rel, err := filepath.Rel(root, name); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return name
}

// sortedFindings renders a multiset as deterministic diff lines.
func sortedFindings(set map[parityFinding]int) []string {
	var lines []string
	for f, n := range set {
		for range n {
			lines = append(lines, f.String())
		}
	}
	sort.Strings(lines)
	return lines
}

// TestUnitParity_RunVsUnit is THE parity gate: `plaid-lint run ./...`
// over the fixture module and `plaid-lint unit` per package bottom-up
// (dependency facts threaded through .plaidfacts files) must produce
// the same finding set as (relative-file, line, linter) tuples.
func TestUnitParity_RunVsUnit(t *testing.T) {
	// Isolate the engine's L0/L1/L2 caches from the developer's real
	// cache dir; the unit driver has no caches by construction. The
	// auto backend would reroute to a shared gocacheprog daemon when
	// GOCACHEPROG is set, defeating the isolation — force local.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCACHEPROG", "")
	t.Setenv("PLAID_DISABLE_AUTO_CACHE_BACKEND", "1")
	// An earlier in-process `run` test may have auto-exported
	// PLAID_CACHE_BACKEND=gocacheprog (memlimit.ApplyCacheBackend uses
	// os.Setenv, which outlives that test); pin the backend so this
	// test is order-independent.
	t.Setenv("PLAID_CACHE_BACKEND", "local")
	// Match the fixture builder's module hygiene for the engine's own
	// packages.Load.
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOWORK", "off")

	dir, pkgs := buildUnitFixture(t, map[string]string{
		"go.mod":        parityGoMod,
		".golangci.yml": parityGolangci,
		"leaf/leaf.go":  parityLeaf,
		"mid/mid.go":    parityMid,
		"root/root.go":  parityRoot,
	})
	golangciPath := filepath.Join(dir, ".golangci.yml")

	// ---- run side: the workspace engine over the whole module.
	jsonOut := filepath.Join(t.TempDir(), "issues.json")
	code, stdout, stderr := runApp(t, dir, "run", "--output.json.path="+jsonOut, "./...")
	if code != exitIssuesFound {
		t.Fatalf("run exit=%d want %d (findings)\nstdout=%q\nstderr=%q", code, exitIssuesFound, stdout, stderr)
	}
	body, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("read run json output: %v", err)
	}
	var payload struct {
		Issues []struct {
			Linter string `json:"linter"`
			Pos    struct {
				Filename string `json:"filename"`
				Line     int    `json:"line"`
			} `json:"pos"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse run json output: %v", err)
	}
	runSet := map[parityFinding]int{}
	for _, is := range payload.Issues {
		runSet[parityFinding{
			file:   relTo(dir, is.Pos.Filename),
			line:   is.Pos.Line,
			linter: is.Linter,
		}]++
	}

	// The seeded violations must actually be in the run baseline —
	// otherwise the equality below could hold vacuously.
	seedPresent := func(file, linter string) bool {
		for f := range runSet {
			if f.file == file && f.linter == linter {
				return true
			}
		}
		return false
	}
	// Linter attribution follows the engine's convention: family
	// members report the analyzer's own name (SA1004, printf,
	// copylocks), not the umbrella (staticcheck, govet).
	for _, s := range []struct{ file, linter string }{
		{"leaf/leaf.go", "errcheck"},
		{"leaf/leaf.go", "unused"},
		{"mid/mid.go", "ineffassign"},
		{"mid/mid.go", "SA1004"},
		{"mid/mid.go", "printf"}, // the fact-flow seed: leaf.Logf("%d")
		{"root/root.go", "unconvert"},
		{"root/root.go", "copylocks"},
	} {
		if !seedPresent(s.file, s.linter) {
			t.Errorf("run baseline is missing the seeded %s violation in %s; baseline:\n%s",
				s.linter, s.file, strings.Join(sortedFindings(runSet), "\n"))
		}
	}

	// ---- unit side: one action per package, bottom-up, facts from
	// already-analyzed packages threaded into each dependent action.
	unitSet := map[parityFinding]int{}
	depFacts := map[string]string{}
	for _, pkgPath := range []string{
		"example.com/parity/leaf",
		"example.com/parity/mid",
		"example.com/parity/root",
	} {
		pkg := pkgs[pkgPath]
		if pkg == nil {
			t.Fatalf("fixture package %s missing: %v", pkgPath, pkgs)
		}
		cfgPath, sarifPath, factsPath := writeUnitCfg(t, pkg, golangciPath, depFacts)
		code, _, stderr := runApp(t, dir, "unit", "--cfg", cfgPath)
		if code != exitSuccess {
			t.Fatalf("unit %s exit=%d stderr=%q", pkgPath, code, stderr)
		}
		depFacts[pkgPath] = factsPath

		for _, r := range readSarifResults(t, sarifPath) {
			if len(r.Locations) != 1 {
				t.Fatalf("unit %s: result has %d locations: %+v", pkgPath, len(r.Locations), r)
			}
			loc := r.Locations[0].PhysicalLocation
			unitSet[parityFinding{
				file:   relTo(dir, loc.ArtifactLocation.URI),
				line:   loc.Region.StartLine,
				linter: r.RuleID,
			}]++
		}
	}

	// ---- the gate: the multisets must be equal.
	var onlyRun, onlyUnit []string
	for f, n := range runSet {
		if d := n - unitSet[f]; d > 0 {
			for range d {
				onlyRun = append(onlyRun, f.String())
			}
		}
	}
	for f, n := range unitSet {
		if d := n - runSet[f]; d > 0 {
			for range d {
				onlyUnit = append(onlyUnit, f.String())
			}
		}
	}
	sort.Strings(onlyRun)
	sort.Strings(onlyUnit)
	if len(onlyRun)+len(onlyUnit) > 0 {
		t.Errorf("parity gap between `run` and `unit`:\n"+
			"only in run (%d):\n  %s\nonly in unit (%d):\n  %s\n"+
			"full run set:\n  %s\nfull unit set:\n  %s",
			len(onlyRun), strings.Join(onlyRun, "\n  "),
			len(onlyUnit), strings.Join(onlyUnit, "\n  "),
			strings.Join(sortedFindings(runSet), "\n  "),
			strings.Join(sortedFindings(unitSet), "\n  "))
	}
}
