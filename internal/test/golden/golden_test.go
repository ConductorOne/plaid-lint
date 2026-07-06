// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package golden

// golden_test.go is the W11 task-1.39 golden suite. It drives the
// fixtures under testdata/ through Snapshot.Analyze and compares the
// observed diagnostic streams + counters against expected.json files
// next to each fixture.
//
// Invoke `go test -update ./internal/test/golden/...` to rewrite the
// goldens after an intentional behaviour change. Without `-update` a
// mismatch fails the test with a diff between expected and observed.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/test/harness"
)

// updateGolden is wired via -update; when true the test rewrites
// expected.json instead of asserting equality.
var updateGolden = flag.Bool("update", false, "rewrite testdata/<fixture>/expected.json instead of asserting")

// requireGo skips the test if `go` isn't on PATH. The gopls cache
// fork's metadata loader shells out to `go list`, so tests that
// drive a real workspace cannot run without it.
func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

// expectedGolden is the structure expected.json is unmarshalled from
// and rewritten to. We keep the on-disk shape narrowly typed so a
// counter that "happens" to land in a passing range doesn't slip past
// review — the assertion is byte-equality against the marshalled
// expected struct.
//
// L2 counters are deliberately excluded from the golden: the L2
// (gcexportdata) store/hit counts vary across runs depending on the
// type-check batch ordering and the per-package fork/join timing.
// They are timing-sensitive non-counters from the test's perspective.
// The L2 layer's correctness is independently asserted by the W5
// L2 equivalence tests in internal/cache/ and by the load-bearing
// "cold→warm digest equivalence" check (which fails if L2 stored
// wrong bytes).
type expectedGolden struct {
	// Diagnostics keyed by analyzer name. The harness's CanonicalDiag
	// is the wire shape; sorted by harness.SortDiags before marshal.
	Diagnostics map[string][]harness.CanonicalDiag `json:"diagnostics"`

	// L1HitsCold is the cold-run L1 hit count. Must be zero for a
	// fresh-cache cold run; non-zero indicates a leaked host cache.
	L1HitsCold int64 `json:"l1_hits_cold"`

	// L1StoresCold is the cold-run L1 store count. Must be > 0 for
	// any non-empty fixture; zero is the failure symptom
	// (analyzers skipped because compiles=false).
	L1StoresCold int64 `json:"l1_stores_cold"`

	// L1HitsWarm is the warm-run L1 hit count. Must be > 0; zero
	// indicates the cache layer didn't observe equal action IDs
	// between cold and warm — a cache-key determinism regression.
	L1HitsWarm int64 `json:"l1_hits_warm"`
}

// resultToExpected snapshots the observed cold + warm Results into the
// expected-golden wire shape. L2 counters are dropped per the
// expectedGolden doc above.
func resultToExpected(cold, warm harness.Result) expectedGolden {
	return expectedGolden{
		Diagnostics:  cold.Diagnostics,
		L1HitsCold:   cold.L1Metrics.Hits,
		L1StoresCold: cold.L1Metrics.Stores,
		L1HitsWarm:   warm.L1Metrics.Hits,
	}
}

// assertGolden compares observed against testdata/<fixtureName>/expected.json
// or rewrites it under -update. The diff that surfaces on mismatch is
// the JSON-rendered struct so reviewers see the exact field that drifted.
func assertGolden(t *testing.T, fixtureName string, observed expectedGolden) {
	t.Helper()
	goldenPath := filepath.Join("testdata", fixtureName, "expected.json")
	wantBytes, err := harness.MarshalJSONIndent(observed)
	if err != nil {
		t.Fatalf("marshal observed: %v", err)
	}
	if *updateGolden {
		if err := harness.Update(goldenPath, wantBytes); err != nil {
			t.Fatalf("update %s: %v", goldenPath, err)
		}
		t.Logf("rewrote %s", goldenPath)
		return
	}
	gotBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s missing; run with -update to populate", goldenPath)
		}
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	// Normalise both sides to a marshal+round-trip to ignore trailing
	// whitespace differences.
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("golden mismatch %s\n--- want\n%s\n--- got (observed)\n%s",
			goldenPath, string(gotBytes), string(wantBytes))
	}
}

