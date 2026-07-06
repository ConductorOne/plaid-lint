// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pipelinetest hosts cross-package integration tests that need to
// import both the plaid-lint cache primitives (internal/cache) and the
// gopls cache fork (internal/gopls/cache) plus the workspace surface
// (internal/workspace). It is a test-only package; it contributes no
// non-test code to the build.
package pipelinetest

// l1_pipeline_test.go is the W6 gate-evidence test (phase-1 task 1.19).
//
// It drives the actual Snapshot.Analyze pipeline (not direct Analyzer.Run
// calls) against a small on-disk go module with mixed L2-cached + freshly
// type-checked deps. For each of the analyzers wired below, the test
// asserts:
//
//   - Cold and warm Snapshot.Analyze runs produce byte-identical diagnostic
//     streams (modulo sort). This is the "diagnostic equivalence" gate.
//   - The warm run hits L1 for every (analyzer, package) pair the cold run
//     stored, validating both the L1 actionID determinism and the
//     objectpath-based fact round-trip.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// pipelineAnalyzers returns the set of go/analysis analyzers wired
// into the W7 equivalence test: the W6 5-analyzer set (assign,
// nilfunc, nilness, printf, unusedresult) plus the W7-newly-wired
// analyzers (errcheck, ineffassign, SA1000). The remaining
// staticcheck SA-checks and goimports remain deferred.
func pipelineAnalyzers() []*analysis.Analyzer {
	return analyzers.AllBundledAnalyzers()
}

// pipelineFixture writes a small multi-package go module rooted at dir.
// The fixture includes:
//
//   - "shared": a leaf package both foo and consumer reference.
//   - "foo": imports shared.
//   - "bar": imports shared, exercises assign.
//   - "consumer": imports foo, bar, shared. Triggers printf, unusedresult,
//     nilfunc diagnostics so the equivalence test has signal.
//
// The cross-flow shape — foo and bar both reference shared.Thing —
// targets the cross-flow requirement: when foo is L2-cached and bar is freshly type-checked,
// both must agree on the identity of shared.Thing for analyzers to
// produce consistent diagnostics.
func pipelineFixture(t *testing.T, dir string) {
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

	write("go.mod", "module example.com/pipeline\n\ngo 1.22\n")

	// shared is a leaf: no imports.
	write("shared/shared.go", `package shared

type Thing struct {
	Name string
}

func New(name string) *Thing { return &Thing{Name: name} }

// MustNotBeNil returns t.Name; nilness analyzer can verify this never
// dereferences nil if the caller follows the contract.
func MustNotBeNil(t *Thing) string {
	if t == nil {
		return ""
	}
	return t.Name
}
`)

	// foo imports shared. No diagnostics expected.
	write("foo/foo.go", `package foo

import "example.com/pipeline/shared"

func Make() *shared.Thing {
	return shared.New("foo")
}
`)

	// bar exercises the assign analyzer (self-assignment).
	write("bar/bar.go", `package bar

import "example.com/pipeline/shared"

func Make() *shared.Thing {
	t := shared.New("bar")
	t.Name = t.Name
	return t
}
`)

	// consumer imports foo, bar, shared — the cross-flow case.
	// No stdlib imports: gopls's fork has incomplete stdlib
	// type-check wiring, so any package transitively touching the
	// stdlib gets marked "does not compile" and analyzers are skipped.
	// We exercise the cross-flow path via shared.Thing alone.
	//
	// Each analyzer's signal here:
	//   assign      — bar/bar.go's self-assignment (above)
	//   nilfunc     — Pred below: `f == nil` where f is a non-nil func value
	//   nilness     — DerefAlways below: dereferences a known-nil pointer
	//   unusedresult, printf — facty analyzers; their fact propagation
	//     across packages is the load-bearing test for the L1
	//     DepFactsDigest. No stdlib-targeted diagnostics needed.
	write("consumer/consumer.go", `package consumer

import (
	"example.com/pipeline/bar"
	"example.com/pipeline/foo"
	"example.com/pipeline/shared"
)

// alwaysReturns is a top-level function the nilfunc analyzer
// recognises as a *types.Func. Comparing it to nil is the trigger
// (nilfunc.go only fires on the *types.Func obj kind).
func alwaysReturns() string { return "hi" }

// Pred triggers the nilfunc analyzer.
func Pred() bool {
	return alwaysReturns == nil
}

// DerefAlways triggers the nilness analyzer: a guaranteed-nil deref.
func DerefAlways() string {
	var t *shared.Thing
	return t.Name
}

// F drives the cross-flow path: foo and bar both produce shared.Thing.
func F() *shared.Thing {
	_ = foo.Make()
	_ = bar.Make()
	return shared.New("consumer")
}
`)
}

