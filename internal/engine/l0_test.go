// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// l0Fixture builds a small, self-contained module with one package and
// enough content to exercise at least one default analyzer. The fixture
// is reused by every L0 test so the dispatch numbers (loadtime, # of
// diagnostics) stay predictable.
func l0Fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module l0test\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

// Hello returns a greeting.
func Hello(name string) string {
	x := 1
	x = 2
	_ = x
	return "hello " + name
}

func main() {
	_ = Hello("world")
}
`), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

func l0RunInput(t *testing.T, fixture string) (RunInput, *l0.Cache) {
	t.Helper()
	l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
	if err != nil {
		t.Fatalf("open L1: %v", err)
	}
	l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
	if err != nil {
		t.Fatalf("open L2: %v", err)
	}
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	cfg := config.NewDefault()
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	return RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: fixture},
		L1:        l1,
		L2:        l2,
		L0:        l0c,
	}, l0c
}

// recordingHook captures the set of PackageIDs that reached the
// analyzer driver. It uses a synthetic stub Analyze so the test runs
// in milliseconds — the real driver is exercised by the W6 variant
// test, not by these unit-level L0 tests.
type recordingHook struct {
	mu     atomic.Pointer[hookState]
	failOn map[metadata.PackageID]bool
}

type hookState struct {
	calls    int
	pkgs     map[metadata.PackageID]bool
	allPkgs  map[metadata.PackageID]*metadata.Package
	produced map[metadata.PackageID][]*cache.Diagnostic
}

func newRecordingHook() *recordingHook {
	rh := &recordingHook{}
	rh.mu.Store(&hookState{
		pkgs:     map[metadata.PackageID]bool{},
		allPkgs:  map[metadata.PackageID]*metadata.Package{},
		produced: map[metadata.PackageID][]*cache.Diagnostic{},
	})
	return rh
}

func (rh *recordingHook) fn() analyzeFn {
	return func(ctx context.Context, snap *cache.Snapshot, pkgs map[metadata.PackageID]*metadata.Package) ([]*cache.Diagnostic, error) {
		st := rh.mu.Load()
		st.calls++
		for id, mp := range pkgs {
			st.pkgs[id] = true
			st.allPkgs[id] = mp
			if rh.failOn[id] {
				return nil, fmt.Errorf("forced failure for %s", id)
			}
		}
		// No fresh diagnostics — the hook is a stub.
		return nil, nil
	}
}

func (rh *recordingHook) analyzedPkgs() []metadata.PackageID {
	st := rh.mu.Load()
	out := make([]metadata.PackageID, 0, len(st.pkgs))
	for id := range st.pkgs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// TestL0_MissCallsAnalyze: with an empty L0, every package should
// reach the analyzer driver, and after Run completes the L0 should
// have stored an entry per analyzed package.
func TestL0_MissCallsAnalyze(t *testing.T) {
	dir := l0Fixture(t)
	in, l0c := l0RunInput(t, dir)
	hook := newRecordingHook()
	SetAnalyzeHookForTest(&in, hook.fn())

	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.WorkspacePackageCount == 0 {
		t.Fatal("no workspace packages discovered")
	}

	analyzed := hook.analyzedPkgs()
	if len(analyzed) != res.Stats.WorkspacePackageCount {
		t.Errorf("analyzed pkgs = %d, want = WorkspacePackageCount = %d (analyzed=%v)",
			len(analyzed), res.Stats.WorkspacePackageCount, analyzed)
	}
	m := l0c.MetricsPtr().Snapshot()
	if m.Hits != 0 {
		t.Errorf("L0 hits on cold = %d, want 0", m.Hits)
	}
	if m.Stores == 0 {
		t.Errorf("L0 stores on cold = 0, want > 0 (analyzed pkgs should have produced L0 entries)")
	}
	if m.Stores != int64(len(analyzed)) {
		t.Errorf("L0 stores = %d, want = analyzed = %d", m.Stores, len(analyzed))
	}
}

// TestL0_HitSkipsAnalyze: pre-populate L0 by running once, then run
// again with the same L0 — the second run must skip the analyzer
// driver entirely for cached packages.
func TestL0_HitSkipsAnalyze(t *testing.T) {
	dir := l0Fixture(t)
	in, l0c := l0RunInput(t, dir)

	// Cold run (no hook): exercises the real Analyze and populates L0.
	if _, err := Run(context.Background(), in); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	storesAfterCold := l0c.MetricsPtr().Snapshot().Stores
	if storesAfterCold == 0 {
		t.Fatal("cold run did not populate L0")
	}

	// Warm run: install a hook that fails if the driver is invoked at
	// all. Every package should hit L0.
	warmIn := in
	warmIn.L1, _ = clcache.Open(filepath.Join(t.TempDir(), "l1-warm")) // fresh L1 so we don't get false hits
	warmIn.L2, _ = clcache.Open(filepath.Join(t.TempDir(), "l2-warm"))

	hook := newRecordingHook()
	SetAnalyzeHookForTest(&warmIn, hook.fn())

	if _, err := Run(context.Background(), warmIn); err != nil {
		t.Fatalf("warm Run: %v", err)
	}

	hookCalls := hook.mu.Load().calls
	analyzed := hook.analyzedPkgs()
	if hookCalls != 0 && len(analyzed) > 0 {
		t.Errorf("warm Run reached analyzer for pkgs=%v (calls=%d) — L0 hit did not skip Analyze",
			analyzed, hookCalls)
	}
	m := l0c.MetricsPtr().Snapshot()
	if m.Hits == 0 {
		t.Errorf("warm run had zero L0 hits; expected > 0")
	}
}

// TestL0_SourceChangeInvalidates: modify a source file between runs
// and the L0 key must change, so the warm run must re-analyze the
// changed package.
func TestL0_SourceChangeInvalidates(t *testing.T) {
	dir := l0Fixture(t)
	in, l0c := l0RunInput(t, dir)

	// Cold run.
	if _, err := Run(context.Background(), in); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	hitsBefore := l0c.MetricsPtr().Snapshot().Hits

	// Edit the source: change the body of Hello.
	mainGo := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainGo, []byte(`package main