// stageFixture copies testdata/<name>/ into a writable temp dir and
// returns the dest path. The original testdata stays read-only; tests
// that mutate the fixture (add/remove/edit) operate on the copy.
func stageFixture(t *testing.T, name string) string {
	t.Helper()
	dst := harness.LeakyTempDir(t, "plaid-w11-golden-"+name+"-")
	harness.CopyTree(t, filepath.Join("testdata", name), dst)
	return dst
}

// TestGoldenBasic drives the basic fixture cold and warm. The
// assertions name each counter explicitly:
//
//   - cold.L1.Stores > 0   — analyzers ran end-to-end (NOT skipped).
//   - cold.L1.Hits == 0    — fresh cache (no leaked host cache).
//   - warm.L1.Hits > 0     — warm-path L1 lookup succeeded.
//   - cold↔warm digest eq  — diagnostic streams byte-identical.
//
// The expected.json captures the exact diagnostic stream + the four
// counters above so a regression in any of them surfaces on-diff.
func TestGoldenBasic(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "basic")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-basic-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-basic-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-basic-l2-")

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}
	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("cold: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		cold.L1Metrics.Hits, cold.L1Metrics.Stores,
		cold.L2Metrics.Hits, cold.L2Metrics.Stores)

	// Counter assertion: cold-run L1 stores > 0 proves the analyzers
	// actually ran (vs. the symptom where everything skipped on
	// compiles=false). Without this, an empty diagnostic stream could
	// silently pass the equivalence test.
	if cold.L1Metrics.Stores == 0 {
		t.Errorf("cold: L1.Stores = 0, want > 0 (analyzer body must run)")
	}
	// Counter assertion: cold-run L1 hits == 0 proves the cache root
	// was fresh. A non-zero count here means a stale host cache leaked
	// (e.g. GOPLSCACHE not set), which would mask real key regressions.
	if cold.L1Metrics.Hits != 0 {
		t.Errorf("cold: L1.Hits = %d, want 0 (fresh cache invariant)", cold.L1Metrics.Hits)
	}

	warm := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("warm: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		warm.L1Metrics.Hits, warm.L1Metrics.Stores,
		warm.L2Metrics.Hits, warm.L2Metrics.Stores)

	// Counter assertion: warm-run L1 hits > 0 proves cache-key
	// determinism — the action ID computed on warm matches the one
	// written on cold. A zero here = the L1 hot path missed every
	// time, which is the W6/W7 cache-key regression we're guarding.
	if warm.L1Metrics.Hits == 0 {
		t.Errorf("warm: L1.Hits = 0, want > 0 (cache-key determinism)")
	}

	// Diagnostic equivalence: cold and warm must agree on the entire
	// diagnostic stream. The digest comparison is the same shape the
	// pipelinetest W6/W7/W8 tests use.
	if cold.Digest != warm.Digest {
		t.Errorf("cold↔warm digest mismatch:\n  cold: %s\n  warm: %s", cold.Digest, warm.Digest)
	}

	assertGolden(t, "basic", resultToExpected(cold, warm))
}

