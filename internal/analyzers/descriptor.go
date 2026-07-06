// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package analyzers defines the AnalyzerDescriptor schema
// and the registry of bundled descriptors that plaid-lint ships with
// in Phase 1. Each descriptor declares the per-analyzer cache contract:
// which slices of package state participate in the L1 InputDigest, what
// facts the analyzer exports, the analyzer's config-salt function, and
// whether its Run Result is consumed by another analyzer.
//
// The descriptors live here (not in internal/gopls/settings) because
// settings/ holds LSP-flavored shims of upstream gopls. Descriptors are
// a plaid-lint concept; placing them in their own package keeps the
// fork boundary clean.
package analyzers

import (
	"fmt"

	"golang.org/x/tools/go/analysis"
)

// KeyInput names a slice of per-package state that participates in L1's
// InputDigest. The default KeyInputAllPackageSource is a conservative
// "hash every source byte" stand-in; opting into a narrower set per
// analyzer is how W7+ analyzers earn finer-grained caching.
//
// Phase 1 ships every wired analyzer with [KeyInputAllPackageSource];
// the narrower KeyInputs are declared here so descriptors can opt in
// incrementally in Phase 2 without a schema change.
type KeyInput int

const (
	// KeyInputAllPackageSource hashes every byte of the package's .go
	// files. The conservative default; matches today's golangci-lint
	// cache behavior.
	KeyInputAllPackageSource KeyInput = iota
	// KeyInputExportedTypeInfo hashes only the exported portion of the
	// type graph. Suitable for API-only analyzers.
	KeyInputExportedTypeInfo
	// KeyInputBodies hashes function bodies but not signatures.
	KeyInputBodies
	// KeyInputComments hashes comments. For godoc/lint-directive
	// analyzers.
	KeyInputComments
	// KeyInputTokenPositions hashes token positions. For whitespace /
	// format analyzers.
	KeyInputTokenPositions
	// KeyInputStructTags hashes struct tags. For json-tag, depguard
	// pkg checks.
	KeyInputStructTags
	// KeyInputBuildTags hashes build constraints.
	KeyInputBuildTags
	// KeyInputGeneratedMarker hashes the //go:generated marker presence.
	KeyInputGeneratedMarker
	// KeyInputConstValues hashes const initializer values that an
	// analyzer might propagate.
	KeyInputConstValues
	// KeyInputImports hashes the package's import set. For depguard/
	// gomodguard.
	KeyInputImports
)

// TypeUseScope declares how an analyzer reads transitive dep types,
// keying the per-action L1 DepTypeDigest. The narrower the scope, the
// fewer cascade-edits invalidate this analyzer's L1 entries.
//
// The static manifest is a CONSERVATIVE under-approximation: declaring
// a wider scope than the analyzer needs is safe (it just over-
// invalidates); declaring a narrower scope than the analyzer needs is
// a CORRECTNESS BUG (stale L1 hits → wrong diagnostics). Default to
// TypeUseFullTypeGraph for any analyzer whose Run-body type-graph
// access has not been audited.
type TypeUseScope uint8

const (
	// TypeUseFullTypeGraph is the conservative default: the L1
	// DepTypeDigest hashes every vdep's reachability key (ph.key),
	// matching the prior behavior. Any cascade-affecting change to a
	// transitive dep invalidates this analyzer's L1 entry. Use for
	// buildir/buildssa-consuming analyzers, anything that walks the
	// full type graph (e.g. honnef SA-* checks), and any analyzer
	// whose Run body has not been audited.
	TypeUseFullTypeGraph TypeUseScope = 0
	// TypeUseSyntaxOnly declares the analyzer reads only the package's
	// own *ast.File / *token.FileSet — no pass.TypesInfo / pass.Pkg
	// reads of dep types. The L1 DepTypeDigest is constant; only
	// package-local edits invalidate. Use for whitespace, lint-
	// directive, and other pure-syntax analyzers.
	TypeUseSyntaxOnly TypeUseScope = 1
	// TypeUseExportedTypesOnly declares the analyzer reads dep types
	// only through their EXPORTED API surface — equivalent to
	// gcexportdata. The L1 DepTypeDigest hashes only the exported-API
	// digest of each vdep. Edits to dep internals (function bodies,
	// unexported types) do not invalidate. This requires a separate
	// per-vdep exported-API hash; not implemented in this dispatch —
	// reserved for follow-up. Descriptors that opt in here will be
	// treated as TypeUseFullTypeGraph until the digest is wired.
	TypeUseExportedTypesOnly TypeUseScope = 2
)

// String renders the scope for debug dumps.
func (s TypeUseScope) String() string {
	switch s {
	case TypeUseSyntaxOnly:
		return "SyntaxOnly"
	case TypeUseExportedTypesOnly:
		return "ExportedTypesOnly"
	case TypeUseFullTypeGraph:
		return "FullTypeGraph"
	default:
		return fmt.Sprintf("TypeUseScope(%d)", uint8(s))
	}
}

// FactClass enumerates the canonical fact classes an analyzer may
// export. This is a declarative tag — the descriptor
// system uses it for future schema-driven precision; in Phase 1 the
// field is informational.
type FactClass int

const (
	// FactClassNone means the analyzer exports no facts.
	FactClassNone FactClass = iota
	// FactClassObject means the analyzer exports per-object facts
	// (ExportObjectFact-style).
	FactClassObject
	// FactClassPackage means the analyzer exports per-package facts
	// (ExportPackageFact-style).
	FactClassPackage
)

