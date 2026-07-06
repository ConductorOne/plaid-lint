// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"strings"

	"golang.org/x/tools/go/analysis"

	"honnef.co/go/tools/staticcheck"
)

// AnalyzerRequiresIR reports whether a's transitive Requires set
// includes an IR-producing analyzer — honnef's buildir
// (`honnef.co/go/tools/internal/passes/buildir`) or x/tools'
// buildssa (`golang.org/x/tools/go/analysis/passes/buildssa`). The
// check is name-based because buildir lives under an internal/
// path that downstream consumers cannot import directly, and
// because the L3 pin contract is keyed on analyzer identity.
//
// AnalyzerRequiresIR is the single source of truth for "this
// analyzer's Run will consume IR":
//
//   - Bundle-time uses it to set AnalyzerDescriptor.NeedsIR for
//     every staticcheck SA-* root.
//   - The L3 pin path (internal/gopls/cache/l3.go) uses it as a
//     walker-fallback when the action's analyzer has no registered
//     descriptor — Honnef intermediate prerequisites like
//     fact_purity, fact_typedness, and honnef's internal nilness
//     are pulled in transitively via Requires from SA-* roots but
//     are NOT registered in BundledRegistry, yet their Run bodies
//     consume `pass.ResultOf[buildir]`. Without the fallback the
//     W9 scheduler's free-after-fanin signal would miss them and
//     a use-after-free becomes possible.
//
// Concurrency-safety: the function only reads a's Requires graph,
// which is a constant set in every analyzer pointer's lifetime.
// Cycle-safety: the seen-set guards against pathological self-
// referential Requires shapes even though staticcheck's graph is
// a DAG in practice.
func AnalyzerRequiresIR(a *analysis.Analyzer) bool {
	if a == nil {
		return false
	}
	if isIRProducer(a.Name) {
		return true
	}
	seen := make(map[*analysis.Analyzer]bool)
	var walk func(*analysis.Analyzer) bool
	walk = func(x *analysis.Analyzer) bool {
		if x == nil || seen[x] {
			return false
		}
		seen[x] = true
		if isIRProducer(x.Name) {
			return true
		}
		for _, r := range x.Requires {
			if walk(r) {
				return true
			}
		}
		return false
	}
	return walk(a)
}

// isIRProducer reports whether name belongs to a known IR-producer
// analyzer — buildir (honnef) or buildssa (x/tools). Both produce
// the SSA-shaped IR consumed by NeedsIR analyzers.
func isIRProducer(name string) bool {
	return name == "buildir" || name == "buildssa"
}

// requiresIR preserves the original spelling used internally by
// the staticcheck wiring. It is a thin alias over AnalyzerRequiresIR
// to keep call-sites and tests stable.
func requiresIR(a *analysis.Analyzer) bool { return AnalyzerRequiresIR(a) }

// staticcheckSAAnalyzers returns the *analysis.Analyzer pointers for
// every staticcheck SA-* check shipped by `honnef.co/go/tools` at the
// pinned version. The list is read from staticcheck.Analyzers (the
// upstream canonical list) and deduplicated against SA1000, which is
// independently wired in bundled.go as the W7 template.
//
// The function preserves staticcheck.Analyzers' declaration order,
// which is the staticcheck-internal generate.go ordering and is
// stable across upstream patch releases.
func staticcheckSAAnalyzers() []*analysis.Analyzer {
	out := make([]*analysis.Analyzer, 0, len(staticcheck.Analyzers))
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil {
			continue
		}
		if !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		if a.Name == "SA1000" {
			// Wired in bundled.go as the W7 proof-of-shape template;
			// skip here to avoid duplicate registration.
			continue
		}
		out = append(out, a)
	}
	return out
}

// staticcheckSADescriptors returns AnalyzerDescriptors for every
// staticcheck SA-* check NOT already registered in bundled.go's
// BundledDescriptors. NeedsIR is set per check by walking its
// transitive Requires chain; analyzers that consume buildir get
// NeedsIR=true, the rest get NeedsIR=false.
//
// Every SA-* descriptor uses [KeyInputAllPackageSource] and a
// name-salted ConfigSalt closure.
// Per-check config surfaces (e.g. SA1019's ignored-imports list)
// are wired in Phase 2 alongside the full .golangci.yml parser.
func staticcheckSADescriptors() []*AnalyzerDescriptor {
	saAnalyzers := staticcheckSAAnalyzers()
	out := make([]*AnalyzerDescriptor, 0, len(saAnalyzers))
	for _, a := range saAnalyzers {
		out = append(out, &AnalyzerDescriptor{
			Analyzer:     a,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt(a.Name),
			NeedsIR:      requiresIR(a),
			CacheVersion: 1,
		})
	}
	return out
}

// init augments the bundled.go init() pass with every staticcheck SA-*
// descriptor. This runs after bundled.go's init() (Go runs init
// functions in file lexical order within a package, but registration
// is idempotent on analyzer pointer so the ordering is not load-
// bearing).
func init() {
	for _, d := range staticcheckSADescriptors() {
		BundledRegistry.Register(d)
	}
}

// AllStaticcheckSAAnalyzers returns the *analysis.Analyzer pointers
// for every SA-* check now registered, including SA1000. Useful for
// tests that want to drive Snapshot.Analyze with the full SA-* set.
//
// SA1000 is taken from staticcheck.Analyzers (the same list); the
// duplicate in bundled.go's AllBundledAnalyzers ensures the W7 root
// set keeps SA1000 wired regardless of staticcheck's table state.
func AllStaticcheckSAAnalyzers() []*analysis.Analyzer {
	out := make([]*analysis.Analyzer, 0, len(staticcheck.Analyzers))
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		out = append(out, a)
	}
	return out
}