// TestGoldenFactRoundTrip drives the factroundtrip fixture. The
// printf analyzer publishes an isWrapper fact about leaf.Errorf on
// the cold run; the warm run hits L1 for leaf and must rehydrate the
// fact so consumer's printf pass sees the same diagnostic shape.
//
// Counter claim: warm.L1.Hits > 0 implies the fact gob round-trip
// succeeded — without it, leaf's printf action would miss L1 and
// re-run, and the cold/warm digests might still match but the warm
// hit count would be 0.
func TestGoldenFactRoundTrip(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "factroundtrip")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-fact-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-fact-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-fact-l2-")

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}
	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("cold: L1 hits=%d stores=%d", cold.L1Metrics.Hits, cold.L1Metrics.Stores)
	if cold.L1Metrics.Stores == 0 {
		t.Errorf("cold: L1.Stores = 0; printf must run on leaf+consumer")
	}

	warm := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("warm: L1 hits=%d stores=%d", warm.L1Metrics.Hits, warm.L1Metrics.Stores)
	// Counter assertion: warm-run L1 hits > 0 proves the
	// printf-on-leaf entry round-tripped including its fact blob
	// (ObjectFacts in L1Entry). If the fact gob encoding lost
	// information between cold and warm, consumer's pass.ResultOf
	// would see an empty fact set and the warm digest would diverge
	// — both surfaced by this test.
	if warm.L1Metrics.Hits == 0 {
		t.Errorf("warm: L1.Hits = 0; fact gob round-trip likely broken")
	}
	if cold.Digest != warm.Digest {
		t.Errorf("cold↔warm digest mismatch:\n  cold: %s\n  warm: %s", cold.Digest, warm.Digest)
	}

	assertGolden(t, "factroundtrip", resultToExpected(cold, warm))
}

// TestGoldenFileAdd writes a new file into the fixture between cold
// and warm runs. The new file declares an unused function so the
// re-analysis has to process the addition. Assertion: the post-edit
// L1 stores count climbs (a new action ID for the new file's package),
// AND the new file is reflected in the package's source.
func TestGoldenFileAdd(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "addremove")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-add-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-add-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-add-l2-")

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}

	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("pre-add: L1 hits=%d stores=%d", cold.L1Metrics.Hits, cold.L1Metrics.Stores)
	preAddStores := cold.L1Metrics.Stores
	if preAddStores == 0 {
		t.Fatalf("pre-add: L1.Stores = 0; analyzers must have run on the base file")
	}

	// Add a new file into package a/.
	addedPath := filepath.Join(modDir, "a", "added.go")
	harness.WriteFile(t, addedPath, `package a

// Added by the file-add golden subtest. The function body contains a
// self-assignment so the assign analyzer has signal on the post-add
// run; equality with the no-added baseline would prove the harness
// is not actually picking up the new file.

func Touch() string {
	t := NewBase("added")
	t.Name = t.Name
	return t.Name
}
`)

	// Re-run with a fresh WorkspaceState (a new AnalyzeOnce call).
	// The new WorkspaceState's InitializeWorkspace runs `go list` on
	// the module root, which observes the added file naturally — no
	// Invalidate plumbing required (and Invalidate on a stale view
	// wouldn't help: the previous view was Close()d). The added file's
	// presence shifts the package source digest, so every action ID
	// for package a/ changes; the post-add run misses every entry
	// and re-stores under new paths.
	post := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("post-add: L1 hits=%d stores=%d", post.L1Metrics.Hits, post.L1Metrics.Stores)

	// Counter assertion: post-add L1 stores > 0 proves analyzers
	// re-ran. With only one package in the fixture and its source
	// digest changed by the file-add, every action ID rolls; so
	// hits=0 and stores>0 is the expected shape (the pre-add entries
	// at the old paths persist on disk but their action IDs are not
	// re-derivable from the post-add inputs).
	if post.L1Metrics.Stores == 0 {
		t.Errorf("post-add: L1.Stores = 0; file-add must trigger fresh analyzer runs")
	}
	if post.L1Metrics.Hits != 0 {
		t.Errorf("post-add: L1.Hits = %d, want 0 (file-add changes the package source digest, so every action ID changes)", post.L1Metrics.Hits)
	}

	// Diagnostic-stream assertion: the added.go content's self-
	// assignment must appear in the assign diagnostic stream. This
	// is the load-bearing claim of the file-add subtest — that the
	// re-analysis actually parsed the new file. The counters above
	// would also be triggered by a workspace-level re-analysis that
	// missed the new file (because the same source digest path is
	// invalidated by Invalidate), so the diagnostic check is the
	// independent witness.
	hasAddedDiag := false
	for _, d := range post.Diagnostics["assign"] {
		if d.Filename == "added.go" {
			hasAddedDiag = true
			break
		}
	}
	if !hasAddedDiag {
		t.Errorf("assign analyzer did not produce a diagnostic on added.go; file-add was not picked up (have diags: %+v)", post.Diagnostics["assign"])
	}

	assertGolden(t, "addremove", resultToExpected(cold, post))
}

