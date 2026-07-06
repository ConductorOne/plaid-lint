// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package golden is the W11 task-1.39 golden-test suite. The tests in
// this package drive a fixed fixture set through Snapshot.Analyze and
// assert on stable observable counters + diagnostic streams.
//
// Five fixtures live under testdata/:
//
//   - basic/        — leaf + consumer; cold/warm L1 hit-miss verification.
//   - factroundtrip/ — printf-wrapper fact propagation across packages.
//   - addremove/    — base state used by file-add / file-delete subtests.
//   - gomod/        — go.mod fixture used to verify Env.GoVersion stays
//                     non-zero across re-analysis.
//   - buildtag/     — multi-file package with build tags that include
//                     vs exclude a diagnostic-triggering source file.
//
// Each subtest captures cold/warm diagnostic streams + L1/L2 counters
// and compares against testdata/<name>/expected.json. The -update flag
// rewrites the golden when intentional changes land.
//
// The W11 brief explicitly calls out: every counter assertion in this
// package names the counter being checked and what production behaviour
// it proves. See harness.MustL1HitsGT / MustL1StoresGT for the canned
// shape; the inline `t.Errorf` messages in each test follow the same
// "what + why" pattern.
package golden
