// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// TestHarness_ExcludeLeafPackages drives the bench_small fixture
// twice — once with no exclusion, once with the leaf packages
// excluded — and asserts:
//
//   - The excluded run skips 3 of the 6 workspace packages (3 leaves).
//   - cold.action_count drops accordingly.
//   - The harness reports the exclude patterns and excluded count
//     in BenchmarkResult.
//
// This is the Phase 1.7 sub-path (f) integration check called out in
// the dispatcher brief.
func TestHarness_ExcludeLeafPackages(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	baseCfg := func() Config {
		return Config{
			Fixture:           dir,
			FixtureShape:      SmallShape.Name,
			BudgetBytes:       512 * 1024 * 1024,
			MaxConcurrency:    2,
			ObservationSource: scheduler.SourceVmHWM,
			SkipWarm:          true,
			SkipCascade:       true,
		}
	}

	// Baseline: no exclusion.
	baseRes, err := Run(ctx, baseCfg())
	if err != nil {
		t.Fatalf("baseline Run: %v", err)
	}
	if baseRes.Cold == nil {
		t.Fatal("baseline cold scenario missing")
	}
	if baseRes.ExcludedPackageCount != 0 {
		t.Errorf("baseline ExcludedPackageCount = %d, want 0", baseRes.ExcludedPackageCount)
	}
	if baseRes.WorkspacePackageCount == 0 {
		t.Errorf("baseline WorkspacePackageCount = 0, want > 0")
	}
	baseAction := baseRes.Cold.ActionCount
	basePkgs := baseRes.WorkspacePackageCount

	// Excluded run: drop the leaf* packages.
	excluder, err := NewPackageExcluder([]string{"leaf*"}, nil)
	if err != nil {
		t.Fatalf("NewPackageExcluder: %v", err)
	}
	excCfg := baseCfg()
	excCfg.Excluder = excluder
	excRes, err := Run(ctx, excCfg)
	if err != nil {
		t.Fatalf("excluded Run: %v", err)
	}
	if excRes.Cold == nil {
		t.Fatal("excluded cold scenario missing")
	}

	// bench_small has 3 leaves + 2 mid + 1 root = 6 packages.
	// "leaf*" matches the 3 leaves.
	if want := 3; excRes.ExcludedPackageCount != want {
		t.Errorf("ExcludedPackageCount = %d, want %d (SmallShape has 3 leaves)",
			excRes.ExcludedPackageCount, want)
	}
	if excRes.WorkspacePackageCount != basePkgs {
		t.Errorf("WorkspacePackageCount = %d, want %d (same as baseline)",
			excRes.WorkspacePackageCount, basePkgs)
	}
	if excRes.Cold.ActionCount >= baseAction {
		t.Errorf("excluded action_count = %d, baseline = %d (want strict drop)",
			excRes.Cold.ActionCount, baseAction)
	}
	// ExcludePatterns reflects the supplied glob.
	if len(excRes.ExcludePatterns) == 0 {
		t.Errorf("ExcludePatterns empty, want at least one entry")
	}
	var found bool
	for _, p := range excRes.ExcludePatterns {
		if p == "leaf*" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ExcludePatterns = %v, want contains \"leaf*\"", excRes.ExcludePatterns)
	}
}

// TestHarness_ExcludeAllPackagesIsError verifies that excluding
// every workspace package surfaces a clear error rather than
// silently producing an empty run.
func TestHarness_ExcludeAllPackagesIsError(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// "*" matches every basename. Combined with the dir-pattern
	// handling, this drops every workspace package.
	excluder, err := NewPackageExcluder([]string{"*"}, nil)
	if err != nil {
		t.Fatalf("NewPackageExcluder: %v", err)
	}
	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		SkipWarm:          true,
		SkipCascade:       true,
		Excluder:          excluder,
	}
	_, err = Run(ctx, cfg)
	if err == nil {
		t.Fatal("expected error when every workspace package is excluded, got nil")
	}
}