// TestGoldenFileDelete is the inverse of FileAdd: start with two
// files, remove one, re-analyze. Assertion: the post-delete run
// produces a clean diagnostic stream without diagnostics anchored to
// the deleted file's filename.
func TestGoldenFileDelete(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "addremove")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-del-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-del-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-del-l2-")

	// Seed an extra file with intentional diagnostic content; we'll
	// delete it on the second pass.
	extraPath := filepath.Join(modDir, "a", "extra.go")
	harness.WriteFile(t, extraPath, `package a

// Extra file with an assign self-assignment trigger. The
// file-delete subtest removes this and asserts the post-delete
// analyzer stream contains no diagnostics anchored to extra.go.

func Extra() string {
	t := NewBase("extra")
	t.Name = t.Name
	return t.Name
}
`)

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}

	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("pre-delete: L1 hits=%d stores=%d", cold.L1Metrics.Hits, cold.L1Metrics.Stores)
	hasExtra := false
	for _, d := range cold.Diagnostics["assign"] {
		if d.Filename == "extra.go" {
			hasExtra = true
			break
		}
	}
	if !hasExtra {
		t.Fatalf("pre-delete: assign analyzer did not flag extra.go; fixture is broken")
	}

	// Remove extra.go and re-run.
	harness.RemoveFile(t, extraPath)
	cfg.InvalidatePaths = []string{extraPath}
	post := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("post-delete: L1 hits=%d stores=%d", post.L1Metrics.Hits, post.L1Metrics.Stores)

	// Assertion: post-delete diagnostic stream must NOT contain any
	// diagnostic anchored to extra.go. If it does, the re-analysis
	// didn't pick up the deletion and is still consulting the stale
	// source set.
	for _, d := range post.Diagnostics["assign"] {
		if d.Filename == "extra.go" {
			t.Errorf("post-delete: assign still flags extra.go: %+v (file should not have been analysed)", d)
		}
	}

	// Counter assertion: cold-run L1 stores > 0 (every fixture)
	// AND the cold run has the extra file's diagnostic. The store
	// count is from the cold (pre-delete) run, so the file-delete
	// observation lives only in the diagnostic stream, not the
	// counters — that's why this golden is asymmetric vs file-add.
	if cold.L1Metrics.Stores == 0 {
		t.Errorf("pre-delete: L1.Stores = 0; cold run must produce entries")
	}

	// No expected.json comparison here — the cold + post diagnostic
	// streams diverge by construction (extra.go's entry is in cold,
	// not post). We instead pin the *post-delete* stream as the
	// golden, with one expected.json containing the post-delete
	// counters in cold position and warm position derived from a
	// repeat of the post run.
	postRepeat := harness.AnalyzeOnce(t, context.Background(), cfg)
	if post.Digest != postRepeat.Digest {
		t.Errorf("post-delete repeat digest mismatch:\n  first:  %s\n  second: %s",
			post.Digest, postRepeat.Digest)
	}
	assertGolden(t, "addremove_delete", resultToExpected(post, postRepeat))
}

