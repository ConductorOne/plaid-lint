// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// streamingFixture builds a tiny module whose diagnostics include at
// least one path the test filter is configured to drop. The fixture
// uses two files in the same package so we can exercise per-file
// scoping in the filter cache.
func streamingFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sfilter\n\ngo 1.21\n"), 0o600); err != nil {
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

func streamingRunInput(t *testing.T, fixture string, filter *exclusion.Filter, l0c *l0.Cache) RunInput {
	t.Helper()
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
		Workspace: subproc.WorkspaceRef{ModuleRoot: fixture},
		L1:        l1,
		L2:        l2,
		L0:        l0c,
		Filter:    filter,
	}
}

// TestL0_StoresPostFilter is the load-bearing semantic test: after a
// cold run with a filter that drops every diagnostic from the source
// file, the L0 entry on disk must contain ZERO diagnostics — not the
// raw pre-filter set. This is the contract the prior dispatch broke.
func TestL0_StoresPostFilter(t *testing.T) {
	dir := streamingFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Build a filter that drops every diagnostic from main.go via a
	// path rule. After the cold run the L0 entries should be empty.
	cfg := &config.Config{}
	cfg.Linters.Exclusions = config.LinterExclusions{
		Paths: []string{"main\\.go$"},
	}
	filter, err := exclusion.NewFilter(cfg, dir, nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	in := streamingRunInput(t, dir, filter, l0c)
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	// Run-level result: zero diagnostics because the path rule
	// dropped them all.
	if len(res.Diagnostics) != 0 {
		t.Errorf("cold Run returned %d diagnostics, want 0 (filter should have dropped them all): %+v",
			len(res.Diagnostics), res.Diagnostics)
	}

	// L0 must have stored entries — empty entries, NOT pre-filter
	// entries. Verify by counting Stores and re-running with a
	// filter that would PASS everything; the warm hit must serve
	// the empty cached set, not the raw analyzer output.
	storesAfterCold := l0c.MetricsPtr().Snapshot().Stores
	if storesAfterCold == 0 {
		t.Fatalf("L0 stores = 0 after cold run; zero-diagnostic packages must still cache empty entries")
	}

	// Warm run with the SAME filter config so the L0 key matches and
	// the cached entry is served. A warm hit bypasses the filter, so
	// if the entry had been stored PRE-filter the raw diagnostics would
	// resurface here — they must not. (The warm filter must match the
	// cold one: the suppression config is now folded into the L0 key,
	// so a different filter would miss and re-analyze instead of hit.)
	warmCfg := &config.Config{}
	warmCfg.Linters.Exclusions = config.LinterExclusions{
		Paths: []string{"main\\.go$"},
	}
	warmFilter, err := exclusion.NewFilter(warmCfg, dir, nil)
	if err != nil {
		t.Fatalf("warm NewFilter: %v", err)
	}
	// Reopen L1/L2 fresh so L0 is the only signal.
	warmIn := streamingRunInput(t, dir, warmFilter, l0c)
	warmRes, err := Run(context.Background(), warmIn)
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	if len(warmRes.Diagnostics) != 0 {
		t.Errorf("warm Run returned %d diagnostics, want 0 (L0 must serve post-filter empty set, not re-run raw): %+v",
			len(warmRes.Diagnostics), warmRes.Diagnostics)
	}
	hits := l0c.MetricsPtr().Snapshot().Hits
	if hits == 0 {
		t.Errorf("warm Run produced zero L0 hits; expected ≥1")
	}
}

// TestL0_WarmHitsBypassFilter pins the bypass-on-hit contract: when the
// suppression config is UNCHANGED, a warm L0 hit serves the cached
// post-filter diagnostics directly without re-running the filter. The
// filter config is folded into the L0 key, so "unchanged config" is the
// precondition for a hit — a changed config is covered by
// TestL0_FilterConfigChangeInvalidatesEntry below.
func TestL0_WarmHitsBypassFilter(t *testing.T) {
	dir := streamingFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Cold run: permissive filter, populates L0 with real diagnostics.
	permissive, err := exclusion.NewFilter(&config.Config{}, dir, nil)
	if err != nil {
		t.Fatalf("permissive NewFilter: %v", err)
	}
	in := streamingRunInput(t, dir, permissive, l0c)
	coldRes, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	coldDiagCount := len(coldRes.Diagnostics)
	if coldDiagCount == 0 {
		t.Skip("fixture produced no diagnostics; cannot exercise warm-hit-bypass")
	}

	// Warm run: the SAME permissive filter, so the L0 key matches and
	// the entry is served. The warm path must not reach the analyzer
	// driver and must serve the cached diagnostics verbatim (no
	// re-filtering of an L0 hit).
	warmPermissive, err := exclusion.NewFilter(&config.Config{}, dir, nil)
	if err != nil {
		t.Fatalf("warm permissive NewFilter: %v", err)
	}
	warmIn := streamingRunInput(t, dir, warmPermissive, l0c)
	hook := newRecordingHook()
	SetAnalyzeHookForTest(&warmIn, hook.fn())

	warmRes, err := Run(context.Background(), warmIn)
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	if len(hook.analyzedPkgs()) > 0 {
		t.Fatalf("warm Run reached the analyzer driver (%d pkgs); L0 didn't hit cleanly",
			len(hook.analyzedPkgs()))
	}
	if len(warmRes.Diagnostics) != coldDiagCount {
		t.Errorf("warm Run returned %d diagnostics, want %d (warm path must not re-filter L0 hits)",
			len(warmRes.Diagnostics), coldDiagCount)
	}
}

// TestL0_FilterConfigChangeInvalidatesEntry is the regression test for
// the suppression-cache-key vulnerability. L0 stores the POST-filter
// per-package diagnostic stream, so the effective suppression config
// MUST be part of the L0 key. Otherwise a contributor could land
// vulnerable code plus an exclude-rule that suppresses the linter
// flagging it; the first run caches the empty post-filter result keyed
// only on source; removing the exclude-rule (source byte-identical)
// would replay the cached empty result and the finding would stay
// hidden.
//
// Here we populate L0 under a filter that drops EVERYTHING, then re-run
// with a permissive filter over byte-identical source. The previously
// suppressed diagnostics must resurface: the entry under the old
// suppression config must NOT be served.
func TestL0_FilterConfigChangeInvalidatesEntry(t *testing.T) {
	dir := streamingFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Establish how many diagnostics the package produces with no
	// suppression, so we can assert they resurface after the rule is
	// removed.
	baseline, err := exclusion.NewFilter(&config.Config{}, dir, nil)
	if err != nil {
		t.Fatalf("baseline NewFilter: %v", err)
	}
	baseL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open baseline L0: %v", err)
	}
	baseRes, err := Run(context.Background(), streamingRunInput(t, dir, baseline, baseL0))
	if err != nil {
		t.Fatalf("baseline Run: %v", err)
	}
	if len(baseRes.Diagnostics) == 0 {
		t.Skip("fixture produced no diagnostics; cannot exercise suppression invalidation")
	}

	// Cold run with a suppress-everything filter (the "exclude-rule"):
	// caches an empty post-filter entry.
	suppressCfg := &config.Config{}
	suppressCfg.Linters.Exclusions = config.LinterExclusions{
		Paths: []string{".*"},
	}
	suppress, err := exclusion.NewFilter(suppressCfg, dir, nil)
	if err != nil {
		t.Fatalf("suppress NewFilter: %v", err)
	}
	coldRes, err := Run(context.Background(), streamingRunInput(t, dir, suppress, l0c))
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	if len(coldRes.Diagnostics) != 0 {
		t.Fatalf("cold Run under suppress-all returned %d diagnostics, want 0", len(coldRes.Diagnostics))
	}

	// Warm run after the suppression rule is removed (permissive
	// filter), source byte-identical. The diagnostics must resurface;
	// if the stale empty entry were served they would stay hidden.
	removed, err := exclusion.NewFilter(&config.Config{}, dir, nil)
	if err != nil {
		t.Fatalf("removed NewFilter: %v", err)
	}
	warmRes, err := Run(context.Background(), streamingRunInput(t, dir, removed, l0c))
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	if len(warmRes.Diagnostics) != len(baseRes.Diagnostics) {
		t.Errorf("warm Run after removing suppression returned %d diagnostics, want %d "+
			"(stale post-filter entry was served — suppression config not in L0 key): %+v",
			len(warmRes.Diagnostics), len(baseRes.Diagnostics), warmRes.Diagnostics)
	}
}