// TestHarness_ExcludeSubdirPackages exercises the c1-shaped workspace:
// one go.mod at the fixture root, multiple workspace packages under
// distinct subdirectories. This is the shape LEARN-FGL-003 surfaced
// as broken in the original implementation, where ShouldExcludePackage
// consulted mp.LoadDir (the loader cwd, identical across every
// workspace package in a `go list ./...` load) instead of each
// package's source directory.
//
// The fixture has four packages with hand-picked basenames so neither
// the directory pattern nor the basename file pattern accidentally
// match the wrong target:
//
//   - generated/proto/proto.go
//   - generated/mocks/mocks.go
//   - lib/util/util.go
//   - main pkg at root (main.go)
//
// The test asserts:
//
//  1. --exclude-glob=generated/proto drops exactly that one package.
//  2. yamlPaths=["generated/"] (the .golangci.yml-style substring pattern)
//     drops both generated/* packages.
//  3. excluded_package_count is reported and non-zero.
//  4. cold.action_count strictly decreases vs the no-exclude baseline
//     on the same fixture.
func TestHarness_ExcludeSubdirPackages(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if err := writeMultiPackageFixture(dir); err != nil {
		t.Fatalf("writeMultiPackageFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	baseCfg := func() Config {
		return Config{
			Fixture:           dir,
			FixtureShape:      "multi_pkg",
			BudgetBytes:       512 * 1024 * 1024,
			MaxConcurrency:    2,
			ObservationSource: scheduler.SourceVmHWM,
			SkipWarm:          true,
			SkipCascade:       true,
		}
	}

	// Baseline: no exclusion. 4 workspace packages.
	baseRes, err := Run(ctx, baseCfg())
	if err != nil {
		t.Fatalf("baseline Run: %v", err)
	}
	if baseRes.Cold == nil {
		t.Fatal("baseline cold scenario missing")
	}
	if baseRes.ExcludedPackageCount != 0 {
		t.Errorf("baseline ExcludedPackageCount = %d, want 0", baseRes.ExcludedPackageCount)
	}
	if want := 4; baseRes.WorkspacePackageCount != want {
		t.Errorf("baseline WorkspacePackageCount = %d, want %d", baseRes.WorkspacePackageCount, want)
	}
	baseAction := baseRes.Cold.ActionCount
	basePkgs := baseRes.WorkspacePackageCount

	// (1) --exclude-glob=generated/proto drops exactly one package.
	t.Run("glob_subdir_path", func(t *testing.T) {
		excluder, err := NewPackageExcluder([]string{"generated/proto"}, nil)
		if err != nil {
			t.Fatalf("NewPackageExcluder: %v", err)
		}
		cfg := baseCfg()
		cfg.Excluder = excluder
		res, err := Run(ctx, cfg)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Cold == nil {
			t.Fatal("cold scenario missing")
		}
		if want := 1; res.ExcludedPackageCount != want {
			t.Errorf("ExcludedPackageCount = %d, want %d", res.ExcludedPackageCount, want)
		}
		if res.WorkspacePackageCount != basePkgs {
			t.Errorf("WorkspacePackageCount = %d, want %d (unchanged from baseline)",
				res.WorkspacePackageCount, basePkgs)
		}
		if res.Cold.ActionCount >= baseAction {
			t.Errorf("excluded action_count = %d, baseline = %d (want strict drop)",
				res.Cold.ActionCount, baseAction)
		}
	})

	// (2) yamlPaths=["generated/"] (substring + regex) drops both
	// generated/* packages. This is the path .golangci.yml's
	// linters.exclusions.paths feeds into.
	t.Run("yaml_dir_prefix", func(t *testing.T) {
		excluder, err := NewPackageExcluder(nil, []string{"generated/"})
		if err != nil {
			t.Fatalf("NewPackageExcluder: %v", err)
		}
		cfg := baseCfg()
		cfg.Excluder = excluder
		res, err := Run(ctx, cfg)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Cold == nil {
			t.Fatal("cold scenario missing")
		}
		if want := 2; res.ExcludedPackageCount != want {
			t.Errorf("ExcludedPackageCount = %d, want %d", res.ExcludedPackageCount, want)
		}
		if res.WorkspacePackageCount != basePkgs {
			t.Errorf("WorkspacePackageCount = %d, want %d (unchanged from baseline)",
				res.WorkspacePackageCount, basePkgs)
		}
		if res.Cold.ActionCount >= baseAction {
			t.Errorf("excluded action_count = %d, baseline = %d (want strict drop)",
				res.Cold.ActionCount, baseAction)
		}
		if len(res.ExcludePatterns) == 0 {
			t.Errorf("ExcludePatterns empty, want at least one entry")
		}
	})
}

// writeMultiPackageFixture materialises a small workspace with one
// go.mod at the root and four packages laid out in subdirectories.
// File basenames are intentionally chosen so they cannot match
// directory-style patterns like "generated/proto" or "generated/" via
// the basename file-pattern fallback — only the source-directory
// check can catch them.
func writeMultiPackageFixture(root string) error {
	files := map[string]string{
		"go.mod": "module example.com/multi\n\ngo 1.22\n",
		// Generated proto-shaped package.
		"generated/proto/proto.go": "package proto\n\nfunc Marshal() string { return \"x\" }\n",
		// Generated mocks-shaped package.
		"generated/mocks/mocks.go": "package mocks\n\nfunc Mock() string { return \"m\" }\n",
		// Handwritten util package.
		"lib/util/util.go": "package util\n\nfunc Util() string { return \"u\" }\n",
		// Root main package, importing both groups so the analyzer
		// graph has cross-package work.
		"main.go": `package main

import (
	gp "example.com/multi/generated/proto"
	gm "example.com/multi/generated/mocks"
	"example.com/multi/lib/util"
)

func main() {
	_ = gp.Marshal()
	_ = gm.Mock()
	_ = util.Util()
}
`,
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}