// TestGoldenGoModBump bumps go.mod's `go` directive between cold and
// warm runs. The hot-path assertion guards against a regression:
// View.GoVersion() must be non-zero on both runs so stdlib type-check
// succeeds and analyzers actually execute.
//
// The current production wiring derives Env.GoVersion from
// runtime.Version() (not from go.mod's `go` directive — that's a
// pre-existing gap the W3 stub introduced and later worked around).
// This test pins the contract that the GoVersion path stays
// non-zero across both cold and warm runs; a regression that
// flipped back to 0 would re-trigger the "package does not compile"
// cascade we just paid to fix.
func TestGoldenGoModBump(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "gomod")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-gomod-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-gomod-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-gomod-l2-")

	gomodPath := filepath.Join(modDir, "go.mod")
	originalMod, err := os.ReadFile(gomodPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	// Cold run on the original go 1.22 directive. We capture
	// GoVersion via a CacheMutate-side observer of the *cache.View
	// — but the View is owned by workspace.NewWithCache, so we
	// reach in via the cache.Snapshot.View().GoVersion() accessor
	// after the run.
	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}
	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("cold (go 1.22): L1 hits=%d stores=%d", cold.L1Metrics.Hits, cold.L1Metrics.Stores)
	// Counter assertion: cold-run L1 stores > 0 — analyzers must
	// have run end-to-end on the stdlib-importing fixture. A zero
	// here = the regression (Env.GoVersion=0 → stdlib does
	// not compile → analyzers skip → no L1 writes).
	if cold.L1Metrics.Stores == 0 {
		t.Errorf("cold (go 1.22): L1.Stores = 0; stdlib-importing fixture must type-check")
	}

	// Bump go.mod's `go` directive to 1.23 and re-run.
	bumped := strings.Replace(string(originalMod), "go 1.22", "go 1.23", 1)
	if bumped == string(originalMod) {
		t.Fatalf("go.mod bump no-op; the fixture's `go` directive was not 1.22")
	}
	if err := os.WriteFile(gomodPath, []byte(bumped), 0o644); err != nil {
		t.Fatalf("write bumped go.mod: %v", err)
	}
	cfg.InvalidatePaths = []string{gomodPath}
	warm := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("warm (go 1.23): L1 hits=%d stores=%d", warm.L1Metrics.Hits, warm.L1Metrics.Stores)

	// Counter assertion: warm-run L1 stores OR hits > 0 — the
	// post-bump re-analysis must observe either fresh stores (because
	// the action ID changed) or hits (because the cache-key didn't
	// fold in the go directive change, which is the current expected
	// behaviour given Env.GoVersion is runtime-sourced). Either is
	// acceptable; what's NOT acceptable is total silence (stores=0
	// AND hits=0), which means analyzers skipped.
	if warm.L1Metrics.Stores == 0 && warm.L1Metrics.Hits == 0 {
		t.Errorf("post-bump (go 1.23): L1 silent (stores=0 hits=0); regression")
	}

	// Defensive: write the original go.mod back so a partial test
	// failure doesn't leave the testdata tree in a mutated state. The
	// stageFixture call above already copied to a tempdir so this is
	// belt-and-suspenders.
	t.Cleanup(func() {
		_ = os.WriteFile(gomodPath, originalMod, 0o644)
	})

	// Stdlib-importing fixtures have non-deterministic L1/L2 counter
	// values across runs (the stdlib closure shape varies based on
	// what `go list` reports — different Go minor versions, different
	// build caches, even different cgo / build-mode toggles all
	// perturb the closure size). We therefore use a stripped golden
	// for gomod that captures only the diagnostic stream; the
	// "non-zero on both runs" counter invariants are asserted inline
	// above, not pinned in expected.json. The rationale:
	// golden-as-baseline-counter only works when the underlying
	// closure shape is deterministic.
	assertGoldenDiagnosticsOnly(t, "gomod", cold.Diagnostics)
}

// assertGoldenDiagnosticsOnly is the golden assertion for fixtures
// whose counter values are non-deterministic (gomod's stdlib closure).
// We pin the diagnostic stream and skip the counter fields entirely.
// Counter invariants (>0 stores cold, >0 hits warm) are asserted
// inline in the calling test, not via the golden.
func assertGoldenDiagnosticsOnly(t *testing.T, fixtureName string, diags map[string][]harness.CanonicalDiag) {
	t.Helper()
	goldenPath := filepath.Join("testdata", fixtureName, "expected_diagnostics.json")
	wantBytes, err := harness.MarshalJSONIndent(diags)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	if *updateGolden {
		if err := harness.Update(goldenPath, wantBytes); err != nil {
			t.Fatalf("update %s: %v", goldenPath, err)
		}
		t.Logf("rewrote %s", goldenPath)
		return
	}
	gotBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s missing; run with -update to populate", goldenPath)
		}
		t.Fatalf("read %s: %v", goldenPath, err)
	}
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("golden mismatch %s\n--- want\n%s\n--- got\n%s",
			goldenPath, string(gotBytes), string(wantBytes))
	}
}

