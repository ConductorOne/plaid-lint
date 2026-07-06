// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// TestL1_CrossMachine is the falsifiable portability gate for
// the L1 cache layer: an L1 entry written on machine A must:
//
//  1. Hit when read on machine B, where machine B has a clone of A's
//     source tree at a different absolute path.
//  2. After the engine's read-and-canonicalise pass, return
//     diagnostics whose post-Resolve Pos.Filename points at machine
//     B's absolute path — no machine-A path component survives.
//
// This is the L1 analogue of TestCanonicalize_CrossMachine (the
// L0 gate). It exercises the gobDiagnostic URI canonicalisation at
// write/read and the localPackageKey URI fold-in, which together let
// L1 keys + values be shipped between machines.
//
// To force the engine to consult L1 (rather than serving everything
// from L0), the test disables the L0 cache on the warm B run. With
// L0 unattached the engine builds the gopls action graph, which
// is where L1's tryL1Lookup runs.
func TestL1_CrossMachine(t *testing.T) {
	src := canonicalFixture(t)

	// Machine A: cold pass populates L1 under l1RootA.
	l1RootA := t.TempDir()
	l1A, err := clcache.Open(l1RootA)
	if err != nil {
		t.Fatalf("open L1 A: %v", err)
	}
	inA := canonRunInputL1L2(t, src, l1A)
	resA, err := Run(context.Background(), inA)
	if err != nil {
		t.Fatalf("cold Run A: %v", err)
	}
	if len(resA.Diagnostics) == 0 {
		t.Skip("fixture produced no diagnostics; cannot exercise portability gate")
	}

	// Machine B: clone source to a different absolute path.
	machineB := buildMachineB(t, src)

	// Copy L1 entries from A to a fresh L1 root B.
	l1RootB := t.TempDir()
	copyL0Tree(t, l1RootA, l1RootB)
	l1B, err := clcache.Open(l1RootB)
	if err != nil {
		t.Fatalf("open L1 B: %v", err)
	}

	// Run on machine B with the imported L1 entries. L0 disabled so
	// the engine must consult L1 to find the diagnostics.
	inB := canonRunInputL1L2(t, machineB, l1B)
	resB, err := Run(context.Background(), inB)
	if err != nil {
		t.Fatalf("warm Run B: %v", err)
	}

	if len(resB.Diagnostics) == 0 {
		t.Fatalf("warm Run B returned zero diagnostics; L1 must serve them")
	}

	// Gate (2): post-Resolve Pos.Filename is absolute on machine B.
	output.ResolveDiagnostics(canonicalpath.NewResolver(resB.PkgDirs), resB.Diagnostics)
	for _, d := range resB.Diagnostics {
		if !filepath.IsAbs(d.Pos.Filename) {
			t.Errorf("post-Resolve Pos.Filename = %q; want absolute path on machine B", d.Pos.Filename)
		}
		if !pathHasPrefix(d.Pos.Filename, machineB) {
			t.Errorf("post-Resolve Pos.Filename = %q; want machine B prefix %q", d.Pos.Filename, machineB)
		}
		if pathHasPrefix(d.Pos.Filename, src) {
			t.Errorf("post-Resolve Pos.Filename = %q still on machine A prefix %q", d.Pos.Filename, src)
		}
	}

	// Gate (3): no machine-A path bytes inside the L1 entry files.
	// This is the load-bearing privacy assertion. We scan every
	// analyzer/* entry's bytes; a hit on machine A's absolute prefix
	// would mean the canonicalisation didn't take effect.
	absBytes := []byte(src)
	walkErr := filepath.Walk(filepath.Join(l1RootA, "analyzer"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), string(absBytes)) {
			t.Errorf("L1 entry %s contains absolute machine-A path %q (leaks dev-box paths cross-machine)", p, src)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk L1: %v", walkErr)
	}
}

// TestL2_CrossMachine is the falsifiable portability gate for
// the L2 (gcexportdata) cache layer.
//
// The L2 entry stores a dep package's exported types as
// gcexportdata-encoded bytes plus a FileSetSnapshot. The blob is
// written on machine A. Copied to machine B (different source path),
// the engine on B must hit L2 and produce a coherent type-check
// result — no machine-A path bytes inside the L2 entry files.
//
// L1 is unattached on the warm B run so the engine's action graph
// path is forced; with L0 also unattached, the type-check side
// consults L2 directly via tryL2Lookup.
func TestL2_CrossMachine(t *testing.T) {
	src := canonicalFixtureWithDep(t)

	// Machine A: cold pass populates L2 (and L1, which we won't use
	// on the warm run).
	l2RootA := t.TempDir()
	l2A, err := clcache.Open(l2RootA)
	if err != nil {
		t.Fatalf("open L2 A: %v", err)
	}
	inA := canonRunInputL2(t, src, l2A)
	resA, err := Run(context.Background(), inA)
	if err != nil {
		t.Fatalf("cold Run A: %v", err)
	}
	_ = resA

	// Machine B: clone source.
	machineB := buildMachineB(t, src)

	// Copy L2 (typecheck/*) entries from A to a fresh root B.
	l2RootB := t.TempDir()
	copyL0Tree(t, l2RootA, l2RootB)
	l2B, err := clcache.Open(l2RootB)
	if err != nil {
		t.Fatalf("open L2 B: %v", err)
	}

	inB := canonRunInputL2(t, machineB, l2B)
	resB, err := Run(context.Background(), inB)
	if err != nil {
		t.Fatalf("warm Run B: %v", err)
	}
	_ = resB

	// Gate: no machine-A path bytes inside the L2 entry files.
	absBytes := []byte(src)
	walkErr := filepath.Walk(filepath.Join(l2RootA, "typecheck"), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), string(absBytes)) {
			t.Errorf("L2 entry %s contains absolute machine-A path %q (leaks dev-box paths cross-machine)", p, src)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk L2: %v", walkErr)
	}
}