// TestStreamingFilter_W6_ColdWarmEquivalence asserts that cold +
// warm produce the same canonical diagnostic stream when both runs
// see the same filter.
func TestStreamingFilter_W6_ColdWarmEquivalence(t *testing.T) {
	dir := streamingFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Build a filter with non-trivial behavior (uniq-by-line +
	// generated-file detection from a real config).
	cfg := config.NewDefault()
	filter, err := exclusion.NewFilter(cfg, dir, nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	in := streamingRunInput(t, dir, filter, l0c)
	coldRes, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	warmIn := streamingRunInput(t, dir, filter, l0c)
	warmRes, err := Run(context.Background(), warmIn)
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	c := canonicalForCompare(coldRes.Diagnostics)
	w := canonicalForCompare(warmRes.Diagnostics)
	if c != w {
		t.Errorf("W6 cold/warm mismatch under streaming filter:\ncold=%s\nwarm=%s",
			c, w)
	}
	// Confirm warm actually hit L0 — without this, the equivalence
	// is trivially true (both runs did identical work).
	if h := l0c.MetricsPtr().Snapshot().Hits; h == 0 {
		t.Errorf("warm Run produced zero L0 hits; equivalence is trivial")
	}
}

// TestStreamingFilter_W6_L0_Disabled extends the cold↔warm
// equivalence to the three-way contract: cold == warm-with-L0 ==
// warm-no-L0 (no L0 cache; engine forced through Analyze on every
// package).
func TestStreamingFilter_W6_L0_Disabled(t *testing.T) {
	dir := streamingFixture(t)
	cfg := config.NewDefault()
	filter, err := exclusion.NewFilter(cfg, dir, nil)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	// Shared L0 across cold+warm; the warm run hits.
	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open shared L0: %v", err)
	}

	coldIn := streamingRunInput(t, dir, filter, sharedL0)
	coldRes, err := Run(context.Background(), coldIn)
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	warmIn := streamingRunInput(t, dir, filter, sharedL0)
	warmRes, err := Run(context.Background(), warmIn)
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}

	// L0-disabled path: pass nil L0; the engine routes every
	// package through Analyze.
	noL0In := streamingRunInput(t, dir, filter, nil)
	noL0In.L0 = nil
	noL0Res, err := Run(context.Background(), noL0In)
	if err != nil {
		t.Fatalf("no-l0 Run: %v", err)
	}

	c := canonicalForCompare(coldRes.Diagnostics)
	w := canonicalForCompare(warmRes.Diagnostics)
	n := canonicalForCompare(noL0Res.Diagnostics)
	if c != w {
		t.Errorf("cold != warm:\ncold=%s\nwarm=%s", c, w)
	}
	if c != n {
		t.Errorf("cold != warm-no-L0:\ncold=%s\nno-l0=%s", c, n)
	}
}

