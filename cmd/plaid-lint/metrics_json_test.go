// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readMetricsFile parses a metrics JSON file written by the
// `--metrics-json` flag. Returns the raw object map so individual
// tests assert on whatever subset they care about.
func readMetricsFile(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode metrics %q: %v", body, err)
	}
	return m
}

// TestRun_MetricsJSON_RoundTrip drives the production CLI against a
// small workspace with --metrics-json and asserts the resulting
// file parses and the layer counters are non-negative. Exercises
// the engine → CacheMetrics → JSON pipeline end-to-end.
func TestRun_MetricsJSON_RoundTrip(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	out := filepath.Join(t.TempDir(), "m.json")
	_, _, stderr := runApp(t, dir, "run", "--metrics-json", out)

	m := readMetricsFile(t, out)
	if v, _ := m["schema_version"].(float64); int(v) != metricsJSONSchemaVersion {
		t.Errorf("schema_version=%v want %d (stderr=%q)", m["schema_version"], metricsJSONSchemaVersion, stderr)
	}
	if v, _ := m["plaid_version"].(string); v == "" {
		t.Errorf("plaid_version is empty")
	}
	if v, _ := m["wall_seconds"].(float64); v < 0 {
		t.Errorf("wall_seconds=%v should be non-negative", v)
	}
	for _, layer := range []string{"l1", "l2"} {
		sub, ok := m[layer].(map[string]any)
		if !ok {
			t.Errorf("layer %q missing", layer)
			continue
		}
		for _, k := range []string{"hits", "misses", "stores", "skipped", "errors"} {
			v, _ := sub[k].(float64)
			if v < 0 {
				t.Errorf("%s.%s=%v should be non-negative", layer, k, v)
			}
		}
	}
	l0, ok := m["l0"].(map[string]any)
	if !ok {
		t.Fatalf("l0 missing or null; stderr=%q", stderr)
	}
	for _, k := range []string{"hits", "misses", "stores", "errors", "skipped_pkgs", "dep_hits", "dep_writes", "override_hits"} {
		v, _ := l0[k].(float64)
		if v < 0 {
			t.Errorf("l0.%s=%v should be non-negative", k, v)
		}
	}
}

// TestRun_MetricsJSON_SchemaStability asserts the wire shape stays
// stable: every key the bench runner depends on is present, even on
// a workspace that produces zero diagnostics.
func TestRun_MetricsJSON_SchemaStability(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	out := filepath.Join(t.TempDir(), "metrics", "run.json")
	runApp(t, dir, "run", "--metrics-json", out)

	m := readMetricsFile(t, out)
	for _, k := range []string{"schema_version", "plaid_version", "wall_seconds", "l0", "l1", "l2", "diagnostics"} {
		if _, ok := m[k]; !ok {
			t.Errorf("top-level key %q missing", k)
		}
	}
	l1, _ := m["l1"].(map[string]any)
	for _, k := range []string{"hits", "misses", "stores", "skipped", "errors", "by_scope"} {
		if _, ok := l1[k]; !ok {
			t.Errorf("l1.%s missing", k)
		}
	}
	scope, _ := l1["by_scope"].(map[string]any)
	for _, k := range []string{
		"syntax_only_hits",
		"syntax_only_misses",
		"exported_types_only_hits",
		"full_type_graph_hits",
		"full_type_graph_misses",
	} {
		if _, ok := scope[k]; !ok {
			t.Errorf("l1.by_scope.%s missing", k)
		}
	}
	l2, _ := m["l2"].(map[string]any)
	for _, k := range []string{"hits", "misses", "stores", "skipped", "errors"} {
		if _, ok := l2[k]; !ok {
			t.Errorf("l2.%s missing", k)
		}
	}
	l0, _ := m["l0"].(map[string]any)
	for _, k := range []string{"hits", "misses", "stores", "errors", "skipped_pkgs", "dep_hits", "dep_writes", "override_hits"} {
		if _, ok := l0[k]; !ok {
			t.Errorf("l0.%s missing", k)
		}
	}
	diag, _ := m["diagnostics"].(map[string]any)
	if _, ok := diag["count"]; !ok {
		t.Errorf("diagnostics.count missing")
	}
	sev, _ := diag["by_severity"].(map[string]any)
	for _, k := range []string{"error", "warning", "info"} {
		if _, ok := sev[k]; !ok {
			t.Errorf("diagnostics.by_severity.%s missing", k)
		}
	}
}

// TestRun_MetricsJSON_SyntaxOnlyScope confirms that the per-scope
// L1 counters reflect real cache traffic when a SyntaxOnly analyzer
// (ineffassign per the W6 cascade work) is enabled. The
// warm pass after a cold one should record at least one syntax-only
// hit — exactly what the bench runner needs to observe.
func TestRun_MetricsJSON_SyntaxOnlyScope(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	src := `package main

func main() {
	x := 1
	x = 2
	_ = x
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	// Share the L1/L2 cache between cold and warm passes.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// Disable L0 so the warm pass must consult L1 rather than
	// short-circuiting on the per-package L0 entry. This is the
	// same lever the W6 bench harness uses to isolate L1 traffic.
	t.Setenv("PLAID_DISABLE_L0_CACHE", "1")

	// Cold pass: populates L1.
	coldOut := filepath.Join(t.TempDir(), "cold.json")
	runApp(t, dir, "run", "--metrics-json", coldOut)

	// Warm pass: should hit L1 (and surface syntax_only_hits >= 1
	// because the engine treats ineffassign as a SyntaxOnly analyzer).
	warmOut := filepath.Join(t.TempDir(), "warm.json")
	runApp(t, dir, "run", "--metrics-json", warmOut)

	m := readMetricsFile(t, warmOut)
	l1, _ := m["l1"].(map[string]any)
	scope, _ := l1["by_scope"].(map[string]any)
	v, _ := scope["syntax_only_hits"].(float64)
	if v < 1 {
		t.Errorf("warm pass: syntax_only_hits=%v want >= 1; full warm json=%v", v, m)
	}
}