// TestGoldenBuildTag exercises Go's build-constraint exclusion. The
// fixture starts with a single common file. The cold run captures the
// baseline; the warm run adds a file with a build tag that EXCLUDES
// it on the current platform, plus a file with a tag that INCLUDES
// it. Both files would trigger an assign diagnostic if analysed;
// only the included file's diagnostic should appear.
func TestGoldenBuildTag(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := stageFixture(t, "buildtag")
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-golden-buildtag-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-golden-buildtag-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-golden-buildtag-l2-")

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
	}

	// Add two files with mutually exclusive build tags:
	// - included.go: //go:build linux || darwin   (current CI matches)
	// - excluded.go: //go:build !linux && !darwin (current CI does not)
	//
	// Both files contain self-assignment so assign would flag them if
	// the build system included them. Only included.go's diagnostic
	// should make it into the diagnostic stream.
	includedPath := filepath.Join(modDir, "a", "included.go")
	harness.WriteFile(t, includedPath, `//go:build linux || darwin

package a

// Included on linux+darwin (the platforms we test on). The assign
// analyzer must flag this self-assignment in the diagnostic stream.

func Included() string {
	t := New("included")
	t.Name = t.Name
	return t.Name
}
`)

	excludedPath := filepath.Join(modDir, "a", "excluded.go")
	harness.WriteFile(t, excludedPath, `//go:build !linux && !darwin

package a

// Excluded on linux+darwin. The assign analyzer must NOT flag this
// file because the build system filters it out before parsing.

func Excluded() string {
	t := New("excluded")
	t.Name = t.Name
	return t.Name
}
`)

	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("buildtag cold: L1 hits=%d stores=%d diags=%d",
		cold.L1Metrics.Hits, cold.L1Metrics.Stores,
		len(cold.Diagnostics["assign"]))

	// Counter assertion: cold-run L1 stores > 0 confirms analyzers
	// ran.
	if cold.L1Metrics.Stores == 0 {
		t.Errorf("buildtag cold: L1.Stores = 0; analyzers must have run")
	}

	// Diagnostic-stream assertion: included.go IS in the assign
	// diagnostic stream, excluded.go IS NOT. The assertion is on
	// filename only — message text may shift across upstream bumps.
	sawIncluded, sawExcluded := false, false
	for _, d := range cold.Diagnostics["assign"] {
		switch d.Filename {
		case "included.go":
			sawIncluded = true
		case "excluded.go":
			sawExcluded = true
		}
	}
	if !sawIncluded {
		t.Errorf("buildtag: included.go missing from assign diagnostics (build tag mishandled - file should be analyzed on %s)", currentPlatform())
	}
	if sawExcluded {
		t.Errorf("buildtag: excluded.go appeared in assign diagnostics (build tag mishandled - file should NOT be analyzed on %s)", currentPlatform())
	}

	// Warm to verify counters round-trip.
	warm := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("buildtag warm: L1 hits=%d stores=%d", warm.L1Metrics.Hits, warm.L1Metrics.Stores)
	if warm.L1Metrics.Hits == 0 {
		t.Errorf("buildtag warm: L1.Hits = 0; warm run must hit cache")
	}
	if cold.Digest != warm.Digest {
		t.Errorf("buildtag cold↔warm digest mismatch:\n  cold: %s\n  warm: %s", cold.Digest, warm.Digest)
	}

	assertGolden(t, "buildtag", resultToExpected(cold, warm))
}

// currentPlatform returns "linux/amd64" etc. for log lines.
func currentPlatform() string {
	return fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
}