func Hello(name string) string {
	return "different " + name
}

func main() {
	_ = Hello("world")
}
`), 0o600); err != nil {
		t.Fatalf("rewrite main.go: %v", err)
	}

	// Warm run with the hook installed; the package must be re-analyzed.
	warmIn := in
	hook := newRecordingHook()
	SetAnalyzeHookForTest(&warmIn, hook.fn())
	if _, err := Run(context.Background(), warmIn); err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	if len(hook.analyzedPkgs()) == 0 {
		t.Errorf("source edit did not invalidate L0: no packages re-analyzed")
	}
	hitsAfter := l0c.MetricsPtr().Snapshot().Hits
	// We may have 0 net new hits (only one pkg in the workspace) or
	// some hits if there were sub-packages. The key signal is that
	// the analyzer driver WAS called for the changed package.
	t.Logf("L0 hits cold→edit: before=%d after=%d", hitsBefore, hitsAfter)
}

// TestL0_AnalyzerSetChangeInvalidates: changing the enabled linter
// set must change L0 keys.
func TestL0_AnalyzerSetChangeInvalidates(t *testing.T) {
	dir := l0Fixture(t)
	inA, _ := l0RunInput(t, dir)
	inB, _ := l0RunInput(t, dir)
	// Change inB's analyzer set.
	inB.Config.Linters.Default = config.GroupNone
	regB, _, err := registry.Build(inB.Config)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	inB.Registry = regB

	planA := planFromRegistry(inA.Registry)
	planB := planFromRegistry(inB.Registry)

	// Both inputs must drive at least one in-process analyzer for
	// the comparison to be meaningful. inB (Linters.Default=none)
	// should have zero analyzers — that itself is a different set,
	// so the analyzerSetHash must differ.
	hashA := computeAnalyzerSetHash(planA, inA.AnalyzerRegistry)
	hashB := computeAnalyzerSetHash(planB, inB.AnalyzerRegistry)
	if hashA == hashB {
		t.Errorf("analyzer-set hashes match across different sets (A=%x B=%x); A has %d analyzers, B has %d",
			hashA, hashB, len(planA.analyzers), len(planB.analyzers))
	}
}

// TestL0_CacheVersionBumpInvalidates: changing the L0 cache-version
// (simulated via ToolVersion since the const is compile-time) must
// invalidate prior entries.
func TestL0_CacheVersionBumpInvalidates(t *testing.T) {
	dir := l0Fixture(t)
	in, l0c := l0RunInput(t, dir)

	// Cold run with default tool version.
	if _, err := Run(context.Background(), in); err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	storesBefore := l0c.MetricsPtr().Snapshot().Stores

	// Bump tool version (a stand-in for L0 cache-version bump — both
	// fold into the key identically).
	warmIn := in
	warmIn.CacheToolVersion = "bumped-version-v2"

	hook := newRecordingHook()
	SetAnalyzeHookForTest(&warmIn, hook.fn())
	if _, err := Run(context.Background(), warmIn); err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	if len(hook.analyzedPkgs()) == 0 {
		t.Error("tool-version bump did not invalidate L0: no packages re-analyzed")
	}
	storesAfter := l0c.MetricsPtr().Snapshot().Stores
	if storesAfter <= storesBefore {
		t.Errorf("L0 stores did not increase after version bump: before=%d after=%d",
			storesBefore, storesAfter)
	}
}

// TestL0_DigestEquivalence: cold+warm runs must produce the same
// diagnostic stream (canonicalised). This is the W6 contract
// extended to cover L0.
func TestL0_DigestEquivalence(t *testing.T) {
	dir := l0Fixture(t)
	in, _ := l0RunInput(t, dir)

	res1, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	res2, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	d1 := canonicalForCompare(res1.Diagnostics)
	d2 := canonicalForCompare(res2.Diagnostics)
	if d1 != d2 {
		t.Errorf("cold/warm diagnostic stream diverged:\ncold=%s\nwarm=%s", d1, d2)
	}
}

// TestL0_PartialFailureDoesNotCache: when Analyze returns an error,
// no L0 entries should be written for that package.
func TestL0_PartialFailureDoesNotCache(t *testing.T) {
	dir := l0Fixture(t)
	in, l0c := l0RunInput(t, dir)

	// Install a hook that fails for every package it sees.
	hook := newRecordingHook()
	hook.failOn = map[metadata.PackageID]bool{} // populated below

	// First call: figure out the package IDs by running once with a
	// passthrough hook, then re-run with failOn populated.
	probe := newRecordingHook()
	SetAnalyzeHookForTest(&in, probe.fn())
	if _, err := Run(context.Background(), in); err != nil {
		t.Fatalf("probe Run: %v", err)
	}
	pkgIDs := probe.analyzedPkgs()
	if len(pkgIDs) == 0 {
		t.Fatal("probe found no packages")
	}
	// Reset L0 + caches; install the failing hook.
	for _, id := range pkgIDs {
		hook.failOn[id] = true
	}
	probeStores := l0c.MetricsPtr().Snapshot().Stores
	// Stores happened on probe run (no failure). Reopen L0 for clean baseline.
	freshL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("re-open L0: %v", err)
	}
	in2, _ := l0RunInput(t, dir)
	in2.L0 = freshL0
	in2.AnalyzerRegistry = in.AnalyzerRegistry
	SetAnalyzeHookForTest(&in2, hook.fn())

	_, err = Run(context.Background(), in2)
	if err == nil {
		t.Fatal("expected Run to fail due to analyzer hook error")
	}
	m := freshL0.MetricsPtr().Snapshot()
	if m.Stores != 0 {
		t.Errorf("L0 stores after failed run = %d, want 0 (partial failure must not cache)", m.Stores)
	}
	// Sanity: probe did populate L0 on the success path.
	if probeStores == 0 {
		t.Error("probe run did not populate L0 (sanity check)")
	}
}

// canonicalForCompare reduces a diagnostic slice to a deterministic
// string suitable for byte-identity comparison.
func canonicalForCompare(diags []output.Diagnostic) string {
	dd := make([]output.Diagnostic, len(diags))
	copy(dd, diags)
	output.Sort(dd)
	var b []byte
	for _, d := range dd {
		b = append(b, []byte(d.Linter)...)
		b = append(b, '\x00')
		b = append(b, []byte(d.Message)...)
		b = append(b, '\x00')
		b = append(b, []byte(d.Pos.Filename)...)
		b = append(b, '\x00')
		b = append(b, []byte(fmt.Sprintf("%d:%d", d.Pos.Line, d.Pos.Column))...)
		b = append(b, '\n')
	}
	return string(b)
}

// Compile-time use of unused imports.
var _ = errors.New
