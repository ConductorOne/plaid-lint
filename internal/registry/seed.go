// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

// seedRows returns the static catalog of every linter T2.3 knows
// about. Mirrors a 127-linter audit plus the v2-only formatter
// surface. Source-of-truth ordering is alphabetical by canonical name
// to make additions stable.
//
// Group membership mirrors upstream's `lintersdb` defaults:
//
//   - `standard`: errcheck, govet, ineffassign, staticcheck (upstream's
//     default-on set; unused would join but is subprocess-deferred).
//   - `fast`: linters that do not require full type info or SSA. The
//     canonical fast list mirrors upstream `IsFast()` selectors.
//
// `none` and `all` aren't seeded per-row: every entry is implicitly
// "not in `none`" and (unless Deprecated) "in `all`".
//
// Shape classification rules (T2.3 ship state):
//
//   - ShapeNative: backed by an `*analysis.Analyzer` instance that
//     plaid-lint's existing module deps export (errcheck,
//     ineffassign, the x/tools-shaped govet sub-analyzers when they
//     are exposed under their upstream linter name). Today this set
//     is small because upstream golangci-lint reorganizes most
//     x/tools passes under `govet`'s `enable` map.
//   - ShapeNativeFamily: a fan-out wrapper. Today only `staticcheck`
//     (95 SA + 18 ST + 35 S + 12 QF = 160 checks across 4 honnef
//     tables) and `govet` (47 x/tools-shaped sub-analyzers) use this
//     shape. The user-facing linter name is the family name; the
//     engine wires every member analyzer.
//   - ShapeRegistryOnly: the catalog has the entry so unknown-name
//     validation passes, but the analyzer instance is not in
//     plaid-lint's `go.mod` yet. Enabling produces a
//     [Resolved] with [Status] == StatusNoAnalyzerWired. Follow-up
//     patches add the dep and re-shape to ShapeNative without
//     reordering this seed list.
//   - ShapeSubprocess: deferred-to-Phase-3 set.
//   - ShapeFormatter: `formatters.enable`-only; misplacing under
//     `linters.enable` produces the T2.1 validate error.
func seedRows() []seedRow {
	return []seedRow{
		// A-shape linters. Most are ShapeRegistryOnly today because
		// each lives in its own upstream third-party module that
		// plaid-lint hasn't taken a dep on yet. The two exceptions
		// (errcheck, ineffassign) are already in go.mod via the W7
		// root set.
		{name: "asasalint", shape: ShapeNative},                // batch2
		{name: "asciicheck", shape: ShapeNative},               // batch1
		{name: "bidichk", shape: ShapeNative, fast: true},      // batch1
		{name: "bodyclose", shape: ShapeNative},                // batch1
		{name: "canonicalheader", shape: ShapeNative},          // batch2
		{name: "containedctx", shape: ShapeNative},             // batch2
		{name: "contextcheck", shape: ShapeNative},             // batch3
		{name: "copyloopvar", shape: ShapeNative},              // batch3
		{name: "cyclop", shape: ShapeNative},                   // batch4
		{name: "decorder", shape: ShapeNative, fast: true},     // batch5
		{name: "depguard", shape: ShapeNative},                 // polybatchA
		{name: "dogsled", shape: ShapeNative, fast: true},      // inline port
		{name: "dupl", shape: ShapeNative},                     // per-pass port
		{name: "dupword", shape: ShapeNative, fast: true},      // batch4
		{name: "durationcheck", shape: ShapeNative},            // batch1
		{name: "embeddedstructfieldcheck", shape: ShapeNative}, // batch5

		// errcheck — first-class native; already in go.mod.
		{name: "errcheck", shape: ShapeNative, standard: true},

		{name: "errchkjson", shape: ShapeNative},                            // batch4
		{name: "errname", shape: ShapeNative},                               // batch2
		{name: "errorlint", shape: ShapeNative},                             // batch5
		{name: "exhaustive", shape: ShapeNative},                            // polybatchA
		{name: "exhaustruct", shape: ShapeNative},                           // batch6
		{name: "exptostd", shape: ShapeNative},                              // batch2
		{name: "fatcontext", shape: ShapeNative},                            // batch3
		{name: "forbidigo", shape: ShapeNative},                             // polybatchA
		{name: "forcetypeassert", shape: ShapeNative},                       // batch2
		{name: "funcorder", shape: ShapeNative},                             // batch4
		{name: "funlen", shape: ShapeNative, fast: true},                    // batch4
		{name: "ginkgolinter", shape: ShapeNative},                          // batch4
		{name: "gocheckcompilerdirectives", shape: ShapeNative, fast: true}, // batch1
		{name: "gochecknoglobals", shape: ShapeNative},                      // batch3
		{name: "gochecknoinits", shape: ShapeNative},                        // inline port
		{name: "gochecksumtype", shape: ShapeNative},                        // wrapbatch
		{name: "gocognit", shape: ShapeNative},                              // batch3
		{name: "goconst", shape: ShapeNative, fast: true},                   // wrapbatch
		{name: "gocritic", shape: ShapeNative, hasGoVersion: true},          // gocritic
		{name: "gocyclo", shape: ShapeNative, fast: true},                   // library-wrap
		{name: "godoclint", shape: ShapeNative},                             // wrapbatch
		{name: "godot", shape: ShapeNative, fast: true},                     // wrapbatch
		{name: "godox", shape: ShapeNative, fast: true},                     // library-wrap
		{name: "goheader", shape: ShapeNative, fast: true},                  // batch6
		{name: "gomoddirectives", shape: ShapeNative, fast: true},           // wrapbatch
		{name: "gomodguard", shape: ShapeNative, fast: true},                // wrapbatch
		{name: "goprintffuncname", shape: ShapeNative, fast: true},          // batch1
		{name: "gosec", shape: ShapeNative},                                 // polybatchA
		// gosimple is a v1-only top-level name; resolved as an alias of
		// staticcheck (declared below). `linters-settings.gosimple.checks`
		// is consolidated into staticcheck.checks at registry-build time.
		{name: "gosmopolitan", shape: ShapeNative}, // batch3

		// govet — first-class native; fan-out over the x/tools passes
		// that ship under govet's enable map upstream.
		{name: "govet", shape: ShapeNativeFamily, standard: true, hasGoVersion: true},

		{name: "grouper", shape: ShapeNative, fast: true}, // batch5
		{name: "iface", shape: ShapeNativeFamily},
		{name: "importas", shape: ShapeNative},    // batch6
		{name: "inamedparam", shape: ShapeNative}, // batch4

		// ineffassign — first-class native; already in go.mod.
		{name: "ineffassign", shape: ShapeNative, standard: true},

		{name: "interfacebloat", shape: ShapeNative}, // batch4
		{name: "intrange", shape: ShapeNative},       // batch2
		// iotamixing has no upstream module — the name only appears in
		// golangci-lint's internal catalog and there is no external
		// `github.com/<author>/iotamixing` repo to import. Stays
		// ShapeRegistryOnly indefinitely; the cleanup batch closed every
		// other long-tail row but this one cannot be wired without a
		// vendor / fork dispatch the project does not currently want.
		{name: "iotamixing", shape: ShapeRegistryOnly, fast: true},
		{name: "ireturn", shape: ShapeNative},              // batch6
		{name: "lll", shape: ShapeNative, fast: true},      // inline port
		{name: "loggercheck", shape: ShapeNative},          // batch6
		{name: "maintidx", shape: ShapeNative, fast: true}, // batch3
		{name: "makezero", shape: ShapeNative},             // batch3
		{name: "mirror", shape: ShapeNative},               // batch2
		{name: "misspell", shape: ShapeNative, fast: true}, // wrapbatch
		{name: "mnd", shape: ShapeRegistryOnly, fast: true, deprecated: "mnd is deprecated; use the upstream replacement"},
		{name: "modernize", shape: ShapeNativeFamily},                  // cleanup
		{name: "musttag", shape: ShapeNative},                          // batch5
		{name: "nakedret", shape: ShapeNative, fast: true},             // batch1
		{name: "nestif", shape: ShapeNative, fast: true},               // library-wrap
		{name: "nilerr", shape: ShapeNative},                           // batch1
		{name: "nilnesserr", shape: ShapeNative},                       // batch2
		{name: "nilnil", shape: ShapeNative},                           // batch3
		{name: "nlreturn", shape: ShapeNative, fast: true},             // batch3
		{name: "noctx", shape: ShapeNative},                            // batch1
		{name: "noinlineerr", shape: ShapeNative},                      // batch2
		{name: "nolintlint", shape: ShapeNative},                       // cleanup (in-tree)
		{name: "nonamedreturns", shape: ShapeNative, fast: true},       // batch2
		{name: "nosprintfhostport", shape: ShapeNative},                // batch2
		{name: "paralleltest", shape: ShapeNative, hasGoVersion: true}, // batch4
		{name: "perfsprint", shape: ShapeNative},                       // batch5
		{name: "prealloc", shape: ShapeNative},                         // wrapbatch
		{name: "predeclared", shape: ShapeNative, fast: true},          // batch1
		{name: "promlinter", shape: ShapeNative, fast: true},           // wrapbatch
		{name: "protogetter", shape: ShapeNative},                      // batch5
		{name: "reassign", shape: ShapeNative},                         // batch4
		{name: "recvcheck", shape: ShapeNative},                        // batch5
		{name: "revive", shape: ShapeNative, hasGoVersion: true},       // revive
		{name: "rowserrcheck", shape: ShapeNative},                     // batch4
		{name: "sloglint", shape: ShapeNative},                         // batch5
		{name: "spancheck", shape: ShapeNative},                        // batch6
		{name: "sqlclosecheck", shape: ShapeNative},                    // batch3

		// staticcheck — first-class native family; fan-out over honnef's
		// 95 SA + 18 ST + 35 S + 12 QF tables. v1 names gosimple and
		// stylecheck alias staticcheck.
		{name: "staticcheck", shape: ShapeNativeFamily, standard: true, aliases: []string{"gosimple", "stylecheck"}},

		{name: "tagalign", shape: ShapeNative, fast: true}, // batch5
		{name: "tagliatelle", shape: ShapeNative},          // batch6
		{name: "testableexamples", shape: ShapeNative},     // batch3
		{name: "testifylint", shape: ShapeNative},          // batch6
		{name: "testpackage", shape: ShapeNative},          // batch4
		{name: "thelper", shape: ShapeNative},              // batch6
		{name: "tparallel", shape: ShapeNative},            // batch1
		{name: "tracecheck", shape: ShapeNative},           // wire_analyzers_tracecheck.go — vendored from github.com/ductone/ci-tools

		// typecheck — engine-internal; the parser/type-checker emits
		// its diagnostics directly. Registered as ShapeNative with no
		// analyzer so disable propagation works.
		{name: "typecheck", shape: ShapeNative, standard: true, fast: true},

		{name: "unconvert", shape: ShapeNative},                 // library-wrap
		{name: "unparam", shape: ShapeNative},                   // library-wrap
		{name: "unqueryvet", shape: ShapeNative},                // cleanup
		{name: "unused", shape: ShapeNative, standard: true},    // library-wrap
		{name: "usestdlibvars", shape: ShapeNative, fast: true}, // batch1
		{name: "usetesting", shape: ShapeNative},                // batch5
		{name: "varnamelen", shape: ShapeNative},                // batch6
		{name: "wastedassign", shape: ShapeNative},              // batch2
		{name: "whitespace", shape: ShapeNative, fast: true},    // batch2
		{name: "wrapcheck", shape: ShapeNative},                 // batch5
		{name: "wsl", shape: ShapeRegistryOnly, fast: true, deprecated: "wsl is deprecated; use wsl_v5"},
		{name: "wsl_v5", shape: ShapeNative, fast: true}, // batch6
		{name: "zerologlint", shape: ShapeNative},        // batch2

		// Formatters — registered so the catalog can answer "this name
		// is a formatter, not a linter" for did-you-mean.
		{name: "gci", shape: ShapeFormatter},
		{name: "gofmt", shape: ShapeFormatter},
		{name: "gofumpt", shape: ShapeFormatter, hasGoVersion: true},
		{name: "goimports", shape: ShapeFormatter},
		{name: "golines", shape: ShapeFormatter},
		{name: "swaggo", shape: ShapeFormatter},
	}
}
