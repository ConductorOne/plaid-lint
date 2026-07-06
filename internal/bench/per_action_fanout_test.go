// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"path/filepath"
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

// TestPerActionFanout_W6_Equivalence pins the B.2 contract:
// the optional sequential intra-package action path
// (PLAID_PER_ACTION_FANOUT=0) produces a diagnostic stream
// byte-equivalent to the default per-action goroutine fan-out. This is
// W6 cold↔warm equivalence applied to the dual-branch execActions
// implementation: both branches MUST agree on diagnostics for the same
// fixture + config.
//
// Strategy:
//  1. Build a small fixture and a full engine.RunInput closure factory.
//  2. Run once with the default fan-out (canonical baseline).
//  3. Toggle perActionFanoutEnabled=false via SetPerActionFanoutForTest.
//  4. Run again on a fresh L0 (so the override fast path doesn't mask
//     the action-graph code).
//  5. Assert the canonical diagnostic digests match.
//
// Cache state is isolated per Run via t.TempDir-backed L1/L2/L0 — no
// cross-test contamination.
func TestPerActionFanout_W6_Equivalence(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func() engine.RunInput {
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
		return engine.RunInput{
			Config:    cfg,
			Registry:  reg,
			Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
			L1:        l1,
			L2:        l2,
			L0:        l0c,
		}
	}

	// Branch 1: default fan-out (one goroutine per action).
	defaultRes, err := engine.Run(ctx, build())
	if err != nil {
		t.Fatalf("default-fanout Run: %v", err)
	}
	defaultDigest := canonicalDigest(defaultRes.Diagnostics)

	// Branch 2: serial intra-package action execution. Use the test-only
	// setter so the toggle is unambiguous (env vars are read once at
	// startup and would be a no-op here).
	restore := cache.SetPerActionFanoutForTest(false)
	defer restore()

	serialRes, err := engine.Run(ctx, build())
	if err != nil {
		t.Fatalf("serial-fanout Run: %v", err)
	}
	serialDigest := canonicalDigest(serialRes.Diagnostics)

	if defaultDigest != serialDigest {
		t.Errorf("B.2 W6 equivalence broken: default=%s serial=%s\ndefault diags (%d):\n%s\nserial diags (%d):\n%s",
			defaultDigest, serialDigest,
			len(defaultRes.Diagnostics), dumpDiags(defaultRes.Diagnostics),
			len(serialRes.Diagnostics), dumpDiags(serialRes.Diagnostics))
	}
}
