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

func buildM2RunInput(t *testing.T, moduleRoot string) engine.RunInput {
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
	return engine.RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
		L1:        l1, L2: l2, L0: l0c,
	}
}

// TestM2_CapNonZero_LimitsBuildirOnly: cap=2 must bound peak buildir
// concurrency end-to-end through engine.Run. The slot-level unit half lives
// at TestM2_CapNonZero_Throttles in internal/gopls/cache.
func TestM2_CapNonZero_LimitsBuildirOnly(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	const cap = 2
	defer cache.SetBuildirDispatchCapForTest(cap)()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if _, err := engine.Run(ctx, buildM2RunInput(t, moduleRoot)); err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	snap := cache.BuildirDispatchSnapshot()
	if snap.Peak > int64(cap) {
		t.Errorf("cap=%d peak=%d (sub-gate failed)", cap, snap.Peak)
	}
	if snap.Peak == 0 {
		t.Errorf("cap=%d peak=0; buildir never ran", cap)
	}
	if snap.Cur != 0 {
		t.Errorf("cur=%d, want 0 (release/acquire mismatch)", snap.Cur)
	}
}

// TestM2_CapNonZero_PreservesW6: cap=2 and cap=0 must produce the same
// diagnostic digest. The cap is a concurrency knob, not a behavior change.
func TestM2_CapNonZero_PreservesW6(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	restore0 := cache.SetBuildirDispatchCapForTest(0)
	uncapped, err := engine.Run(ctx, buildM2RunInput(t, moduleRoot))
	restore0()
	if err != nil {
		t.Fatalf("uncapped Run: %v", err)
	}
	restore2 := cache.SetBuildirDispatchCapForTest(2)
	capped, err := engine.Run(ctx, buildM2RunInput(t, moduleRoot))
	restore2()
	if err != nil {
		t.Fatalf("capped Run: %v", err)
	}
	if u, c := canonicalDigest(uncapped.Diagnostics), canonicalDigest(capped.Diagnostics); u != c {
		t.Errorf("W6 equivalence broken: uncapped=%s cap=2=%s\nuncapped (%d):\n%s\ncapped (%d):\n%s",
			u, c, len(uncapped.Diagnostics), dumpDiags(uncapped.Diagnostics),
			len(capped.Diagnostics), dumpDiags(capped.Diagnostics))
	}
}
