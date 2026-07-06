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
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestInputDigest_W6_Equivalence runs the W6 cold/warm/no-L0 three-way
// equivalence contract with PLAID_INPUT_DIGEST=1 set. The diagnostic
// digest under flag=1 must be byte-identical to flag=0 — the input-
// based cacheKey is a substitution for the output-based derivation,
// not a behavior change.
func TestInputDigest_W6_Equivalence(t *testing.T) {
	requireGo(t)
	t.Setenv("PLAID_INPUT_DIGEST", "1")
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	build := func(l0c *l0.Cache) engine.RunInput {
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

	sharedL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}

	coldRes, err := engine.Run(ctx, build(sharedL0))
	if err != nil {
		t.Fatalf("cold Run: %v", err)
	}
	coldDigest := canonicalDigest(coldRes.Diagnostics)

	warmRes, err := engine.Run(ctx, build(sharedL0))
	if err != nil {
		t.Fatalf("warm Run: %v", err)
	}
	warmDigest := canonicalDigest(warmRes.Diagnostics)
	if coldDigest != warmDigest {
		t.Errorf("flag=1 W6 equivalence broken: cold=%s warm=%s\ncold diags:\n%s\nwarm diags:\n%s",
			coldDigest, warmDigest, dumpDiags(coldRes.Diagnostics), dumpDiags(warmRes.Diagnostics))
	}

	freshL0, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open fresh L0: %v", err)
	}
	noL0Res, err := engine.Run(ctx, build(freshL0))
	if err != nil {
		t.Fatalf("warm-no-l0 Run: %v", err)
	}
	noL0Digest := canonicalDigest(noL0Res.Diagnostics)
	if coldDigest != noL0Digest {
		t.Errorf("flag=1 cold != warm-no-L0: cold=%s no_l0=%s", coldDigest, noL0Digest)
	}
}

// TestInputDigest_W6_VerifyMode runs the W6 equivalence contract with
// BOTH PLAID_INPUT_DIGEST=1 and PLAID_INPUT_DIGEST_VERIFY=1. Verify
// mode adds the inputDigest self-determinism re-derive check inside
// cacheKey; if the hash body has any non-determinism on the production
// analyzer set, verify mode panics. r31a's runtime audit pins zero
// failures expected.
func TestInputDigest_W6_VerifyMode(t *testing.T) {
	requireGo(t)
	t.Setenv("PLAID_INPUT_DIGEST", "1")
	t.Setenv("PLAID_INPUT_DIGEST_VERIFY", "1")
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

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
	in := engine.RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
		L1:        l1,
		L2:        l2,
	}
	if _, err := engine.Run(ctx, in); err != nil {
		t.Fatalf("verify-mode Run: %v", err)
	}
}
