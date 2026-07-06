// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestAnalyzerDeterminism_NRepeats is the runtime determinism gate
// for cacheKey/summaryHash decoupling. It runs cold analysis N
// times against a small generated workspace, captures the raw bytes
// of every L1 entry the engine produced, and asserts that on every
// (analyzer, packageID, on-disk action-id) coordinate the byte
// representation is identical across repeats.
//
// The contract is stronger than the existing W6 cold↔warm digest
// equivalence: W6 asserts the OUTPUT diagnostics digest is stable; this
// test asserts the CACHED ENTRY BYTES are stable per analyzer. Any
// analyzer whose Run body folds process state (time.Now / rand /
// hostname / env) into its facts blob or Result will produce a
// different L1 entry across repeats and fail this gate by name.
//
// The L1 entry layout is content-addressed (see Cache.l1Path), so a
// stable input digest already implies a stable on-disk file name; the
// FILE CONTENT must match too. We assert both: same set of action-ids
// across runs (count + identity) AND same bytes per action-id.
//
// If this test ever fails, the offender is the FIRST thing the r31b
// decoupling dispatch must address — either by ruling the analyzer
// out of the input-digest substitution or by patching the
// non-determinism source.
func TestAnalyzerDeterminism_NRepeats(t *testing.T) {
	requireGo(t)
	const N = 5
	dir := t.TempDir()
	moduleRoot, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	// Per-run state: keyed by (analyzer, hex action-id) → sha256 of L1
	// entry bytes. The set of (analyzer, action-id) keys must also be
	// identical across runs (any drift is a determinism failure of
	// the key derivation, not just the payload).
	type runState struct {
		entries map[string]string // "<analyzer>/<actionid>" → sha256 hex of bytes
	}
	states := make([]runState, N)

	for i := 0; i < N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		l1Root := filepath.Join(t.TempDir(), "l1")
		l2Root := filepath.Join(t.TempDir(), "l2")
		l0Root := t.TempDir()
		l1c, err := clcache.Open(l1Root)
		if err != nil {
			cancel()
			t.Fatalf("run %d: open L1: %v", i, err)
		}
		l2c, err := clcache.Open(l2Root)
		if err != nil {
			cancel()
			t.Fatalf("run %d: open L2: %v", i, err)
		}
		l0c, err := l0.Open(l0Root)
		if err != nil {
			cancel()
			t.Fatalf("run %d: open L0: %v", i, err)
		}
		cfg := config.NewDefault()
		reg, _, err := registry.Build(cfg)
		if err != nil {
			cancel()
			t.Fatalf("run %d: registry.Build: %v", i, err)
		}
		if _, err := engine.Run(ctx, engine.RunInput{
			Config:    cfg,
			Registry:  reg,
			Workspace: subproc.WorkspaceRef{ModuleRoot: moduleRoot},
			L1:        l1c,
			L2:        l2c,
			L0:        l0c,
		}); err != nil {
			cancel()
			t.Fatalf("run %d: engine.Run: %v", i, err)
		}
		cancel()
		states[i].entries = sampleL1Entries(t, l1c.Path())
	}

	// Compare run 0 against every later run.
	base := states[0].entries
	if len(base) == 0 {
		t.Fatalf("no L1 entries produced on run 0 — fixture or wiring is broken")
	}
	t.Logf("captured %d L1 entries per run across %d repeats", len(base), N)

	for i := 1; i < N; i++ {
		cur := states[i].entries
		// Key-set drift?
		if len(cur) != len(base) {
			t.Errorf("run %d L1 entry count (%d) differs from run 0 (%d)", i, len(cur), len(base))
		}
		// Per-key byte-identity.
		offenders := map[string]struct{}{}
		for k, baseHash := range base {
			curHash, ok := cur[k]
			if !ok {
				offenders[analyzerOf(k)] = struct{}{}
				t.Errorf("run %d missing L1 entry %s present in run 0", i, k)
				continue
			}
			if curHash != baseHash {
				offenders[analyzerOf(k)] = struct{}{}
				t.Errorf("run %d L1 entry bytes diverged for %s: run0=%s runN=%s", i, k, baseHash, curHash)
			}
		}
		for k := range cur {
			if _, ok := base[k]; !ok {
				offenders[analyzerOf(k)] = struct{}{}
				t.Errorf("run %d has extra L1 entry %s not in run 0", i, k)
			}
		}
		if len(offenders) > 0 {
			names := make([]string, 0, len(offenders))
			for n := range offenders {
				names = append(names, n)
			}
			sort.Strings(names)
			t.Logf("run %d offending analyzers: %s", i, strings.Join(names, ", "))
		}
	}
}

// sampleL1Entries walks <cacheRoot>/analyzer/<analyzer>/<shard>/<id>
// and returns a map of "<analyzer>/<actionid>" → sha256(content).
// The on-disk format is content-addressed, so two runs that produced
// the same L1 entry will produce byte-identical files. Two runs that
// disagree on the entry's content will produce DIFFERENT action-ids
// (so the keys will differ); two runs that agree on the action-id
// but produce different bytes would indicate either an encoder bug
// or a content-address collision.
func sampleL1Entries(t *testing.T, cacheRoot string) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join(cacheRoot, "analyzer")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if walkErr == fs.ErrNotExist {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		// Layout: <analyzer>/<shard>/<id>; key = "<analyzer>/<id>".
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			return nil
		}
		analyzer, id := parts[0], parts[2]
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[analyzer+"/"+id] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil && err != fs.ErrNotExist {
		t.Fatalf("walk L1: %v", err)
	}
	return out
}

func analyzerOf(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}
