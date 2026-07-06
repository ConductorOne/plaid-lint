// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package bench is the W10 benchmark harness for plaid-lint. It
// drives Snapshot.Analyze over a fixture in three configurations —
// cold, warm, cascade — and emits a structured [BenchmarkResult]
// the calibration scripts (and the gate-decision review) read back
// without parsing prose.
//
// The package is a library; the [cmd/plaid-lint-bench] binary is
// the CLI wrapper around it. Tests in this package exercise the
// harness against the in-repo synthetic fixtures
// (bench_small / bench_medium / bench_cascade); the gate-decision
// benchmark is run by the project lead via the binary against
// /data/squire/src/c1.
package bench