// ResultCodec serialises and deserialises an analyzer's Run Result so
// that an L1 hit can restore it for downstream analyzers that consume
// it via analysis.Analyzer.Requires.
//
// The Result is typed by the analyzer's ResultType. The codec is
// per-analyzer because different Result types have different
// determinism requirements:
//
//   - inspect.Analyzer's *inspector.Inspector is not gob-encodable in a
//     stable way (it carries AST node refs). Its descriptor leaves the
//     codec nil and falls back to the prereq-bypass path.
//   - buildssa's *buildssa.SSA holds *ssa.Package / *ssa.Function and
//     has the same problem as inspect; same fallback.
//   - ctrlflow's *ctrlflow.CFGs holds *cfg.CFG values that reference
//     ast.Node positions; same fallback.
//
// For analyzers whose Result IS serialisable, both methods must be
// set and round-trip the value losslessly.
type ResultCodec struct {
	Encode func(result any) ([]byte, error)
	Decode func(blob []byte) (any, error)
}

// AnalyzerDescriptor is the per-analyzer cache contract. Phase 1 W7
// lands the schema; the integration with the L1
// fast path consumes ConfigSalt, AnalyzerVersion, ConsumedAsResult,
// and (if non-nil) Result.
type AnalyzerDescriptor struct {
	// Analyzer is the go/analysis analyzer this descriptor wraps.
	// Required.
	Analyzer *analysis.Analyzer

	// KeyInputs declares the package-state slices that participate in
	// L1's InputDigest. Defaults to [KeyInputAllPackageSource] when
	// empty — the conservative behavior that matches W6.
	KeyInputs []KeyInput

	// Exports declares the fact classes this analyzer emits. Empty
	// means "no facts". Used in Phase 2 by the scheduler to skip
	// per-importer fact decoding for fact-less analyzers.
	Exports []FactClass

	// PropagatesOnAPIChangeOnly is true if importers of a package
	// running this analyzer can skip re-running when only the
	// package's impl details (function bodies) changed in deps.
	// Phase 1: informational; Phase 2's scheduler uses it.
	PropagatesOnAPIChangeOnly bool

	// ConfigSalt returns the 32-byte stable digest of this analyzer's
	// config block, derived from the canonicalized .golangci.yml
	// subsection. Returns the all-zero digest when the
	// analyzer has no config. Required.
	ConfigSalt func(linterConfig any) [32]byte

	// NeedsIR is true if this analyzer requires buildir's IR. Used by
	// the scheduler (W8) to decide whether to materialise L3 IR for
	// this (analyzer, package) pair on L1 miss.
	NeedsIR bool

	// AnalyzerVersion is a stable per-build hash of this analyzer's
	// binary. Two plaid-lint builds at different versions disagree
	// on AnalyzerVersion, invalidating their L1 entries. When zero,
	// the descriptor's WireAnalyzerVersion (set by the registry on
	// Register) provides the value.
	//
	// Phase 1 derives this from the running binary's SHA-256 (see
	// process_version.go); each analyzer's descriptor inherits the
	// same value because all analyzers ship in the same binary. A
	// future cross-binary scheme can populate this per-analyzer.
	AnalyzerVersion string

	// ResultCodec, if set, lets L1 cache the analyzer's Run Result
	// alongside diagnostics and facts. When nil, the descriptor opts
	// out of Result caching — the analyzer falls into the W6
	// prereq-bypass path whenever it is consumed by another
	// analyzer's Requires chain.
	ResultCodec *ResultCodec

	// TypeUseScope declares how this analyzer reads transitive dep
	// types, narrowing the per-action L1 DepTypeDigest. Defaults to
	// TypeUseFullTypeGraph (today's conservative behavior).
	// See the TypeUseScope godoc above for safety contract; misuse
	// causes stale L1 hits → wrong diagnostics.
	TypeUseScope TypeUseScope

	// CacheVersion is the per-wrapper cache-stamp folded into L1 and
	// L0 cache keys. Bumped manually by the wrapper author when the
	// wrapper's diagnostic-emission contract changes (e.g. message
	// format rewrite, exclusion-default flip) so prior cache entries
	// no longer round-trip to the new behavior.
	//
	// Defaults to 0 when unset, which combined with the engine-level
	// stamp still produces a stable key — i.e. existing descriptors
	// without an explicit version behave identically to a descriptor
	// with CacheVersion=0. New wrappers should set CacheVersion: 1
	// up-front so a future bump to 2 has a meaningful predecessor.
	//
	// NOT auto-derived: see registry.CacheVersion comment. The
	// errcheck wrapper rewrote its emission
	// behavior but cached "unchecked error" diagnostics persisted
	// across rebuilds because nothing in the cache key reflected the
	// change. CacheVersion exists so the next such change is a one-
	// line edit per wrapper.
	CacheVersion uint8
}

// HasKeyInput reports whether the descriptor declares the given key
// input. An empty KeyInputs slice is treated as
// [KeyInputAllPackageSource].
func (d *AnalyzerDescriptor) HasKeyInput(k KeyInput) bool {
	if len(d.KeyInputs) == 0 {
		return k == KeyInputAllPackageSource
	}
	for _, kk := range d.KeyInputs {
		if kk == k {
			return true
		}
	}
	return false
}

// Name returns the analyzer's name, the cache key segment.
func (d *AnalyzerDescriptor) Name() string {
	if d == nil || d.Analyzer == nil {
		return ""
	}
	return d.Analyzer.Name
}

// CanCacheResult reports whether the descriptor's Result is round-
// trippable through a ResultCodec. False means the analyzer falls
// back to the prereq-bypass path when consumed.
func (d *AnalyzerDescriptor) CanCacheResult() bool {
	return d != nil && d.ResultCodec != nil && d.ResultCodec.Encode != nil && d.ResultCodec.Decode != nil
}
