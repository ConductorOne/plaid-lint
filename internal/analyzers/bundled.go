// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/nilness"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/unusedresult"

	ineffassign "github.com/gordonklaus/ineffassign/pkg/ineffassign"
	errcheck "github.com/kisielk/errcheck/errcheck"

	sa1000 "honnef.co/go/tools/staticcheck/sa1000"
)

// noConfigSalt returns a ConfigSalt function for analyzers whose
// descriptor has no exposed config block. The function ignores cfg and
// returns a stable digest derived from the analyzer name alone, so the
// L1 entry's ConfigSalt is non-zero (which lets a future "this
// analyzer used to have no config; now it has config" upgrade not
// collide on the all-zero sentinel).
func noConfigSalt(name string) func(any) [32]byte {
	salt := ConfigSalt(name, nil)
	return func(any) [32]byte { return salt }
}

// configSaltOf returns a closure that canonicalizes cfg through
// ConfigSalt with the given analyzer name. Use this when the descriptor's
// config block is a Go struct or map[string]any that the canonicalizer
// can walk.
func configSaltOf(name string) func(any) [32]byte {
	return func(cfg any) [32]byte { return ConfigSalt(name, cfg) }
}

// BundledDescriptors returns the Phase 1 set of analyzer descriptors
// in deterministic order. The W7 root set is 8 analyzers: the W6
// 5-analyzer set plus errcheck, ineffassign, and SA1000. The
// remaining staticcheck SA-checks and goimports remain deferred.
//
// Every descriptor uses [KeyInputAllPackageSource] (the conservative
// default) and a name-salted ConfigSalt closure. Per-analyzer config
// surfaces are wired in Phase 2 alongside the full .golangci.yml
// parser.
//
// Prerequisite analyzers (inspect, ctrlflow, buildssa) are descriptor-
// registered too so the L1 wiring can find them. Their Result types
// (*inspector.Inspector, *CFGs, *SSA) are not gob-encodable in a
// stable way; their descriptors leave ResultCodec nil, so the L1
// wiring falls back to the W6 prereq-bypass path.
func BundledDescriptors() []*AnalyzerDescriptor {
	return []*AnalyzerDescriptor{
		// The W6 govet-bundled subset.
		{
			Analyzer:     assign.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("assign"),
			CacheVersion: 1,
		},
		{
			Analyzer:     nilfunc.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("nilfunc"),
			CacheVersion: 1,
		},
		{
			Analyzer:     nilness.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("nilness"),
			NeedsIR:      true, // consumes buildssa
			CacheVersion: 1,
		},
		{
			Analyzer:                  printf.Analyzer,
			KeyInputs:                 []KeyInput{KeyInputAllPackageSource},
			Exports:                   []FactClass{FactClassObject},
			ConfigSalt:                configSaltOf("printf"),
			PropagatesOnAPIChangeOnly: true,
			CacheVersion:              1,
		},
		{
			Analyzer:     unusedresult.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   configSaltOf("unusedresult"),
			CacheVersion: 1,
		},

		// W7 newly wired analyzers.
		{
			Analyzer:     errcheck.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   configSaltOf("errcheck"),
			CacheVersion: 1,
		},
		{
			// ineffassign is purely syntactic: its Run body reads only
			// pass.Files and pass.Report; the variable-flow analysis
			// uses *ast.Object (the deprecated parser-level lexical
			// identifier-resolution table), never pass.TypesInfo or
			// pass.Pkg. Audited against
			// github.com/gordonklaus/ineffassign@v0.2.0; if the
			// analyzer ever grows pass.TypesInfo / pass.Pkg reads,
			// drop this back to FullTypeGraph (the default) and bump
			// EngineCacheVersion.
			Analyzer:     ineffassign.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("ineffassign"),
			CacheVersion: 1,
			TypeUseScope: TypeUseSyntaxOnly,
		},
		// Single staticcheck SA-check (SA1000) as a proof of wiring;
		// the remaining 94 are registered via Phase 2's full
		// .golangci.yml integration. SA1000 requires buildir (honnef's
		// IR-builder), which has a non-cacheable *ir.Package Result;
		// buildir's descriptor leaves the codec nil and gets the
		// prereq-bypass fallback. The W7 wiring proves the integration
		// shape; the long tail lands when streaming L3 IR does in W8.
		{
			Analyzer:     sa1000.SCAnalyzer.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("SA1000"),
			NeedsIR:      true,
			CacheVersion: 1,
		},
		{
			// buildir is honnef's IR-builder, the Requires-prerequisite
			// of every staticcheck SA-* analyzer. We pull it through
			// SA1000's Requires[0] because honnef ships buildir under
			// internal/passes/buildir and we can't import it directly.
			Analyzer:     sa1000.SCAnalyzer.Analyzer.Requires[0],
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("buildir"),
			NeedsIR:      true,
			CacheVersion: 1,
		},

		// Prerequisite analyzers — registered so the L1 wiring can
		// look up their descriptors; their Result types are not
		// stably gob-encodable, so the codec stays nil and they
		// inherit the prereq-bypass fallback path.
		{
			Analyzer:     inspect.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("inspect"),
			CacheVersion: 1,
		},
		{
			Analyzer:     ctrlflow.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("ctrlflow"),
			CacheVersion: 1,
		},
		{
			Analyzer:     buildssa.Analyzer,
			KeyInputs:    []KeyInput{KeyInputAllPackageSource},
			ConfigSalt:   noConfigSalt("buildssa"),
			NeedsIR:      true,
			CacheVersion: 1,
		},
	}
}

// init registers the bundled descriptors against BundledRegistry. The
// registry is consulted by the gopls fork's L1 wiring at
// action-execution time; tests that want a different set construct
// their own Registry and pass it through the test-only override.
func init() {
	for _, d := range BundledDescriptors() {
		BundledRegistry.Register(d)
	}
}

// AllBundledAnalyzers returns the *analysis.Analyzer pointers for the
// W7 root analyzer set — the set Snapshot.Analyze invokes when the
// caller doesn't specify a narrower selection. Prerequisite-only
// analyzers (inspect/buildssa/ctrlflow/buildir) are excluded; they
// are pulled in automatically via Requires resolution by
// Snapshot.Analyze.
func AllBundledAnalyzers() []*analysis.Analyzer {
	return []*analysis.Analyzer{
		assign.Analyzer,
		nilfunc.Analyzer,
		nilness.Analyzer,
		printf.Analyzer,
		unusedresult.Analyzer,
		errcheck.Analyzer,
		ineffassign.Analyzer,
		sa1000.SCAnalyzer.Analyzer,
	}
}

// AllPhase1RootAnalyzers returns the deduplicated union of the W7
// root set ([AllBundledAnalyzers]) and the W8 staticcheck SA-* mass-
// wire ([AllStaticcheckSAAnalyzers]). SA1000 appears in both sets and
// is included exactly once. The result is the analyzer set the W10
// benchmark harness installs by default and the workload every
// plaid-lint deployment runs.
//
// Order: W7 roots first (in their declared order), then SA-* checks
// not already covered (in staticcheck.Analyzers' declaration order).
// Dedup is keyed on the analyzer pointer.
func AllPhase1RootAnalyzers() []*analysis.Analyzer {
	w7 := AllBundledAnalyzers()
	sa := AllStaticcheckSAAnalyzers()
	out := make([]*analysis.Analyzer, 0, len(w7)+len(sa))
	seen := make(map[*analysis.Analyzer]bool, len(w7)+len(sa))
	for _, a := range w7 {
		if a == nil || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	for _, a := range sa {
		if a == nil || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}
