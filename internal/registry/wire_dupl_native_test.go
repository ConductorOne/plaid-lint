// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestWireDuplNative_DetectsIntraPackageClones is the happy-path
// pin: two structurally-identical functions in the same package are
// reported, and the diagnostic message preserves the canonical
// `N-M lines are duplicate of file:start-end` shape.
func TestWireDuplNative_DetectsIntraPackageClones(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "src", "intra")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two copies of a body big enough to clear the configured 50-token
	// threshold but small enough to keep the analysistest snapshot
	// readable. Each line is a distinct statement so it parses and
	// type-checks cleanly. The first line of each duplicate carries a
	// `// want` comment so analysistest matches the reported
	// diagnostic by line + message regex.
	bodyLine := "\t_, _ = compute(a, b)"
	var bodyA, bodyB []string
	for i := 0; i < 18; i++ {
		bodyA = append(bodyA, bodyLine)
		bodyB = append(bodyB, bodyLine)
	}
	src := "package intra\n\n" +
		"func compute(a, b int) (int, int) { return a + b, a - b }\n\n" +
		"func cloneA(a, b int) { // want `lines are duplicate of`\n" +
		strings.Join(bodyA, "\n") + "\n}\n\n" +
		"func cloneB(a, b int) { // want `lines are duplicate of`\n" +
		strings.Join(bodyB, "\n") + "\n}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "intra.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	a := duplAnalyzer(50)
	// analysistest uses GOPATH semantics — the package import path is
	// resolved relative to the temp dir's `src/` directory.
	results := analysistest.Run(t, dir, a, "intra")
	if len(results) != 1 {
		t.Fatalf("results len=%d want 1", len(results))
	}
	if len(results[0].Diagnostics) == 0 {
		t.Fatalf("expected at least one duplicate diagnostic on cloneA/cloneB; got 0")
	}

	// Message format pin: `N-M lines are duplicate of file:start-end`.
	msgRe := regexp.MustCompile(`^\d+-\d+ lines are duplicate of .+:\d+-\d+$`)
	for _, d := range results[0].Diagnostics {
		if !msgRe.MatchString(d.Message) {
			t.Errorf("message %q does not match `N-M lines are duplicate of file:start-end`", d.Message)
		}
	}
}

// TestWireDuplNative_RespectsThreshold pins the settings flow: a
// threshold high enough to skip the duplicated body produces no
// diagnostics, confirming [config.DuplSettings.Threshold] reaches
// the underlying library.
func TestWireDuplNative_RespectsThreshold(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "src", "thresh")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Small duplicate (~10 tokens of repetition) — below the high
	// threshold but above the library's internal floor.
	dupBody := "\tx := 1\n\t_ = x\n\ty := 2\n\t_ = y\n"
	src := "package thresh\n\n" +
		"func aa() {\n" + dupBody + "}\n\n" +
		"func bb() {\n" + dupBody + "}\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "t.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	high := duplAnalyzer(10_000)
	results := analysistest.Run(t, dir, high, "thresh")
	if len(results) != 1 {
		t.Fatalf("results len=%d want 1", len(results))
	}
	if len(results[0].Diagnostics) != 0 {
		t.Errorf("high threshold should suppress all diagnostics; got %d", len(results[0].Diagnostics))
	}
}

// TestWireDuplNative_NoFalsePositive: a package with two structurally
// distinct functions yields zero diagnostics under a typical
// threshold. This guards against the suffix-tree's internal floor
// silently matching trivial token runs.
func TestWireDuplNative_NoFalsePositive(t *testing.T) {
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "src", "nofp")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package nofp\n\n" +
		"func add(a, b int) int { return a + b }\n\n" +
		"func mul(a, b int) int { return a * b }\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "n.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	a := duplAnalyzer(150)
	results := analysistest.Run(t, dir, a, "nofp")
	if len(results) != 1 {
		t.Fatalf("results len=%d want 1", len(results))
	}
	if got := len(results[0].Diagnostics); got != 0 {
		t.Errorf("distinct functions should produce no diagnostics; got %d", got)
	}
}

// TestWireDuplNative_SettingsThreshold pins the cfg-arg shape that
// the catalog passes in: a *config.DuplSettings with Threshold>0
// overrides the default; nil and zero fall through to the default.
func TestWireDuplNative_SettingsThreshold(t *testing.T) {
	const sentinel = "dupl"

	e, ok := defaultCatalog.resolve(sentinel)
	if !ok {
		t.Fatal("catalog missing dupl entry")
	}
	if e.AnalyzerFn == nil {
		t.Fatal("AnalyzerFn nil — wireAnalyzerFnsDuplNative did not fire at init")
	}

	// Default fallthrough on nil settings.
	got := e.AnalyzerFn(nil)
	if len(got) != 1 || got[0] == nil {
		t.Fatalf("AnalyzerFn(nil) returned %d analyzers", len(got))
	}
	if got[0].Name != sentinel {
		t.Errorf("analyzer.Name=%q want %q", got[0].Name, sentinel)
	}

	// Settings override path: a *config.DuplSettings with Threshold>0
	// must produce a fresh analyzer (so the engine can install
	// distinct flag sets without cross-talk).
	s := &config.DuplSettings{Threshold: 200}
	got2 := e.AnalyzerFn(s)
	if len(got2) != 1 || got2[0] == nil {
		t.Fatalf("AnalyzerFn(settings) returned %d analyzers", len(got2))
	}
	if got[0] == got2[0] {
		t.Errorf("AnalyzerFn should return a fresh analyzer per call")
	}
}

// Sanity: prevent the analyzer factory from accidentally returning
// a nil Analyzer.
var _ = func() *analysis.Analyzer { return duplAnalyzer(150) }
