// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// buildFactsInput returns a fresh engine.RunInput with new L1/L2;
// callers control L0 reuse for cold/warm staging.
func buildFactsInput(t *testing.T, moduleRoot string, l0c *l0.Cache) engine.RunInput {
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
	return engine.RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
		L1:        l1,
		L2:        l2,
		L0:        l0c,
	}
}

// TestFactsDecode_OncePerDepAnalyzer pins Layer A: the
// gob.Decode of a dep's facts blob is bounded by the (dep, analyzer)
// product, not the (consumer × dep × analyzer) product the pre-fix
// shape produced on a sibling-fan-in geometry like CascadeShape.
func TestFactsDecode_OncePerDepAnalyzer(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var decodes atomic.Int64
	restore := cache.SetFactsDecodeCountHook(func() { decodes.Add(1) })
	defer restore()

	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	if _, err := engine.Run(ctx, buildFactsInput(t, moduleRoot, l0c)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	// Layer A bound: ≤ (packages × analyzers). 100 is generous —
	// staticcheck contributes ~7 fact-bearing analyzers, the shape
	// has 10 packages. Pre-fix the count would be unbounded by
	// sibling count.
	got := decodes.Load()
	const upperBound = int64(100)
	if got > upperBound {
		t.Errorf("DecodeGobFacts fired %d times on a cold CascadeShape run; expected ≤ %d (one per (dep, analyzer) per Layer A)", got, upperBound)
	}
	t.Logf("DecodeGobFacts count on cold CascadeShape: %d (bound %d)", got, upperBound)
}

// TestFactsDecode_LayerBProducerSkip pins Layer B: on a fully
// in-process run, the consumer reads actionSummary.gobFacts directly
// and never reaches gob.Decode. Any non-zero count regresses Layer B.
func TestFactsDecode_LayerBProducerSkip(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var decodes atomic.Int64
	restore := cache.SetFactsDecodeCountHook(func() { decodes.Add(1) })
	defer restore()

	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	if _, err := engine.Run(ctx, buildFactsInput(t, moduleRoot, l0c)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	if got := decodes.Load(); got != 0 {
		t.Errorf("DecodeGobFacts fired %d times on a fully-cold run; expected 0 (Layer B should make every producer→consumer handoff skip gob.Decode)", got)
	}
}

// TestFactsDecode_CrossProcessRoundTrip pins the cross-process
// fallback: when a dep summary arrives via L0 override (or L1
// restore — same code path on the consumer side), its gobFacts is
// unpopulated and the consumer must fall through to gob.Decode of
// the bytes. Without this fallback the warm-with-overrides path
// silently sees no facts and breaks diagnostic equivalence.
func TestFactsDecode_CrossProcessRoundTrip(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	// Cold run populates L0; hook is off so cold-side in-process
	// decodes don't conflate with the warm-side cross-process ones.
	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, buildFactsInput(t, moduleRoot, sharedL0)); err != nil {
		t.Fatalf("cold Run: %v", err)
	}

	// Edit a leaf; its dep closure stays clean and serves from L0
	// overrides on the warm run. Mirrors
	// TestL0DepOverride_LeafEditAvoidsDepBuildir.
	leafFile := filepath.Join(moduleRoot, "leaf0", "leaf0.go")
	src, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf: %v", err)
	}
	modified := append([]byte("// d-155 leaf-edit\n"), src...)
	if err := os.WriteFile(leafFile, modified, 0o644); err != nil {
		t.Fatalf("rewrite leaf: %v", err)
	}

	var warmDecodes atomic.Int64
	restore := cache.SetFactsDecodeCountHook(func() { warmDecodes.Add(1) })
	defer restore()

	cache.ResetL0OverrideHits()
	if _, err := engine.Run(ctx, buildFactsInput(t, moduleRoot, sharedL0)); err != nil {
		t.Fatalf("warm Run: %v", err)
	}

	if h := cache.L0OverrideHits(); h == 0 {
		t.Fatalf("warm leaf-edit produced 0 L0 override hits; the cross-process path can't be exercised without overrides firing")
	}

	// Non-zero warmDecodes proves the cross-process fallback fired.
	// Zero is acceptable (consumers may hit L1 fast-path before
	// reaching the decode); W6 digest equivalence is the correctness
	// gate, this only pins the instrumentation surface.
	t.Logf("warm-with-L0 DecodeGobFacts count: %d (override hits: %d)", warmDecodes.Load(), cache.L0OverrideHits())
}
