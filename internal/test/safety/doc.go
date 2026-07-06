// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package safety is the W11 task-1.40 safety-harness suite. Each
// subtest drives a fixture through a cold→edit→cold cycle and asserts
// the observed set of re-analyzed packages matches a hand-written
// expected_cascade.json ground truth.
//
// The "observed re-analyzed set" is computed by snapshotting the L1
// cache directory before and after the edit-and-re-run cycle, then
// decoding the new L1 entries to extract their PackageID. Every
// L1Entry has a PackageID field (see internal/cache/l1.go), so the
// disk → PackageID mapping is well-defined.
//
// Six fixtures cover the documented cascade-shape matrix:
//
//   - leaf_body       : edit a leaf's non-exported body; expect {leaf}.
//   - leaf_wrapper    : add an exported printf-wrapper in a leaf;
//                       expect {leaf, consumer} via DepFactsDigest.
//   - midtype         : add an exported struct field in a mid-graph pkg;
//                       expect {mid, all_transitive_consumers} via
//                       DepTypeDigest.
//   - midbody         : edit a mid-graph pkg's non-exported body;
//                       expect {mid} only (gcexportdata stays warm).
//   - gomod_bump      : bump go.mod's `go` directive; expect every
//                       package re-analyzed (the
//                       module.GoVersion feeds localPackageKey).
//   - buildtag_flip   : flip a //go:build constraint; expect every
//                       package transitively importing the file's
//                       package to re-analyze (source set changes).
//
// The cascade computation is "packages that wrote NEW L1 entries on
// the re-run" — a package counts as re-analyzed if at least one of
// its (analyzer, package) actions produced a new content-addressed
// entry. Prerequisite analyzers (inspect, buildssa, ctrlflow) do not
// write L1 entries (prereq-bypass), but the root analyzers do.
//
// The design uses disk-snapshot-and-decode
// vs. instrumented counter — the former is observable from outside
// the cache layer without modifying any W4-W10 frozen contracts.
package safety
