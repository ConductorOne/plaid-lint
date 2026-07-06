// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package l3 is the W11 task-1.42 L3-streaming correctness test.
// One Snapshot.Analyze invocation runs all 102 analyzers (the full
// W7+W8 root set, harness.FullPhase1Set) at concurrency=1 on a
// production-shape fixture. The test verifies:
//
//   - All 102 analyzers run to completion (Analyze returns nil error).
//   - IR pin/release stream is complete (every Pin is Release'd before
//     Analyze returns — IRManager.Snapshot is empty at end).
//   - Cold→warm diagnostic equivalence holds.
//   - Peak VmHWM is bounded.
//
// On the 1.5 GB ceiling target: the design specifies "concurrency=1 ≤ 1.5
// GB on a c1-scale workload". The W11 brief acknowledges that
// synthetic fixtures peak at ~55 MB (per W10's calibration on
// bench_medium with 102 analyzers), so the 1.5 GB ceiling is
// 30× below target on synthetic workloads. We assert peak VmHWM is
// well under 1.5 GB (so the architecture-on-synthetic-fixture claim
// is on the record) but the real c1-scale validation is W12+.
package l3
