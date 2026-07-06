// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package subproc is the subprocess invocation framework for the three
// whole-program linters carved out for subprocess execution: unused, unparam, and
// custom plugins (tracecheck etc.). T3.1 ships the framework only;
// the per-linter wrappers (T3.2 unused, T3.3 unparam, T3.4 custom
// plugin protocol) are separate dispatches that consume the types
// declared here.
//
// The framework's job is narrow:
//
//  1. Define the [Runner] interface every wrapper implements.
//  2. Provide [Invoke], a context-aware subprocess primitive that
//     captures stdout / stderr / exit code with documented failure
//     modes ([InvokeError], [ParseError], timeout via ctx).
//  3. Provide a content-addressed [Cache] under
//     ${XDG_CACHE_HOME:-$HOME/.cache}/plaid-lint/subproc/ keyed on
//     the workspace's transitive Go-file content hash plus the
//     linter's name, version, and per-linter settings hash.
//  4. Document the result-merge contract from native subprocess
//     output to [github.com/conductorone/plaid-lint/internal/output.Diagnostic]
//     in diagnostic.go.
//
// Out of scope for T3.1: any specific linter wrapper, any engine
// wiring, any tracecheck integration. The engine consumes [Runner]
// through a separate dispatch.
package subproc