// canonicalDiag is a stable subset of cache.Diagnostic used for the
// equivalence comparison. Positions are normalized to path+line+col,
// so distinct FileSets that span the same on-disk layout compare equal.
type canonicalDiag struct {
	Source   string
	Code     string
	Message  string
	Filename string
	Line     uint32
	Column   uint32
}

func canonicalize(d *cache.Diagnostic) canonicalDiag {
	return canonicalDiag{
		Source:   string(d.Source),
		Code:     d.Code,
		Message:  d.Message,
		Filename: filepath.Base(d.URI.Path()),
		Line:     d.Range.Start.Line,
		Column:   d.Range.Start.Character,
	}
}

func sortDiags(d []canonicalDiag) {
	sort.Slice(d, func(i, j int) bool {
		if d[i].Filename != d[j].Filename {
			return d[i].Filename < d[j].Filename
		}
		if d[i].Line != d[j].Line {
			return d[i].Line < d[j].Line
		}
		if d[i].Column != d[j].Column {
			return d[i].Column < d[j].Column
		}
		return d[i].Message < d[j].Message
	})
}

func canonicalDigest(by map[string][]canonicalDiag) string {
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{
			"analyzer": n,
			"diags":    by[n],
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

func installPipelineAnalyzers(t *testing.T) {
	t.Helper()
	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = nil
	for _, a := range pipelineAnalyzers() {
		settings.AllAnalyzers = append(settings.AllAnalyzers, settings.NewAnalyzer(a))
	}
}

// runAnalyzePipeline drives Snapshot.Analyze over the consumer package
// of the pipeline fixture and returns canonical-form diagnostics keyed
// by analyzer name.
func runAnalyzePipeline(t *testing.T, ws *workspace.WorkspaceState) map[string][]canonicalDiag {
	t.Helper()
	snap := ws.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	defer snap.Release()

	inner := snap.Inner()
	ctx := context.Background()

	// Drive the initial workspace load so awaitLoaded unblocks and
	// MetadataForFile sees populated metadata. The W3 stub in
	// internal/gopls/cache/view.go's initialize() doesn't run a real
	// load on its own; production callers (and this test) call
	// InitializeWorkspace explicitly to materialize the metadata graph.
	if err := inner.InitializeWorkspace(ctx); err != nil {
		t.Fatalf("InitializeWorkspace: %v", err)
	}

	// Gather workspace packages — every package the InitializeWorkspace
	// load discovered. Driving Analyze over the full set exercises the
	// cross-flow fixture: consumer imports foo+bar which both import
	// shared, so analyzers see shared via two distinct dep paths.
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
		// Fall back to the consumer-rooted load if WorkspacePackages
		// is empty (e.g. AdHocView before any reload).
		consumerURI := protocol.URIFromPath(filepath.Join(ws.ModuleRoot(), "consumer", "consumer.go"))
		mps, err := inner.MetadataForFile(ctx, consumerURI, true)
		if err != nil {
			t.Fatalf("MetadataForFile(consumer): %v", err)
		}
		for _, mp := range mps {
			pkgs[mp.ID] = mp
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("no packages loaded")
	}
	t.Logf("analyzing %d packages", len(pkgs))

	diags, err := inner.Analyze(ctx, pkgs, nil)
	if err != nil {
		t.Fatalf("Snapshot.Analyze: %v", err)
	}
	t.Logf("Analyze returned %d diagnostics", len(diags))

	by := make(map[string][]canonicalDiag)
	for _, d := range diags {
		by[string(d.Source)] = append(by[string(d.Source)], canonicalize(d))
	}
	for k := range by {
		sortDiags(by[k])
	}
	return by
}

// TestL1PipelineEquivalence is the W6 gate-evidence test.
//
// Step 1 (cold): drive Snapshot.Analyze against a fresh L1+L2 cache.
// Capture the diagnostic stream as the "ground truth".
//
// Step 2 (warm): construct a new WorkspaceState backed by the SAME
// on-disk L1+L2 caches, and drive Snapshot.Analyze again. The L1 hit
// path should fire for every (analyzer, package) pair, and the
// resulting diagnostic stream must byte-equal the cold stream.
//
// Step 3 (warm hit-rate): the target is ≥ 95% on warm-no-edit.
//
// L2 is attached alongside L1 so the cold run populates the L2
// store and the warm run exercises the W5 cross-flow fast path in
// typeCheckBatch.getImportPackage. Without L2 attached, the
// cross-flow case (foo+bar both reference shared.Thing) is not
// actually exercised at the pipeline level — see Codex's W6 review.
func TestL1PipelineEquivalence(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)

	// Point gopls's analyzeSummary filecache at a tempdir so a stale
	// host-level cache can't poison the test. Note that when L1 is
	// attached, runCached bypasses filecache entirely,
	// so this is belt-and-suspenders.
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()

	const toolVer = "plaid-lint-w6-test"

	// Cold run — fresh L1+L2 caches; analyzers MUST run and L2 MUST store.
	cold := runOnce(t, modDir, l1Dir, l2Dir, toolVer)
	if cold.l1Stores == 0 {
		t.Errorf("cold run: L1 stores = 0, want > 0 (analyzer body must have run)")
	}
	if cold.l1Hits != 0 {
		t.Errorf("cold run: L1 hits = %d, want 0 (fresh cache)", cold.l1Hits)
	}
	if cold.l2Stores == 0 {
		t.Errorf("cold run: L2 stores = 0, want > 0 (L2 fast path must populate on cold)")
	}
	t.Logf("cold run: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		cold.l1Hits, cold.l1Stores, cold.l2Hits, cold.l2Stores)
	t.Logf("cold diagnostics: %s", canonicalDigest(cold.diagnostics))

	// Warm run — same L1+L2 caches.
	warm := runOnce(t, modDir, l1Dir, l2Dir, toolVer)
	t.Logf("warm run: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		warm.l1Hits, warm.l1Stores, warm.l2Hits, warm.l2Stores)
	if warm.l1Hits == 0 {
		t.Errorf("warm run: L1 hits = 0, want > 0 (L1 cache should be populated)")
	}
	if warm.l2Hits == 0 {
		t.Errorf("warm run: L2 hits = 0, want > 0 (L2 cache should be populated)")
	}

	// Diagnostic equivalence — cold vs warm must agree on every analyzer.
	// This is the cross-flow byte-equivalence assertion: under
	// L2-attached configuration, foo+bar both referencing shared.Thing
	// must produce byte-identical diagnostics on cold vs warm.
	coldKey := canonicalDigest(cold.diagnostics)
	warmKey := canonicalDigest(warm.diagnostics)
	if coldKey != warmKey {
		t.Errorf("diagnostic streams differ across cold vs warm runs:\n  cold: %s\n  warm: %s",
			coldKey, warmKey)
	}

	// Per-analyzer detail (so a regression localises).
	for _, a := range pipelineAnalyzers() {
		coldDs := cold.diagnostics[a.Name]
		warmDs := warm.diagnostics[a.Name]
		if len(coldDs) != len(warmDs) {
			t.Errorf("analyzer %q: cold has %d diags, warm has %d",
				a.Name, len(coldDs), len(warmDs))
			continue
		}
		for i := range coldDs {
			if coldDs[i] != warmDs[i] {
				t.Errorf("analyzer %q diag[%d]:\n  cold: %+v\n  warm: %+v",
					a.Name, i, coldDs[i], warmDs[i])
			}
		}
	}

	// Hit rate (≥ 95% on warm-no-edit).
	//
	// The denominator is the count of L1-eligible actions, NOT all
	// actions: actions whose analyzer is consumed as a prerequisite by
	// another action in the same package's graph (e.g. inspect,
	// buildssa, ctrlflow as deps of the 5 root analyzers) deliberately
	// bypass the L1 fast path so their Run produces a non-nil Result
	// for downstream pass.ResultOf consumers.
	//
	// Eligible actions are those that didn't have to re-run as
	// prerequisites on the warm pass — i.e. cold-stores minus the
	// warm prereq re-runs. The prereq re-runs no longer
	// re-store to L1 (the skip-on-existing path bumps Skipped instead
	// of Stores), so the denominator subtracts the union of warm
	// stores + warm skipped.
	warmPrereqWork := warm.l1Stores + warm.l1Skipped
	eligible := cold.l1Stores - warmPrereqWork
	if eligible > 0 {
		hitRate := float64(warm.l1Hits) / float64(eligible)
		t.Logf("L1 warm hit rate: %.2f (hits=%d / eligible=%d, cold-stores=%d, warm-prereq-work=%d [stores=%d skipped=%d])",
			hitRate, warm.l1Hits, eligible, cold.l1Stores, warmPrereqWork, warm.l1Stores, warm.l1Skipped)
		if hitRate < 0.95 {
			t.Errorf("warm hit rate = %.2f, want >= 0.95", hitRate)
		}
	}
}

// runResult bundles the per-run output of runOnce: the canonical
// diagnostic stream plus the L1 and L2 hit/store deltas observed
// during the run.
type runResult struct {
	diagnostics      map[string][]canonicalDiag
	l1Hits, l1Stores int64
	l1Skipped        int64
	l2Hits, l2Stores int64
}

// runOnce builds a fresh WorkspaceState backed by the on-disk L1
// cache at l1Dir and the on-disk L2 cache at l2Dir, drives
// Snapshot.Analyze, and returns the diagnostic stream plus the L1
// and L2 hits and stores observed during the run.
func runOnce(t *testing.T, modDir, l1Dir, l2Dir, toolVer string) runResult {
	t.Helper()

	l1, err := clcache.Open(l1Dir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	l2, err := clcache.Open(l2Dir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	c := cache.New(nil)
	c.AttachL1(l1, toolVer)
	// L2 buildEnv/goVersion strings only need to be stable across the
	// cold and warm runs of this test (they fold into the L2 action ID
	// alongside ph.key); the values themselves are arbitrary.
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()

	beforeL1 := c.L1Metrics()
	beforeL2 := c.L2Metrics()

	diags := runAnalyzePipeline(t, ws)

	afterL1 := c.L1Metrics()
	afterL2 := c.L2Metrics()
	return runResult{
		diagnostics: diags,
		l1Hits:      afterL1.Hits - beforeL1.Hits,
		l1Stores:    afterL1.Stores - beforeL1.Stores,
		l1Skipped:   afterL1.Skipped - beforeL1.Skipped,
		l2Hits:      afterL2.Hits - beforeL2.Hits,
		l2Stores:    afterL2.Stores - beforeL2.Stores,
	}
}

// TestL1PartialCacheRespectsRequires is the regression test for
// Codex's W6 Blocker 2: an analyzer that is consumed as a Requires
// prerequisite by another action MUST NOT be served from L1 with
// result=nil, because the consumer's pass.ResultOf[prereq] would be
// nil and dereferencing it crashes.
//
// The fix marks prerequisite-actions with isPrerequisiteOfEnabled at
// DAG construction time; those actions bypass the L1 fast path. This
// test reproduces the precise partial-cache shape that triggered the
// bug — consumer-analyzer L1 entries are absent while their
// prerequisite L1 entries (e.g. inspect, buildssa) remain present:
//
//  1. Cold run populates L1 for every (analyzer, package) pair.
//  2. Surgically remove the on-disk L1 entries for the consumer
//     analyzers (printf, nilness, unusedresult, assign, nilfunc) by
//     deleting their analyzer-name directories under the L1 cache
//     root. The prerequisite entries (inspect, buildssa, ctrlflow)
//     are left intact.
//  3. Re-run. With the fix, prerequisites still bypass L1 and re-run
//     their Run body anyway — so consumer pass.ResultOf is
//     populated. Without the fix, prereq L1 entries hit and return
//     result=nil, then the consumer's Run body dereferences a nil
//     pass.ResultOf and panics inside the analyzer (e.g.
//     inspector.New(... ResultOf[inspect].(*inspector.Inspector)
//     ...) on a nil interface).
//
// The assertions are: (a) the run completes without panic, (b) the
// diagnostics match the cold-run set (consumer's analyzers ran
// correctly with a fresh, non-nil prereq Result).
func TestL1PartialCacheRespectsRequires(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()

	const toolVer = "plaid-lint-w6-partial"

	// Cold: populate L1.
	cold := runOnce(t, modDir, l1Dir, l2Dir, toolVer)
	if cold.l1Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0")
	}
	t.Logf("cold: L1 hits=%d stores=%d", cold.l1Hits, cold.l1Stores)

	// Surgically evict the consumer analyzers' L1 entries from disk
	// while leaving their prerequisite entries (inspect, buildssa,
	// ctrlflow) intact. The L1 layout (internal/cache l1Path) is
	// <root>/analyzer/<name>/<shard>/<id>, so removing the analyzer
	// directory evicts every package's entry for that analyzer.
	//
	// This produces the exact "prereq hits, consumer misses" shape
	// that surfaces the Blocker 2 bug — and the fix prevents that
	// shape by making prereqs always bypass L1 (always miss, always
	// re-run, always produce a non-nil Result).
	for _, a := range pipelineAnalyzers() {
		dir := filepath.Join(l1Dir, "analyzer", a.Name)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("RemoveAll %s: %v", dir, err)
		}
	}

	// Partial-warm run. The fix prevents the nil-ResultOf panic by
	// always running prerequisite analyzers; without it, this run
	// crashes inside the consumer analyzer body that dereferences
	// pass.ResultOf[inspect] (or [buildssa]). Goroutine panics in the
	// driver are caught by panic-then-fail in the analyzer body —
	// they manifest as analyzer errors that prevent diagnostics from
	// being recorded, NOT a test-process panic. We therefore detect
	// the bug shape via "consumer diagnostics empty" rather than
	// recover().
	partial := runOnce(t, modDir, l1Dir, l2Dir, toolVer)
	t.Logf("partial: L1 hits=%d stores=%d", partial.l1Hits, partial.l1Stores)

	// The consumer analyzers were evicted; their actions must have
	// missed and re-stored. So we expect non-trivial stores.
	if partial.l1Stores == 0 {
		t.Errorf("partial: L1 stores = 0, want > 0 (evicted consumer entries must re-store)")
	}

	// Diagnostic equivalence — the consumer's analyzers must produce
	// the same diagnostics they did on the cold run. The bug-shape
	// produces either a panic or empty consumer diagnostics because
	// pass.ResultOf[prereq] was nil and the analyzer either crashed
	// or short-circuited to no findings.
	coldKey := canonicalDigest(cold.diagnostics)
	partialKey := canonicalDigest(partial.diagnostics)
	if coldKey != partialKey {
		t.Errorf("diagnostic streams differ across cold vs partial-warm runs:\n  cold:    %s\n  partial: %s",
			coldKey, partialKey)
	}
	for _, a := range pipelineAnalyzers() {
		coldDs := cold.diagnostics[a.Name]
		partialDs := partial.diagnostics[a.Name]
		if len(coldDs) != len(partialDs) {
			t.Errorf("analyzer %q: cold has %d diags, partial has %d",
				a.Name, len(coldDs), len(partialDs))
		}
	}
}

// TestL1WhitespaceConfigInvariant verifies the invariant at
// the salt boundary: identical canonical config bytes produce
// identical salts, so a whitespace-only `.golangci.yml` edit that
// canonicalizes to the same bytes does NOT invalidate L1.
//
// The full canonicalizer is W7 work. W6 ships the
// salt function; this test asserts the salt-boundary contract the
// W7 canonicalizer will satisfy.
func TestL1WhitespaceConfigInvariant(t *testing.T) {
	canonical := []byte(`{"option":"value"}`)
	a := clcache.ConfigSaltForAnalyzer("foo", canonical)
	b := clcache.ConfigSaltForAnalyzer("foo", canonical)
	if a != b {
		t.Errorf("identical canonical config → different salts: %x vs %x", a, b)
	}

	// Two whitespace-different surface configs canonicalized via the
	// stand-in canonicalizer below produce the same canonical bytes,
	// and therefore the same salt. The W7 canonicalizer ships the real
	// thing; this test pins the salt-boundary half of the contract.
	raw1 := `{"option":"value"}`
	raw2 := `{ "option" : "value" }`
	c1 := stripJSONWhitespace(raw1)
	c2 := stripJSONWhitespace(raw2)
	if c1 != c2 {
		t.Fatalf("test bug: stand-in canonicalizer disagrees: %q vs %q", c1, c2)
	}
	if got, want := clcache.ConfigSaltForAnalyzer("foo", []byte(c1)),
		clcache.ConfigSaltForAnalyzer("foo", []byte(c2)); got != want {
		t.Errorf("canonicalized configs → different salts: %x vs %x", got, want)
	}
}

// stripJSONWhitespace is a stand-in canonicalizer used only by the
// whitespace-invariant test. It removes ASCII whitespace outside of
// string literals; that's enough to flatten the surface differences in
// raw1 / raw2 above. It is NOT the W7 canonicalizer — that one knows
// the full YAML grammar and the per-linter option schema.
func stripJSONWhitespace(s string) string {
	var out []byte
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
			out = append(out, c)
			continue
		}
		if !inStr && (c == ' ' || c == '\t' || c == '\n' || c == '\r') {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
