// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// TestUnit_BrokenDeclaredInputsFailTheAction pins the infrastructure/
// finding split: an importcfg that NAMES an export-data file which is
// missing or unreadable is a broken action input and must fail the
// run (an error, exit 3 at the CLI) — never exit 0 with a `typecheck`
// finding and an empty fact set, which would silently lose downstream
// findings in facts_only chains. A source import that the importcfg
// simply does not name remains a typecheck finding: that is what
// broken SOURCE legitimately looks like.
func TestUnit_BrokenDeclaredInputsFailTheAction(t *testing.T) {
	golangci := testConfig(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "a.go")
	if err := os.WriteFile(src, []byte("package a\n\nimport \"fmt\"\n\nfunc F() { fmt.Println() }\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	mkcfg := func(importcfgBody string) *Config {
		icfg := filepath.Join(dir, "importcfg")
		if err := os.WriteFile(icfg, []byte(importcfgBody), 0o666); err != nil {
			t.Fatal(err)
		}
		return &Config{
			Schema: SchemaVersion,
			Package: PackageConfig{
				Path:    "example.com/a",
				GoFiles: []string{src},
				GOOS:    "linux",
				GOARCH:  "arm64",
			},
			Deps:     DepsConfig{Importcfg: icfg},
			Analysis: AnalysisConfig{Mode: ModeFactsOnly},
			Out: OutConfig{
				Sarif: filepath.Join(t.TempDir(), "out.sarif"),
				Facts: filepath.Join(t.TempDir(), "out.plaidfacts"),
			},
		}
	}

	reg, _, err := registry.BuildFromConfig(golangci)
	if err != nil {
		t.Fatal(err)
	}
	filter, err := exclusion.NewFilter(golangci, dir, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Case 1: importcfg names a nonexistent export file — infra error.
	_, err = Run(context.Background(), mkcfg("packagefile fmt=/nonexistent/fmt.a\n"), golangci, reg, filter)
	if err == nil {
		t.Fatal("missing declared export data did not fail the action")
	}
	if !strings.Contains(err.Error(), "broken action inputs") {
		t.Errorf("unexpected error shape: %v", err)
	}

	// Case 2: importcfg does not name fmt at all — a typecheck
	// finding-shaped situation, NOT an infra error (facts_only:
	// success with a warning).
	res, err := Run(context.Background(), mkcfg("# empty\n"), golangci, reg, filter)
	if err != nil {
		t.Fatalf("undeclared import treated as infra error: %v", err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "does not compile") {
		t.Errorf("expected does-not-compile warning in facts_only mode; got %q", joined)
	}
}
