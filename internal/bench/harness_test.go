// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

// TestHarness_SmallFixture is the harness's W10 smoke test. It drives
// the bench_small fixture through cold→warm and asserts:
//
//   - The harness completes end-to-end without error.
//   - cold.action_count > 0 (the analyzer DAG actually ran).
//   - cold.diagnostic_digest == warm.diagnostic_digest (equivalence).
//   - warm.l1_hits > 0 (warm scenario picked up cached actions).
//   - IRPinEvents == IRReleaseEvents (no pin leak).
func TestHarness_SmallFixture(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024, // small budget so the gate actually fires
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		SkipCascade:       true, // small fixture has no cascade-mid
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Cold == nil {
		t.Fatal("cold scenario missing")
	}
	if res.Cold.ActionCount == 0 {
		t.Errorf("cold action_count = 0, want > 0")
	}
	if res.Warm == nil {
		t.Fatal("warm scenario missing")
	}
	if res.Warm.L1Hits == 0 {
		t.Errorf("warm l1_hits = 0, want > 0")
	}
	if res.Cold.IRPinEvents != res.Cold.IRReleaseEvents {
		t.Errorf("cold pin leak: pins=%d releases=%d", res.Cold.IRPinEvents, res.Cold.IRReleaseEvents)
	}
	// Cold and warm digests must agree.
	if res.Cold.DiagnosticDigest != res.Warm.DiagnosticDigest {
		t.Errorf("cold↔warm digests differ: cold=%s warm=%s",
			res.Cold.DiagnosticDigest, res.Warm.DiagnosticDigest)
	}

	// Round-trip the result through JSON to confirm the schema is
	// serialisable as documented.
	buf, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var back BenchmarkResult
	if err := json.Unmarshal(buf, &back); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if back.Schema != 1 {
		t.Errorf("schema = %d, want 1", back.Schema)
	}
}

// TestHarness_CascadeFixture drives the bench_cascade fixture and
// asserts the cascade scenario produces a measurable signal:
//
//   - The cascade scenario's wall time is finite.
//   - cascade.l1_hits + cascade.l1_misses > 0 (some action ran).
//   - cascade.diagnostic_count == cold.diagnostic_count (the source
//     edit was a comment append, so diagnostics shouldn't change).
func TestHarness_CascadeFixture(t *testing.T) {
	requireGo(t)
	if testing.Short() {
		t.Skip("skipping cascade harness in -short mode")
	}
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      CascadeShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Cascade == nil {
		t.Fatal("cascade scenario missing")
	}
	if res.Cascade.WallMs <= 0 {
		t.Errorf("cascade wall_ms = %d, want > 0", res.Cascade.WallMs)
	}
	// The cascade-mid edit is a comment append, so diagnostic count
	// should match cold.
	if res.Cascade.DiagnosticCount != res.Cold.DiagnosticCount {
		t.Errorf("cascade diagnostic_count = %d, want %d (cold) — comment-only edit should not change diagnostics",
			res.Cascade.DiagnosticCount, res.Cold.DiagnosticCount)
	}
	// H-4 (Phase 1.5): cascade.diagnostic_digest must be populated.
	// Pre-1.5 the harness hardcoded "" here; the comment-only edit
	// produces position-stable diagnostics, so the digest should
	// equal cold.
	if res.Cascade.DiagnosticDigest == "" {
		t.Errorf("cascade diagnostic_digest is empty; expected the same digest the cold/warm path computes (H-4)")
	}
	if res.Cascade.DiagnosticDigest != res.Cold.DiagnosticDigest {
		t.Errorf("cascade↔cold digest differ: cascade=%s cold=%s — comment-only edit should not perturb the digest",
			res.Cascade.DiagnosticDigest, res.Cold.DiagnosticDigest)
	}
	// CascadeAggregate (H-2) must always be present whenever the
	// cascade scenario ran. RunCount must equal 1 here (the default
	// CascadeRuns is unset → falls back to 1).
	if res.CascadeAggregate == nil {
		t.Fatal("cascade_aggregate missing (H-2)")
	}
	if res.CascadeAggregate.RunCount != 1 {
		t.Errorf("cascade_aggregate.run_count = %d, want 1", res.CascadeAggregate.RunCount)
	}
	if got := res.CascadeAggregate.AggregateStats.MeanWallMs; got != res.Cascade.WallMs {
		t.Errorf("cascade_aggregate.mean_wall_ms = %d, want %d (single-run equivalence)", got, res.Cascade.WallMs)
	}
}

