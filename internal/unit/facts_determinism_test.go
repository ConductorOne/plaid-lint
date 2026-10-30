// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// multiFactDep exports facts of several distinct types when analyzed
// under govet+staticcheck:
//
//   - Logf     → printf.isWrapper (govet/printf)
//   - OldLogf  → deprecated.IsDeprecated (staticcheck facts/deprecated)
//   - Add      → purity.IsPure (staticcheck facts/purity)
//
// plus whatever typedness/nilness facts the honnef closure records
// for these functions. That makes the dep's .plaidfacts a mixed-type
// fact set — the shape the byte-determinism guarantee has to hold
// for, not just the single-printf-fact fixture of TestUnit_Determinism.
const (
	multiFactDep = `package dep

import "fmt"

// Logf is a printf wrapper; the printf analyzer records a wrapper
// fact about it.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}

// OldLogf is kept for callers that predate Logf.
//
// Deprecated: use Logf instead.
func OldLogf(format string, args ...any) {
	Logf(format, args...)
}

// Add is a pure function; staticcheck's purity pass records a fact
// about it that SA4017 consumes downstream.
func Add(a, b int) int {
	return a + b
}
`

	multiFactRoot = `package root

import "example.com/fix/dep"

func use() {
	dep.Logf("%d")       // printf: missing arg — needs dep's wrapper fact
	dep.OldLogf("hello") // SA1019 — needs dep's deprecation fact
	dep.Add(1, 2)        // SA4017 — needs dep's purity fact
}
`
)

// multiFactConfig enables govet (printf facts) and the staticcheck
// family (deprecation, purity, typedness, nilness facts).
func multiFactConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, warnings, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - govet
    - staticcheck
`), ".yml")
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	for _, w := range warnings {
		t.Logf("config warning: %s: %s", w.Field, w.Message)
	}
	return cfg
}

// TestFactsDeterminism_MultiFactDep strengthens TestUnit_Determinism
// beyond the printf-only fixture: a dependency exporting MANY facts
// of different types must produce byte-identical .plaidfacts on
// every run (5×, fresh output dirs), and a root package consuming
// those facts must produce identical diagnostics on every run (3×).
// Byte-stable facts are what make unit actions cacheable by content
// hash in a distributed build.
func TestFactsDeterminism_MultiFactDep(t *testing.T) {
	pkgs := buildFixture(t, map[string]string{
		"go.mod":       fixtureGoMod,
		"dep/dep.go":   multiFactDep,
		"root/root.go": multiFactRoot,
	})
	dep := pkgs["example.com/fix/dep"]
	root := pkgs["example.com/fix/root"]
	if dep == nil || root == nil {
		t.Fatalf("fixture packages missing: %v", pkgs)
	}

	// Phase 1: the dep's facts_only action, 5×. runUnitOn allocates a
	// fresh output dir per call; each run also rebuilds the registry
	// and filter from a fresh config so no state can leak between
	// runs except through the output files themselves.
	var factsBlobs [5][]byte
	var factsPaths [5]string
	for i := range factsBlobs {
		_, _, factsPath := runUnitOn(t, dep, ModeFactsOnly, nil, multiFactConfig(t))
		blob, err := os.ReadFile(factsPath)
		if err != nil {
			t.Fatalf("run %d: dep facts missing: %v", i, err)
		}
		factsBlobs[i] = blob
		factsPaths[i] = factsPath
	}
	payload, err := unwrapFacts(factsBlobs[0])
	if err != nil {
		t.Fatalf("dep facts container: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("dep exported no facts; fixture must produce printf+deprecated+purity facts")
	}
	for i := 1; i < len(factsBlobs); i++ {
		if !bytes.Equal(factsBlobs[0], factsBlobs[i]) {
			t.Errorf(".plaidfacts differ between identical runs 0 and %d (%s vs %s): %d vs %d bytes",
				i, factsPaths[0], factsPaths[i], len(factsBlobs[0]), len(factsBlobs[i]))
		}
	}

	// Phase 2: the root consuming run 0's facts, 3×. Every fact type
	// must actually flow — each expected finding is reachable only
	// through a different fact type — and the decoded diagnostics
	// must be identical across runs.
	depFacts := map[string]string{"example.com/fix/dep": factsPaths[0]}
	var diagJSON [3][]byte
	for i := range diagJSON {
		res, _, _ := runUnitOn(t, root, ModeFull, depFacts, multiFactConfig(t))
		if i == 0 {
			for _, want := range []struct{ linter, substr string }{
				{"printf", "Logf"},    // printf via isWrapper fact
				{"SA1019", "OldLogf"}, // SA1019 via IsDeprecated fact
				{"SA4017", "Add"},     // SA4017 via IsPure fact
			} {
				if !hasFinding(res, want.linter, want.substr) {
					t.Errorf("missing %s finding mentioning %q — its fact type did not flow; got %+v",
						want.linter, want.substr, res.Diagnostics)
				}
			}
		}
		blob, err := json.Marshal(res.Diagnostics)
		if err != nil {
			t.Fatalf("run %d: marshal diagnostics: %v", i, err)
		}
		diagJSON[i] = blob
	}
	for i := 1; i < len(diagJSON); i++ {
		if !bytes.Equal(diagJSON[0], diagJSON[i]) {
			t.Errorf("root diagnostics differ between identical runs 0 and %d:\nrun 0: %s\nrun %d: %s",
				i, diagJSON[0], i, diagJSON[i])
		}
	}
}

// enumDep declares an enum, so exhaustive exports an enumMembersFact
// for Color. That fact holds three maps; with enough members their
// gob iteration order differs between runs unless the fact set
// serializes them canonically. Crimson aliases Red's value so the
// value→names map has a multi-name entry.
const (
	enumDep = `package dep

