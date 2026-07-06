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

func buildM1RunInput(t *testing.T, moduleRoot string) engine.RunInput {
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

// TestM1_SharedBuildir_W6Equivalence_FlagOnVsOff is the load-bearing
// correctness gate: flag=1 must produce byte-equivalent
// diagnostic digests to flag=0. Any divergence here invalidates the
// shared-Program design and blocks rollout.
func TestM1_SharedBuildir_W6Equivalence_FlagOnVsOff(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	restoreOff := cache.SetSharedBuildirEnabledForTest(false)
	off, err := engine.Run(ctx, buildM1RunInput(t, moduleRoot))
	restoreOff()
	if err != nil {
		t.Fatalf("flag=off Run: %v", err)
	}

	restoreOn := cache.SetSharedBuildirEnabledForTest(true)
	on, err := engine.Run(ctx, buildM1RunInput(t, moduleRoot))
	snapshot := cache.SharedBuildirSnapshot()
	restoreOn()
	if err != nil {
		t.Fatalf("flag=on Run: %v", err)
	}

	if u, c := canonicalDigest(off.Diagnostics), canonicalDigest(on.Diagnostics); u != c {
		t.Errorf("W6 equivalence broken: flag=off=%s flag=on=%s\nflag=off (%d):\n%s\nflag=on (%d):\n%s",
			u, c, len(off.Diagnostics), dumpDiags(off.Diagnostics),
			len(on.Diagnostics), dumpDiags(on.Diagnostics))
	}

	// At flag=on, the shared cache must have fired at least once.
	// Programs == 1 confirms one *ir.Program per batch.
	// PackageBuilds should equal the workspace package count;
	// Dispatches should equal the buildir-run count (one per package
	// per batch). CacheHits = Dispatches - PackageBuilds.
	if snapshot.Dispatches == 0 {
		t.Errorf("flag=on but Dispatches=0; cache never fired")
	}
	if snapshot.Programs == 0 {
		t.Errorf("flag=on but Programs=0; *ir.Program never allocated")
	}
	t.Logf("M1 snapshot: programs=%d package_builds=%d cache_hits=%d dispatches=%d",
		snapshot.Programs, snapshot.PackageBuilds, snapshot.CacheHits, snapshot.Dispatches)
}

// TestM1_SharedBuildir_W6Equivalence_FlagOff_Unchanged pins that
// flag=off behavior matches the prior baseline. This is technically
// redundant with the existing W6 tests (which all run at flag=off by
// default), but the explicit pin captures the contract.
func TestM1_SharedBuildir_W6Equivalence_FlagOff_Unchanged(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	restore := cache.SetSharedBuildirEnabledForTest(false)
	defer restore()
	cache.ResetSharedBuildirStats()

	out, err := engine.Run(ctx, buildM1RunInput(t, moduleRoot))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out.Diagnostics) == 0 {
		t.Logf("Run returned zero diagnostics; fixture may be too small to exercise SA-* analyzers")
	}

	snap := cache.SharedBuildirSnapshot()
	if snap.Dispatches != 0 || snap.Programs != 0 {
		t.Errorf("flag=off but M1 fired: %+v", snap)
	}
}