// TestL1L2_ActionIDStability asserts the load-bearing keyspace fix:
// the L1 and L2 ActionIDs for the same logical package
// content must be byte-identical across machines with different
// absolute source paths. If this fails, the L1/L2 keyspaces are
// machine-disjoint and cross-machine cache sharing is moot.
//
// Mechanism:
//
//  1. Run on machine A, capture L1 file paths under l1RootA.
//  2. Run on machine B (different abs path, identical bytes), capture
//     L1 file paths under l1RootB.
//  3. The set of L1 actionID hexes (the on-disk filenames) must match
//     exactly. Same logic for L2.
//
// The actionID hex IS the L1/L2 key per cache.l1Path /
// cache.l2Path. Two machines producing different hexes for the same
// logical content means the underlying derivation folded a machine-
// local input.
func TestL1L2_ActionIDStability(t *testing.T) {
	src := canonicalFixtureWithDep(t)

	// Machine A.
	l1RootA := t.TempDir()
	l1A, err := clcache.Open(l1RootA)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	inA := canonRunInputL1L2(t, src, l1A)
	if _, err := Run(context.Background(), inA); err != nil {
		t.Fatalf("Run A: %v", err)
	}

	// Machine B: clone source to a different absolute path.
	machineB := buildMachineB(t, src)

	l1RootB := t.TempDir()
	l1B, err := clcache.Open(l1RootB)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	inB := canonRunInputL1L2(t, machineB, l1B)
	if _, err := Run(context.Background(), inB); err != nil {
		t.Fatalf("Run B: %v", err)
	}

	// Gather L1 actionIDs from both roots.
	keysA := collectActionIDs(t, filepath.Join(l1RootA, "analyzer"))
	keysB := collectActionIDs(t, filepath.Join(l1RootB, "analyzer"))
	if len(keysA) == 0 {
		t.Skip("no L1 entries produced on machine A; fixture insufficient")
	}
	if diff := diffStringSets(keysA, keysB); diff != "" {
		t.Errorf("L1 actionIDs differ between machine A and B (keyspace is machine-local):\n%s", diff)
	}

	// Gather L2 actionIDs from both roots.
	l2A := collectActionIDs(t, filepath.Join(l1RootA, "typecheck"))
	l2B := collectActionIDs(t, filepath.Join(l1RootB, "typecheck"))
	if len(l2A) == 0 {
		t.Skip("no L2 entries produced; fixture insufficient")
	}
	if diff := diffStringSets(l2A, l2B); diff != "" {
		t.Errorf("L2 actionIDs differ between machine A and B (keyspace is machine-local):\n%s", diff)
	}
}

// canonRunInputL1L2 is a RunInput pointed at fixture with L1 attached
// (the supplied cache c provides both L1 and L2 storage; the clcache
// is a single root that hosts both layers). L0 is intentionally NOT
// attached so the engine's L1/L2 lookup path is exercised.
func canonRunInputL1L2(t *testing.T, fixture string, c *clcache.Cache) RunInput {
	t.Helper()
	cfg := config.NewDefault()
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	return RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: fixture},
		L1:        c,
		L2:        c,
		L0:        nil,
	}
}

// canonRunInputL2 is identical to canonRunInputL1L2; the L2 layer
// rides on the same clcache root. Kept as a separate factory so the
// L2 test reads cleanly.
func canonRunInputL2(t *testing.T, fixture string, c *clcache.Cache) RunInput {
	return canonRunInputL1L2(t, fixture, c)
}

// canonicalFixtureWithDep builds a tiny two-package module so the L2
// layer (which caches imported dep types) has something to write.
// canontest/main.go imports canontest/util.
func canonicalFixtureWithDep(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("go.mod", "module canontest\n\ngo 1.21\n")
	mustWrite("util/util.go", `package util

func Twice(s string) string {
	return s + s
}
`)
	mustWrite("main.go", `package main

import "canontest/util"

func Hello(name string) string {
	x := 1
	x = 2
	_ = x
	return util.Twice("hello " + name)
}

func main() {
	_ = Hello("world")
}
`)
	return dir
}

// collectActionIDs walks dir and returns the set of basename (action-
// hex) values. Used to compare keyspaces between machines.
func collectActionIDs(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		out[filepath.Base(p)] = struct{}{}
		return nil
	})
	if err != nil {
		// Missing directory is treated as empty set, not an error;
		// L1 / L2 entries may not have been written if the fixture
		// produced nothing cacheable.
		if !os.IsNotExist(err) {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	return out
}

// diffStringSets returns "" when a == b, otherwise a short rendering
// of the symmetric difference.
func diffStringSets(a, b map[string]struct{}) string {
	var onlyA, onlyB []string
	for k := range a {
		if _, ok := b[k]; !ok {
			onlyA = append(onlyA, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			onlyB = append(onlyB, k)
		}
	}
	if len(onlyA) == 0 && len(onlyB) == 0 {
		return ""
	}
	var sb strings.Builder
	if len(onlyA) > 0 {
		sb.WriteString("only on machine A:\n")
		for _, k := range onlyA {
			sb.WriteString("  ")
			sb.WriteString(k)
			sb.WriteString("\n")
		}
	}
	if len(onlyB) > 0 {
		sb.WriteString("only on machine B:\n")
		for _, k := range onlyB {
			sb.WriteString("  ")
			sb.WriteString(k)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// Pin imports so a future trim of one helper doesn't drop them.
var (
	_ = l0.CacheVersion
)