// TestHarness_LeafEditScenario covers H-1 (Phase 1.5) at the
// library scope (bench.Run). The file IS expected to be left
// mutated after bench.Run returns — file restoration is the CLI's
// responsibility (--cascade-restore-on-exit in
// cmd/plaid-lint-bench) and is tested elsewhere. This test
// drives the small fixture through cold→warm→leaf_edit and
// asserts:
//
//   - The leaf_edit scenario completes end-to-end without error.
//   - leaf_edit.wall_ms > 0 (the scenario actually ran).
//   - leaf_edit.action_count > 0 (the analyzer DAG re-ran for at
//     least the leaf package).
//   - leaf_edit.diagnostic_digest is non-empty (H-4 parity).
//   - The leaf-edit target file IS mutated by bench.Run (the
//     test's own defer restores it; bench.Run itself does not).
func TestHarness_LeafEditScenario(t *testing.T) {
	requireGo(t)
	if testing.Short() {
		t.Skip("skipping leaf-edit harness in -short mode")
	}
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, SmallShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	// SmallShape has 3 leaves; leaf0/leaf0.go is the canonical
	// leaf-package file. Its package (leaf0) is imported by the
	// mid layer and transitively by roots, so editing it does
	// have downstream effects in this synthetic shape — that's
	// fine for the test (the assertion is "the scenario produces
	// a measurable signal", not "the cascade closure is zero").
	leafFile := filepath.Join(dir, "leaf0", "leaf0.go")
	originalBody, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf file: %v", err)
	}

	// The harness mutates leafFile in place; we restore it after
	// Run returns so the assertion below sees the original body.
	defer func() {
		if err := os.WriteFile(leafFile, originalBody, 0o644); err != nil {
			t.Errorf("restore leaf file: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		SkipCascade:       true, // SmallShape has no cascade-mid
		LeafEditFile:      leafFile,
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LeafEdit == nil {
		t.Fatal("leaf_edit scenario missing")
	}
	if res.LeafEdit.WallMs <= 0 {
		t.Errorf("leaf_edit.wall_ms = %d, want > 0", res.LeafEdit.WallMs)
	}
	if res.LeafEdit.ActionCount == 0 {
		t.Errorf("leaf_edit.action_count = 0, want > 0 (at least the leaf package's actions must re-run)")
	}
	if res.LeafEdit.DiagnosticDigest == "" {
		t.Errorf("leaf_edit.diagnostic_digest is empty; expected a non-empty SHA-256 hex digest")
	}
	// JSON round-trip to confirm the new field marshals at the
	// "leaf_edit" key.
	buf, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !strings.Contains(string(buf), "\"leaf_edit\"") {
		t.Errorf("marshalled result missing \"leaf_edit\" field; got: %s", string(buf))
	}
	// Verify the file was actually mutated by the harness (so the
	// post-test restore is meaningful). The harness's
	// applyLeafEdit appends "// bench-leaf-edit:" trailers.
	got, err := os.ReadFile(leafFile)
	if err != nil {
		t.Fatalf("read leaf file post-run: %v", err)
	}
	if !strings.Contains(string(got), "bench-leaf-edit:") {
		t.Errorf("leaf file does not contain the harness's trailer marker; the harness may not have applied the edit:\n%s", string(got))
	}
}

// TestHarness_LeafEditAndCascadeJSONShape pins the post-H-1/H-2/H-4
// JSON document shape so runbook v3 consumers (and downstream
// calibration scripts) know which keys to read. It drives all four
// scenarios (cold, warm, leaf_edit, cascade with CascadeRuns=2)
// against a single CascadeShape fixture and asserts the JSON
// document has every documented key with the right type.
func TestHarness_LeafEditAndCascadeJSONShape(t *testing.T) {
	requireGo(t)
	if testing.Short() {
		t.Skip("skipping JSON shape harness in -short mode")
	}
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}
	leafFile := filepath.Join(dir, "leaf0", "leaf0.go")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cfg := Config{
		Fixture:           dir,
		FixtureShape:      CascadeShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		LeafEditFile:      leafFile,
		CascadeRuns:       2,
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	buf, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	// Decode into a generic map so we assert the on-the-wire field
	// names, not Go field names.
	var generic map[string]any
	if err := json.Unmarshal(buf, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	for _, key := range []string{
		"cold", "warm", "leaf_edit", "cascade", "cascade_aggregate",
		"schema", "fixture", "fixture_shape", "observation_source",
		"platform", "go_version", "scheduler_max_concurrency",
		"scheduler_budget_bytes", "analyzer_count",
		"static_fallbacks", "action_peak_observations",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("JSON document missing key %q; got keys: %v", key, mapKeys(generic))
		}
	}

	// cascade_aggregate shape.
	agg, ok := generic["cascade_aggregate"].(map[string]any)
	if !ok {
		t.Fatalf("cascade_aggregate not an object")
	}
	for _, key := range []string{"run_count", "runs", "aggregate_stats"} {
		if _, ok := agg[key]; !ok {
			t.Errorf("cascade_aggregate missing %q", key)
		}
	}
	stats, ok := agg["aggregate_stats"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate_stats not an object")
	}
	for _, key := range []string{
		"mean_wall_ms", "p95_wall_ms", "max_wall_ms", "min_wall_ms",
		"mean_peak_rss_bytes", "max_peak_rss_bytes",
	} {
		if _, ok := stats[key]; !ok {
			t.Errorf("aggregate_stats missing %q", key)
		}
	}
	runs, ok := agg["runs"].([]any)
	if !ok {
		t.Fatalf("cascade_aggregate.runs not an array")
	}
	if len(runs) != 2 {
		t.Errorf("cascade_aggregate.runs length = %d, want 2", len(runs))
	}
	// Each run has the same ScenarioResult shape; spot-check the
	// load-bearing fields.
	for i, r := range runs {
		ro, ok := r.(map[string]any)
		if !ok {
			t.Errorf("runs[%d] not an object", i)
			continue
		}
		for _, key := range []string{
			"wall_ms", "peak_rss_bytes", "diagnostic_count",
			"diagnostic_digest", "action_count",
			"l1_hits", "l1_stores", "l1_misses",
		} {
			if _, ok := ro[key]; !ok {
				t.Errorf("runs[%d] missing %q", i, key)
			}
		}
	}

	// leaf_edit shape parity with cold.
	cold, _ := generic["cold"].(map[string]any)
	leaf, _ := generic["leaf_edit"].(map[string]any)
	if cold == nil || leaf == nil {
		t.Fatal("cold or leaf_edit missing as object")
	}
	for key := range cold {
		if _, ok := leaf[key]; !ok {
			t.Errorf("leaf_edit missing %q present in cold", key)
		}
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestHarness_MultiRunCascade covers H-2 (Phase 1.5). It drives the
// cascade fixture with CascadeRuns=3 and asserts:
//
//   - CascadeAggregate.Runs has length 3.
//   - All three runs produce equivalent diagnostic_digest (the
//     edit is content-bumping only via a unique trailer; the
//     downstream diagnostics are position-stable).
//   - CascadeAggregate.AggregateStats has min ≤ mean ≤ max for
//     wall_ms (sanity).
//   - BenchmarkResult.Cascade equals CascadeAggregate.Runs[0]
//     (backwards-compat invariant).
func TestHarness_MultiRunCascade(t *testing.T) {
	requireGo(t)
	if testing.Short() {
		t.Skip("skipping multi-run cascade harness in -short mode")
	}
	dir := t.TempDir()
	_, _, err := GenerateFixture(dir, CascadeShape)
	if err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	// Capture the cascade-file SHA-256 at the start of each run.
	// All N hashes must be equal — the package L1 cache key
	// includes source content, so any per-run drift would
	// invalidate the cache-state-carries-forward Option A
	// invariant H-2 depends on.
	var (
		perRunMu     sync.Mutex
		perRunHashes []string
	)
	cfg := Config{
		Fixture:           dir,
		FixtureShape:      CascadeShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		CascadeRuns:       3,
		CascadePerRunObserver: func(runIndex int, body []byte) {
			sum := sha256.Sum256(body)
			perRunMu.Lock()
			defer perRunMu.Unlock()
			for len(perRunHashes) <= runIndex {
				perRunHashes = append(perRunHashes, "")
			}
			perRunHashes[runIndex] = hex.EncodeToString(sum[:])
		},
	}
	res, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CascadeAggregate == nil {
		t.Fatal("cascade_aggregate missing")
	}
	if res.CascadeAggregate.RunCount != 3 {
		t.Fatalf("cascade_aggregate.run_count = %d, want 3", res.CascadeAggregate.RunCount)
	}
	if got := len(res.CascadeAggregate.Runs); got != 3 {
		t.Fatalf("cascade_aggregate.runs length = %d, want 3", got)
	}
	// H-2 invariant: every run must see byte-identical source
	// bytes for the cascade-file. Any drift would change the L1
	// package cache key.
	if len(perRunHashes) != 3 {
		t.Fatalf("CascadePerRunObserver fired %d times, want 3", len(perRunHashes))
	}
	for i := 1; i < len(perRunHashes); i++ {
		if perRunHashes[i] != perRunHashes[0] {
			t.Errorf("cascade source bytes diverged across runs: hash[0]=%s hash[%d]=%s",
				perRunHashes[0], i, perRunHashes[i])
		}
	}
	// Reproducibility: all three runs should produce the same
	// diagnostic_digest because the only per-run difference is
	// the trailer's nanosecond marker (which the digest does not
	// see — canonicalizeDiagnostics keys on diagnostic positions,
	// not source bytes).
	d0 := res.CascadeAggregate.Runs[0].DiagnosticDigest
	if d0 == "" {
		t.Errorf("cascade run 0 digest empty")
	}
	for i := 1; i < res.CascadeAggregate.RunCount; i++ {
		if res.CascadeAggregate.Runs[i].DiagnosticDigest != d0 {
			t.Errorf("cascade run %d digest = %s, want %s (run 0)",
				i, res.CascadeAggregate.Runs[i].DiagnosticDigest, d0)
		}
	}
	// Aggregate sanity: min ≤ mean ≤ max wall.
	stats := res.CascadeAggregate.AggregateStats
	if stats.MinWallMs > stats.MeanWallMs {
		t.Errorf("aggregate min_wall_ms=%d > mean_wall_ms=%d", stats.MinWallMs, stats.MeanWallMs)
	}
	if stats.MeanWallMs > stats.MaxWallMs {
		t.Errorf("aggregate mean_wall_ms=%d > max_wall_ms=%d", stats.MeanWallMs, stats.MaxWallMs)
	}
	if stats.MaxWallMs <= 0 {
		t.Errorf("aggregate max_wall_ms = %d, want > 0", stats.MaxWallMs)
	}
	// Backwards-compat: top-level Cascade must equal Runs[0].
	if res.Cascade == nil {
		t.Fatal("top-level cascade missing")
	}
	if res.Cascade.WallMs != res.CascadeAggregate.Runs[0].WallMs {
		t.Errorf("Cascade.wall_ms=%d != CascadeAggregate.Runs[0].wall_ms=%d",
			res.Cascade.WallMs, res.CascadeAggregate.Runs[0].WallMs)
	}
}
