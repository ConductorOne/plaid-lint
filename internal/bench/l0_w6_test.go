// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"testing"
	"time"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestL0_W6_DigestEquivalence is the L0-aware extension of the W6
// cold↔warm digest equivalence contract. It drives engine.Run three
// times against a small generated fixture and asserts:
//
//   - Cold run (fresh L0): produces digest D_cold.
//   - Warm run with the same L0: produces digest D_warm == D_cold.
//   - Warm run with a FRESH L0 (forces the L1-only path through the
//     engine): produces D_warm_no_l0 == D_cold.
//
// The three-way equality contract lives here.
func TestL0_W6_DigestEquivalence(t *testing.T) {
	requireGo(t)
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

	// Shared L0 across cold + warm; the warm run should hit on every
	// package and short-circuit Analyze.
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
		t.Errorf("W6 L0 equivalence broken: cold=%s warm=%s\ncold diags (%d):\n%s\nwarm diags (%d):\n%s",
			coldDigest, warmDigest, len(coldRes.Diagnostics), dumpDiags(coldRes.Diagnostics),
			len(warmRes.Diagnostics), dumpDiags(warmRes.Diagnostics))
	}

	// Confirm the warm run actually hit L0.
	if h := sharedL0.MetricsPtr().Snapshot().Hits; h == 0 {
		t.Errorf("warm run produced zero L0 hits; expected ≥1")
	}

	// Warm-no-L0 path: fresh L0 means no hits, every package goes
	// through Analyze. Result digest must still match.
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
		t.Errorf("W6 cold ≠ warm-no-L0: cold=%s no_l0=%s\ncold diags:\n%s\nno_l0 diags:\n%s",
			coldDigest, noL0Digest, dumpDiags(coldRes.Diagnostics), dumpDiags(noL0Res.Diagnostics))
	}
}

// canonicalDigest reduces an output.Diagnostic slice to a stable
// sha256 hex.
func canonicalDigest(diags []output.Diagnostic) string {
	dd := make([]output.Diagnostic, len(diags))
	copy(dd, diags)
	output.Sort(dd)
	h := sha256.New()
	for _, d := range dd {
		h.Write([]byte(d.Linter))
		h.Write([]byte{0})
		h.Write([]byte(d.Message))
		h.Write([]byte{0})
		h.Write([]byte(filepath.Base(d.Pos.Filename)))
		h.Write([]byte{0})
		h.Write([]byte(d.Severity))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func dumpDiags(diags []output.Diagnostic) string {
	dd := make([]output.Diagnostic, len(diags))
	copy(dd, diags)
	output.Sort(dd)
	var b []byte
	keys := make([]string, len(dd))
	for i, d := range dd {
		keys[i] = d.Linter + ":" + filepath.Base(d.Pos.Filename) + ":" + d.Message
	}
	sort.Strings(keys)
	for _, k := range keys {
		b = append(b, []byte("  ")...)
		b = append(b, []byte(k)...)
		b = append(b, '\n')
	}
	return string(b)
}