// TestTargetScope_FiltersSubprocDiagnostics is the regression
// test. Subproc Runners scan the full workspace regardless of the
// user's target patterns; a target-scope filter must drop the
// outside-target diagnostics from the merged stream the same way it
// drops them on the in-process path. Before this fix the engine merged
// subDiags into out.Diagnostics without filtering.
func TestTargetScope_FiltersSubprocDiagnostics(t *testing.T) {
	dir := streamingFixture(t)

	cfg := config.NewDefault()
	filter, err := exclusion.NewFilter(cfg, dir, []string{"./pkg/in/..."})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	in := streamingRunInput(t, dir, filter, nil)
	// Inject a stub subproc Runner that emits diagnostics from inside
	// AND outside the user's target. The filter must drop the
	// outside-target one.
	stub := &stubRunner{
		name: "stub-subproc",
		diags: []output.Diagnostic{
			{Linter: "stub-subproc", Message: "inside target", Pos: output.Position{Filename: filepath.Join(dir, "pkg/in/x.go"), Line: 1}},
			{Linter: "stub-subproc", Message: "outside target", Pos: output.Position{Filename: filepath.Join(dir, "pkg/out/y.go"), Line: 1}},
			{Linter: "stub-subproc", Message: "cmd outside target", Pos: output.Position{Filename: filepath.Join(dir, "cmd/c1aw/main.go"), Line: 1}},
		},
	}
	SetExtraSubprocRunnersForTest(&in, []subproc.Runner{stub})

	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	var stubKept []output.Diagnostic
	for _, d := range res.Diagnostics {
		if d.Linter == "stub-subproc" {
			stubKept = append(stubKept, d)
		}
	}
	if len(stubKept) != 1 {
		t.Fatalf("expected 1 surviving subproc diagnostic, got %d: %+v", len(stubKept), stubKept)
	}
	if stubKept[0].Message != "inside target" {
		t.Errorf("wrong subproc diagnostic survived: %+v", stubKept[0])
	}
}

// TestTargetScope_SubprocPassthroughOnFullRepo asserts the inverse: a
// `./...` invocation must not drop subproc diagnostics — target-scope
// is a no-op for full-repo runs (the filter only narrows when the
// CLI passed positional patterns).
func TestTargetScope_SubprocPassthroughOnFullRepo(t *testing.T) {
	dir := streamingFixture(t)

	cfg := config.NewDefault()
	filter, err := exclusion.NewFilter(cfg, dir, []string{"./..."})
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}

	in := streamingRunInput(t, dir, filter, nil)
	stub := &stubRunner{
		name: "stub-subproc",
		diags: []output.Diagnostic{
			{Linter: "stub-subproc", Message: "pkg/in", Pos: output.Position{Filename: filepath.Join(dir, "pkg/in/x.go"), Line: 1}},
			{Linter: "stub-subproc", Message: "pkg/out", Pos: output.Position{Filename: filepath.Join(dir, "pkg/out/y.go"), Line: 1}},
		},
	}
	SetExtraSubprocRunnersForTest(&in, []subproc.Runner{stub})

	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}

	var stubKept []output.Diagnostic
	for _, d := range res.Diagnostics {
		if d.Linter == "stub-subproc" {
			stubKept = append(stubKept, d)
		}
	}
	if len(stubKept) != 2 {
		t.Fatalf("expected both subproc diagnostics to survive `./...`, got %d: %+v", len(stubKept), stubKept)
	}
}

// dummyForOutputImport keeps the output import referenced from the
// pkg-init checker even when a future refactor removes it from the
// test bodies.
var _ output.Diagnostic
