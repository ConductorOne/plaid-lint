// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package engine is the production entry point that drives one
// `plaid-lint run` invocation end-to-end: load the workspace,
// install the resolved analyzer set, run the in-process analyzers
// via internal/gopls/cache, fan out the subprocess wrappers from
// internal/subproc, and return the merged diagnostic stream.
//
// The bench harness (internal/bench) is the test-time driver for
// the same engine layer; engine.Run exposes the production subset
// (no cold/warm/cascade scenarios, no observation-sampling, no
// JSON-to-file). Both call into Snapshot.Analyze.
package engine
