// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// TestW6CascadeEditComment_DigestEquivalence is the diagnostic-stream
// half of the W6-cascade-edit-comment correctness gate: a
// comment-only edit to a package imported by N roots must not change
// any analyzer's diagnostic output. The L1 cache hit-rate half of the
// gate is covered by [TestW6CascadeEditComment_InputDigestPropagationGap]
// below, which captures the Phase A finding.
//
// This half of the gate stays GREEN regardless of Phase A: digest
// equivalence is the W6 correctness invariant, and Phase A's L1
// changes do not affect analyzer output.
func TestW6CascadeEditComment_DigestEquivalence(t *testing.T) {
	requireGo(t)

	syntaxOnly := makeCascadeSyntaxOnlyAnalyzer()
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        syntaxOnly,
		ConfigSalt:      func(any) [32]byte { return [32]byte{0x71} },
		AnalyzerVersion: "cascade-test-v1",
		CacheVersion:    1,
		TypeUseScope:    analyzers.TypeUseSyntaxOnly,
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(syntaxOnly)}

	modDir := t.TempDir()
	cascadeFixture(t, modDir, 5)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	coldDigest := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry).digest

	// Edit only a comment in app/app.go.
	appFile := filepath.Join(modDir, "app", "app.go")
	body, err := os.ReadFile(appFile)
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	body = append(body, []byte("\n// cascade-edit-comment-test trailer\n")...)
	if err := os.WriteFile(appFile, body, 0o644); err != nil {
		t.Fatalf("rewrite app.go: %v", err)
	}

	warmDigest := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry).digest
	if warmDigest != coldDigest {
		t.Errorf("W6-cascade-edit-comment digest equivalence broken: cold=%s warm=%s", coldDigest, warmDigest)
	}
}

// TestW6CascadeEditComment_InputDigestNarrowed is the falsifiable
// WIN-side gate. Prior to the change the L1 action ID's
// InputDigest was always ph.key — the gopls recursive reachability
// hash — so a content change in any reachable dep flipped the
// InputDigest on every transitive importer (`internal/gopls/cache/
// check.go` ~line 1840). The change narrows the chain-1 channel: for
// TypeUseSyntaxOnly analyzers, InputDigest is now ph.localKey
// (package-local source + import-map IDs + sizes + viewType — no
// dep-content composition), so a comment-only edit to a dep leaves
// every importer's InputDigest invariant and the L1 entries hit.
//
// The previous shape of this test (`...InputDigestPropagationGap`)
// pinned the GAP by asserting warm hits = 0. With chain 1
// closed the assertion direction inverts: warm hits MUST exceed
// stores-on-app, i.e. every importer must hit on warm.
//
// Fixture: 1 app/ + 5 roots, all importing app/. Comment-only edit
// to app/app.go. Expected shape:
//
//   - cold:  6 stores, 0 hits, 6 misses
//   - warm:  1 store (the re-checked app/), 5 hits (root0..root4),
//     1 miss (the re-checked app/)
//
// Pairs with TestW6CascadeEditComment_DigestEquivalence (correctness
// invariant: diagnostic stream unchanged).
func TestW6CascadeEditComment_InputDigestNarrowed(t *testing.T) {
	requireGo(t)

	syntaxOnly := makeCascadeSyntaxOnlyAnalyzer()
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        syntaxOnly,
		ConfigSalt:      func(any) [32]byte { return [32]byte{0x71} },
		AnalyzerVersion: "cascade-test-v1",
		CacheVersion:    1,
		TypeUseScope:    analyzers.TypeUseSyntaxOnly,
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(syntaxOnly)}

	modDir := t.TempDir()
	const numRoots = 5
	cascadeFixture(t, modDir, numRoots)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	cold := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry)
	if cold.l1.Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0; metrics=%+v", cold.l1)
	}
	if cold.l1.SyntaxOnlyHits != 0 {
		t.Fatalf("cold: SyntaxOnly hits = %d, want 0 (empty L1); metrics=%+v",
			cold.l1.SyntaxOnlyHits, cold.l1)
	}

	// Edit only a comment in app/app.go.
	appFile := filepath.Join(modDir, "app", "app.go")
	body, err := os.ReadFile(appFile)
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	body = append(body, []byte("\n// cascade-edit-comment-test trailer\n")...)
	if err := os.WriteFile(appFile, body, 0o644); err != nil {
		t.Fatalf("rewrite app.go: %v", err)
	}

	warm := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry)

	// THE WIN: chain-1 narrowing means every importer's InputDigest
	// (= its ph.localKey) is invariant to dep-internals edits. So
	// the 5 roots hit L1 on warm; only the edited app/ misses.
	//
	// We assert SyntaxOnly hits >= numRoots — a strict-equality check
	// would over-pin (e.g. if the fixture ever adds intermediate
	// helper packages). The "missed app/" lower bound on
	// SyntaxOnlyMisses is asserted separately.
	if warm.l1.SyntaxOnlyHits < numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want >= %d (chain-1 narrowing).\n"+
			"If this regresses, the SyntaxOnly InputDigest is once again sensitive\n"+
			"to dep-content edits — either ph.localKey's derivation changed shape, or\n"+
			"l1.go's inputDigest() lost the SyntaxOnly branch.\nwarm L1 metrics: %+v",
			warm.l1.SyntaxOnlyHits, numRoots, warm.l1)
	}
	// The edited app/ must still miss (its localKey flipped). If
	// warm.SyntaxOnlyMisses is 0, we've accidentally invalidated
	// nothing on a real edit — that would be a CORRECTNESS bug.
	if warm.l1.SyntaxOnlyMisses == 0 {
		t.Errorf("warm: SyntaxOnly L1 misses = 0; the edited app/ package's L1 entry must miss its previous ID. metrics=%+v", warm.l1)
	}
	// And the importer hits must not exceed the importer count —
	// over-hitting would mean the edited app/ is hitting too, i.e.
	// a stale-hit correctness bug (localKey collision across edits).
	if warm.l1.SyntaxOnlyHits > numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want <= %d (over-hit; possible stale L1 entry for edited app/). metrics=%+v",
			warm.l1.SyntaxOnlyHits, numRoots, warm.l1)
	}
	t.Logf("chain-1 narrowed: %d/%d importer packages hit L1 on a "+
		"cascade comment-only edit. cold=%+v warm=%+v",
		warm.l1.SyntaxOnlyHits, numRoots, cold.l1, warm.l1)
}