type Color int

const (
	Red Color = iota
	Orange
	Yellow
	Green
	Blue
	Indigo
	Violet
	Black
	White
	Grey
	Cyan
	Magenta
	Crimson = Red
)
`

	enumRoot = `package root

import "example.com/fix/dep"

func name(c dep.Color) string {
	switch c {
	case dep.Red, dep.Orange, dep.Yellow, dep.Green, dep.Blue, dep.Indigo,
		dep.Violet, dep.Black, dep.White, dep.Grey, dep.Cyan:
		return "known"
	}
	return "?"
}
`
)

func exhaustiveConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, _, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - exhaustive
`), ".yml")
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

// TestFactsDeterminism_EnumDep pins the map-bearing exhaustive fact:
// the enum package's .plaidfacts must be byte-identical across runs,
// and the root's exhaustive check must still see the decoded fact
// (the missing-case finding is only reachable through it).
func TestFactsDeterminism_EnumDep(t *testing.T) {
	pkgs := buildFixture(t, map[string]string{
		"go.mod":       fixtureGoMod,
		"dep/dep.go":   enumDep,
		"root/root.go": enumRoot,
	})
	dep := pkgs["example.com/fix/dep"]
	root := pkgs["example.com/fix/root"]
	if dep == nil || root == nil {
		t.Fatalf("fixture packages missing: %v", pkgs)
	}

	const runs = 8
	var factsBlobs [runs][]byte
	var factsPaths [runs]string
	for i := range factsBlobs {
		_, _, factsPath := runUnitOn(t, dep, ModeFactsOnly, nil, exhaustiveConfig(t))
		blob, err := os.ReadFile(factsPath)
		if err != nil {
			t.Fatalf("run %d: dep facts missing: %v", i, err)
		}
		factsBlobs[i] = blob
		factsPaths[i] = factsPath
	}
	payload, err := unwrapFacts(factsBlobs[0])
	if err != nil {
		t.Fatalf("dep facts container: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("dep exported no facts; fixture must produce an exhaustive enum fact")
	}
	for i := 1; i < runs; i++ {
		if !bytes.Equal(factsBlobs[0], factsBlobs[i]) {
			t.Errorf(".plaidfacts differ between identical runs 0 and %d (%s vs %s): %d vs %d bytes",
				i, factsPaths[0], factsPaths[i], len(factsBlobs[0]), len(factsBlobs[i]))
		}
	}

	// Every run's facts, decoded, must drive the same finding.
	var want []byte
	for i, path := range factsPaths {
		res, _, _ := runUnitOn(t, root, ModeFull, map[string]string{"example.com/fix/dep": path}, exhaustiveConfig(t))
		if !hasFinding(res, "exhaustive", "dep.Magenta") {
			t.Fatalf("run %d: missing exhaustive finding for dep.Magenta — enum fact did not round-trip; got %+v",
				i, res.Diagnostics)
		}
		if hasFinding(res, "exhaustive", "dep.Crimson") {
			t.Errorf("run %d: Crimson shares Red's value and must not be reported missing; got %+v", i, res.Diagnostics)
		}
		blob, err := json.Marshal(res.Diagnostics)
		if err != nil {
			t.Fatalf("run %d: marshal diagnostics: %v", i, err)
		}
		if i == 0 {
			want = blob
		} else if !bytes.Equal(want, blob) {
			t.Errorf("root diagnostics differ between runs 0 and %d:\nrun 0: %s\nrun %d: %s", i, want, i, blob)
		}
	}
}
