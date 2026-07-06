// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/output"
)

// metricsJSONSchemaVersion identifies the shape of the metrics JSON
// payload. Bump whenever a field is removed or renamed so consumers
// (the bench runner) can refuse mismatched files.
const metricsJSONSchemaVersion = 1

// metricsJSON is the wire shape written to --metrics-json. Fields
// mirror the engine's CacheMetrics plus a top-level diagnostics
// rollup and a wall-clock seconds value the runner uses for
// throughput math.
type metricsJSON struct {
	SchemaVersion  int                `json:"schema_version"`
	PlaidVersion string             `json:"plaid_version"`
	WallSeconds    float64            `json:"wall_seconds"`
	L0             *l0MetricsJSON     `json:"l0"`
	L1             l1MetricsJSON      `json:"l1"`
	L2             l2MetricsJSON      `json:"l2"`
	Diagnostics    diagnosticsSummary `json:"diagnostics"`
}

type l0MetricsJSON struct {
	Hits         int64 `json:"hits"`
	Misses       int64 `json:"misses"`
	Stores       int64 `json:"stores"`
	Errors       int64 `json:"errors"`
	SkippedPkgs  int64 `json:"skipped_pkgs"`
	DepHits      int64 `json:"dep_hits"`
	DepWrites    int64 `json:"dep_writes"`
	OverrideHits int64 `json:"override_hits"`
}

type l1MetricsJSON struct {
	Hits    int64                `json:"hits"`
	Misses  int64                `json:"misses"`
	Stores  int64                `json:"stores"`
	Skipped int64                `json:"skipped"`
	Errors  int64                `json:"errors"`
	ByScope l1MetricsJSONByScope `json:"by_scope"`
}

// l1MetricsJSONByScope carries the per-scope counters.
// Field names use *_hits to leave room for matching *_misses fields
// without a schema bump, since the underlying L1Metrics already
// tracks both.
type l1MetricsJSONByScope struct {
	SyntaxOnlyHits        int64 `json:"syntax_only_hits"`
	SyntaxOnlyMisses      int64 `json:"syntax_only_misses"`
	ExportedTypesOnlyHits int64 `json:"exported_types_only_hits"`
	FullTypeGraphHits     int64 `json:"full_type_graph_hits"`
	FullTypeGraphMisses   int64 `json:"full_type_graph_misses"`
}

type l2MetricsJSON struct {
	Hits    int64 `json:"hits"`
	Misses  int64 `json:"misses"`
	Stores  int64 `json:"stores"`
	Skipped int64 `json:"skipped"`
	Errors  int64 `json:"errors"`
}

type diagnosticsSummary struct {
	Count      int            `json:"count"`
	BySeverity map[string]int `json:"by_severity"`
}

// writeMetricsJSON serialises the run metrics to path. Parent
// directories are created on demand. Errors are returned to the
// caller; the caller decides whether to fail the run (today it
// warns and continues).
func writeMetricsJSON(path string, runOut *engine.RunOutput, diags []output.Diagnostic, wall time.Duration) error {
	payload := metricsJSON{
		SchemaVersion:  metricsJSONSchemaVersion,
		PlaidVersion: resolveVersion().Version,
		WallSeconds:    wall.Seconds(),
		Diagnostics:    summariseDiagnostics(diags),
	}
	if runOut != nil {
		payload.L1 = l1MetricsJSON{
			Hits:    runOut.CacheMetrics.L1.Hits,
			Misses:  runOut.CacheMetrics.L1.Misses,
			Stores:  runOut.CacheMetrics.L1.Stores,
			Skipped: runOut.CacheMetrics.L1.Skipped,
			Errors:  runOut.CacheMetrics.L1.Errors,
			ByScope: l1MetricsJSONByScope{
				SyntaxOnlyHits:      runOut.CacheMetrics.L1.SyntaxOnlyHits,
				SyntaxOnlyMisses:    runOut.CacheMetrics.L1.SyntaxOnlyMisses,
				FullTypeGraphHits:   runOut.CacheMetrics.L1.FullTypeGraphHits,
				FullTypeGraphMisses: runOut.CacheMetrics.L1.FullTypeGraphMisses,
			},
		}
		payload.L2 = l2MetricsJSON{
			Hits:    runOut.CacheMetrics.L2.Hits,
			Misses:  runOut.CacheMetrics.L2.Misses,
			Stores:  runOut.CacheMetrics.L2.Stores,
			Skipped: runOut.CacheMetrics.L2.Skipped,
			Errors:  runOut.CacheMetrics.L2.Errors,
		}
		if runOut.CacheMetrics.L0 != nil {
			payload.L0 = &l0MetricsJSON{
				Hits:         runOut.CacheMetrics.L0.Hits,
				Misses:       runOut.CacheMetrics.L0.Misses,
				Stores:       runOut.CacheMetrics.L0.Stores,
				Errors:       runOut.CacheMetrics.L0.Errors,
				SkippedPkgs:  runOut.CacheMetrics.L0.SkippedPkgs,
				DepHits:      runOut.CacheMetrics.L0.DepHits,
				DepWrites:    runOut.CacheMetrics.L0.DepWrites,
				OverrideHits: runOut.CacheMetrics.L0.OverrideHits,
			}
		}
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// summariseDiagnostics tallies diagnostics by severity. Buckets that
// receive zero diagnostics are still emitted so the consumer can
// rely on a stable map shape.
func summariseDiagnostics(diags []output.Diagnostic) diagnosticsSummary {
	s := diagnosticsSummary{
		Count: len(diags),
		BySeverity: map[string]int{
			string(output.SeverityError):   0,
			string(output.SeverityWarning): 0,
			string(output.SeverityInfo):    0,
		},
	}
	for _, d := range diags {
		sev := string(d.Severity)
		if sev == "" {
			sev = string(output.SeverityError)
		}
		s.BySeverity[sev]++
	}
	return s
}