// cascadeRunResult bundles the per-run outputs the cascade tests
// inspect: canonical diagnostic-stream digest and the absolute L1
// metrics snapshot.
type cascadeRunResult struct {
	digest string
	l1     cache.L1Metrics
}

// runCascadeOnce drives a single Snapshot.Analyze pass against the
// cascade fixture rooted at modDir, using the provided on-disk L1
// and L2 directories. The L1 metrics in the returned struct are the
// snapshot taken at the end of the run (NOT a delta) — callers that
// want a delta diff successive runs themselves.
func runCascadeOnce(
	t *testing.T,
	modDir, l1Dir, l2Dir string,
	registry *analyzers.Registry,
) cascadeRunResult {
	t.Helper()
	const toolVer = "plaid-lint-r20-phase-a-cascade"

	l1, err := clcache.Open(l1Dir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	l2, err := clcache.Open(l2Dir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	c := cache.New(nil)
	c.AttachL1WithRegistry(l1, toolVer, registry)
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()

	diags := runCascadeAnalyze(t, ws)
	return cascadeRunResult{
		digest: canonicalDigest(diags),
		l1:     c.L1Metrics(),
	}
}

// makeCascadeSyntaxOnlyAnalyzer returns the SyntaxOnly analyzer used
// by the cascade tests. Its Run touches only pass.Files — no
// pass.TypesInfo, no pass.Pkg.Imports — matching the SyntaxOnly
// declarative contract.
func makeCascadeSyntaxOnlyAnalyzer() *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: "cascadesyntaxonly",
		Doc:  "synthetic SyntaxOnly analyzer for W6-cascade-edit-comment",
		Run: func(pass *analysis.Pass) (any, error) {
			for _, f := range pass.Files {
				_ = f.Name
			}
			return nil, nil
		},
	}
}

// cascadeFixture writes a small go module rooted at dir with one
// "app" package imported by N root packages root0..root{N-1}.
func cascadeFixture(t *testing.T, dir string, numRoots int) {
	t.Helper()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	write("go.mod", "module example.com/cascade\n\ngo 1.22\n")
	write("app/app.go", `package app

// T is the cascade-mid type touched (only transitively) by every
// root. The cascade-edit-comment test appends a comment trailer
// here — exported API is unchanged.
type T struct {
	Name string
}

// New is the constructor every root imports.
func New(name string) *T { return &T{Name: name} }
`)
	for i := 0; i < numRoots; i++ {
		body := fmt.Sprintf(`package root%d

import "example.com/cascade/app"

// Use is a no-op that wires the app dependency.
func Use() *app.T {
	return app.New("root%d")
}
`, i, i)
		write(fmt.Sprintf("root%d/root%d.go", i, i), body)
	}
}

// runCascadeAnalyze drives Snapshot.Analyze across every workspace
// package and returns canonical-form diagnostics keyed by analyzer
// name.
func runCascadeAnalyze(t *testing.T, ws *workspace.WorkspaceState) map[string][]canonicalDiag {
	t.Helper()
	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	defer snap.Release()

	inner := snap.Inner()
	ctx := context.Background()
	if err := inner.InitializeWorkspace(ctx); err != nil {
		t.Fatalf("InitializeWorkspace: %v", err)
	}

	wsPkgs := inner.WorkspacePackages()
	pkgs := map[metadata.PackageID]*metadata.Package{}
	for id := range wsPkgs.All() {
		mp := inner.Metadata(id)
		if mp == nil {
			continue
		}
		pkgs[mp.ID] = mp
	}
	if len(pkgs) == 0 {
		t.Fatalf("no packages loaded")
	}

	diags, err := inner.Analyze(ctx, pkgs, nil)
	if err != nil {
		t.Fatalf("Snapshot.Analyze: %v", err)
	}
	by := make(map[string][]canonicalDiag)
	for _, d := range diags {
		by[string(d.Source)] = append(by[string(d.Source)], canonicalize(d))
	}
	for k := range by {
		sortDiags(by[k])
	}
	return by
}
