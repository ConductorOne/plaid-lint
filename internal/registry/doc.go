// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package registry resolves a parsed .golangci.yml [config.Config]
// into the set of linters T2.4's CLI + the engine actually enable,
// applying:
//
//   - default-group expansion (`linters.default: none/standard/all/fast`)
//   - enable / disable union and subtract
//   - per-linter Go-version fan-out (`Run.Go` → `Govet.Go` /
//     `Revive.Go` / `Gocritic.Go` / `ParallelTest.Go` /
//     `Formatters.GoFumpt.LangVersion`)
//   - v1 gosimple/stylecheck/staticcheck check-selector consolidation
//   - unknown-linter-name validation with a Levenshtein-based
//     did-you-mean suggestion
//
// The package is config-resolution only. It does not load custom
// plugin `.so` files (Phase 4), wire diagnostics into the engine
// (T2.4 + production wiring), or run any analyzers. The output of
// [BuildFromConfig] is a slice of [Resolved] values carrying the
// canonical linter name, its integration shape, the relevant
// settings sub-block, and — for linters whose `*analysis.Analyzer`
// pointer is available in plaid-lint's existing module set — the
// analyzer instance ready to wire into [analyzers.Registry].
//
// Catalog organization:
//
//   - [catalog] is the static per-process inventory of every linter
//     name the registry knows about. Entries are seeded by [init];
//     tests use [NewTestRegistry] when they need a known-empty or
//     custom shape.
//   - [Build] is the stateless entry point that takes a
//     [*config.Config] and returns the resolved enabled-linter set.
//   - [Validate] is the cheap pre-flight that rejects unknown linter
//     names with descriptive errors.
package registry
