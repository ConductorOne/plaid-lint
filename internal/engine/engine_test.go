// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestRun_RejectsMissingInputs surfaces a clear error for each of
// the required RunInput fields. Catches the common "engine called
// with a partly-built input" footgun before it lands in a real
// Run.
func TestRun_RejectsMissingInputs(t *testing.T) {
	good := mustInput(t)
	cases := []struct {
		name string
		in   RunInput
		want string
	}{
		{"no config", withConfig(good, nil), "Config"},
		{"no registry", withRegistry(good, nil), "Registry"},
		{"no module root", withModuleRoot(good, ""), "ModuleRoot"},
		{"no L1", withL1(good, nil), "L1"},
		{"no L2", withL2(good, nil), "L2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(context.Background(), tc.in)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestRun_EmptyAnalyzerSetReturnsZeroDiagnostics drives the engine
// against a workspace with `linters.default: none`, which produces
// zero in-process analyzers. The Run should still succeed (no
// InitializeWorkspace, no Analyze) and surface no diagnostics.
func TestRun_EmptyAnalyzerSetReturnsZeroDiagnostics(t *testing.T) {
	in := mustInput(t)
	in.Config.Linters.Default = config.GroupNone
	reg, _, err := registry.Build(in.Config)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	in.Registry = reg

	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	if len(res.Diagnostics) != 0 {
		t.Errorf("expected zero diagnostics, got %d", len(res.Diagnostics))
	}
	if res.Stats.AnalyzerCount != 0 {
		t.Errorf("expected zero analyzers wired, got %d", res.Stats.AnalyzerCount)
	}
}

// TestRun_SubprocFailureCancelsPeers asserts that a Runner error
// surfaces from Run and cancels sibling Runners via the
// errgroup-shared context. Uses two stub Runners: one returns an
// error immediately, the other blocks on ctx.Done.
func TestRun_SubprocFailureCancelsPeers(t *testing.T) {
	var peerCancelled atomic.Bool
	failing := &stubRunner{name: "failer", err: errors.New("boom")}
	peer := &stubRunner{
		name: "peer",
		blockUntilCancel: func(ctx context.Context) {
			<-ctx.Done()
			peerCancelled.Store(true)
		},
	}

	diags, err := runSubproc(context.Background(), mustInput(t), []subproc.Runner{failing, peer})
	if err == nil {
		t.Fatalf("expected error from failing runner, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected error to mention boom, got %v", err)
	}
	if diags != nil {
		t.Errorf("expected nil diagnostics on error, got %v", diags)
	}
	if !peerCancelled.Load() {
		t.Errorf("peer was not cancelled when sibling failed")
	}
}

// TestRun_SubprocMergesResultsDeterministically asserts that two
// Runners returning disjoint diagnostic sets are concatenated in
// the merged output regardless of execution order.
func TestRun_SubprocMergesResultsDeterministically(t *testing.T) {
	a := &stubRunner{name: "a", diags: []output.Diagnostic{{Linter: "a", Message: "alpha"}}}
	b := &stubRunner{name: "b", diags: []output.Diagnostic{{Linter: "b", Message: "beta"}}}

	diags, err := runSubproc(context.Background(), mustInput(t), []subproc.Runner{a, b})
	if err != nil {
		t.Fatalf("runSubproc: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("expected 2 merged diagnostics, got %d: %+v", len(diags), diags)
	}
	seen := map[string]bool{}
	for _, d := range diags {
		seen[d.Linter] = true
	}
	for _, want := range []string{"a", "b"} {
		if !seen[want] {
			t.Errorf("merged diags missing linter %q: %+v", want, diags)
		}
	}
}

// stubRunner is a hand-rolled subproc.Runner for engine-level tests.
type stubRunner struct {
	name             string
	diags            []output.Diagnostic
	err              error
	blockUntilCancel func(ctx context.Context)
}

func (s *stubRunner) Name() string { return s.name }
func (s *stubRunner) Run(ctx context.Context, _ *config.Config, _ subproc.WorkspaceRef) ([]output.Diagnostic, error) {
	if s.blockUntilCancel != nil {
		s.blockUntilCancel(ctx)
		return nil, ctx.Err()
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.diags, nil
}

// mustInput builds a baseline RunInput backed by a temp module so
// engine-level tests can mutate exactly one field per case.
func mustInput(t *testing.T) RunInput {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module engine-test\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
	if err != nil {
		t.Fatalf("open L1: %v", err)
	}
	l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
	if err != nil {
		t.Fatalf("open L2: %v", err)
	}
	cfg := config.NewDefault()
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	return RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: dir},
		L1:        l1,
		L2:        l2,
	}
}

func withConfig(in RunInput, c *config.Config) RunInput       { in.Config = c; return in }
func withRegistry(in RunInput, r *registry.Registry) RunInput { in.Registry = r; return in }
func withModuleRoot(in RunInput, p string) RunInput {
	in.Workspace.ModuleRoot = p
	return in
}
func withL1(in RunInput, c *clcache.Cache) RunInput { in.L1 = c; return in }
func withL2(in RunInput, c *clcache.Cache) RunInput { in.L2 = c; return in }

// TestRun_TargetPatternsNarrowsWorkspaceLoad asserts that a non-empty
// RunInput.TargetPatterns narrows the metadata load and therefore the
// analysis surface. The fixture has two packages (a, b); when the
// engine runs against `./a/...` only, WorkspacePackageCount must drop
// to a single workspace package, vs the all-loaded baseline. This is
// the production fix for the target-narrowing bug — the regression test
// here is what stops a future refactor from quietly reverting to a
// view-wide load on every Run.
func TestRun_TargetPatternsNarrowsWorkspaceLoad(t *testing.T) {
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
	mustWrite("go.mod", "module narrow-test\n\ngo 1.21\n")
	mustWrite("a/a.go", "package a\n\nfunc F() int { return 0 }\n")
	mustWrite("b/b.go", "package b\n\nfunc G() int { return 0 }\n")

	build := func(patterns []string) RunInput {
		l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
		if err != nil {
			t.Fatalf("open L1: %v", err)
		}
		l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
		if err != nil {
			t.Fatalf("open L2: %v", err)
		}
		cfg := config.NewDefault()
		reg, _, err := registry.Build(cfg)
		if err != nil {
			t.Fatalf("registry.Build: %v", err)
		}
		return RunInput{
			Config:         cfg,
			Registry:       reg,
			Workspace:      subproc.WorkspaceRef{ModuleRoot: dir},
			L1:             l1,
			L2:             l2,
			TargetPatterns: patterns,
		}
	}

	// Baseline: no patterns → both packages loaded as workspace
	// packages (the historical viewLoadScope behavior).
	baseRes, err := Run(context.Background(), build(nil))
	if err != nil {
		t.Fatalf("baseline engine.Run: %v", err)
	}
	if baseRes.Stats.WorkspacePackageCount < 2 {
		t.Fatalf("baseline WorkspacePackageCount = %d, want >= 2 (a, b)",
			baseRes.Stats.WorkspacePackageCount)
	}

	// Narrowed: only ./a/... → exactly one workspace package.
	narrowRes, err := Run(context.Background(), build([]string{"./a/..."}))
	if err != nil {
		t.Fatalf("narrow engine.Run: %v", err)
	}
	if narrowRes.Stats.WorkspacePackageCount != 1 {
		t.Errorf("narrow WorkspacePackageCount = %d, want 1 (just a)",
			narrowRes.Stats.WorkspacePackageCount)
	}
	if narrowRes.Stats.WorkspacePackageCount >= baseRes.Stats.WorkspacePackageCount {
		t.Errorf("narrowing did not reduce workspace surface: narrow=%d base=%d",
			narrowRes.Stats.WorkspacePackageCount,
			baseRes.Stats.WorkspacePackageCount)
	}
}
