// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/facts"
)

// l1ActionID derives the L1 content-addressed action ID for the (act.a,
// act.an.ph) pair. It assembles the L1Entry header:
//
//   - InputDigest: scope-aware. FullTypeGraph uses ph.key (the gopls
//     reachability hash: package source plus transitive dep keys);
//     SyntaxOnly uses ph.localKey (package source only, no dep
//     composition) so a cascade-edit to a dep's internals does not
//     flip the importer's InputDigest. See depInputDigest.
//   - DepFactsDigest: SHA-256 over the (sorted by PackageID) sequence of
//     this analyzer's FactsHash from each vdep's actionSummary. If a
//     vdep is missing the analyzer in its actions map, that vdep
//     contributes a zero hash (it didn't produce facts for this
//     analyzer; the absence is itself an input).
//   - DepTypeDigest: scope-aware. SyntaxOnly emits a
//     domain-tag constant; FullTypeGraph hashes every vdep's ph.key.
//   - AnalyzerVersion: per-analyzer descriptor's AnalyzerVersion. W7
//     uses the process binary hash via analyzers.ProcessBinaryVersion;
//     when no descriptor is registered for the analyzer we fall back to
//     the W6 stub for compatibility.
//   - ConfigSalt: descriptor.ConfigSalt(linterConfig). The linter
//     config is passed through from the analysis driver; Phase 1 uses
//     nil (no config) for every analyzer.
//   - ToolVersion: the batch's l1ToolVer (set by AttachL1).
func (act *action) l1ActionID() clcache.ActionID {
	an := act.an
	desc := act.descriptor()
	e := &clcache.L1Entry{
		Analyzer:        act.a.Name,
		PackageID:       string(an.ph.mp.ID),
		AnalyzerVersion: analyzerVersionFor(act.a, desc),
		ConfigSalt:      configSaltFor(act.a, desc),
		ToolVersion:     an.batch.l1ToolVer,
	}
	e.InputDigest = act.inputDigest()
	// DepFactsDigest / DepTypeDigest: assembled in deterministic order
	// (sorted by PackageID) so the hash is stable across runs.
	e.DepFactsDigest = act.depFactsDigest()
	e.DepTypeDigest = act.depTypeDigest()
	return clcache.ComputeL1ActionID(e)
}

// descriptor returns the AnalyzerDescriptor for this action's analyzer
// from the batch's registry, or nil when the registry is unset or the
// analyzer is unregistered. Callers that need a guaranteed-present
// descriptor must use the explicit helpers below, which fall back to
// W6-equivalent behaviour.
func (act *action) descriptor() *analyzers.AnalyzerDescriptor {
	b := act.an.batch
	if b == nil || b.l1Registry == nil {
		return nil
	}
	return b.l1Registry.Lookup(act.a)
}

// descriptorCanRoundTripResult reports whether the registry for an
// has a descriptor for a whose ResultCodec can round-trip the
// analyzer's Run Result. Used by the W7 narrowing of the prereq-
// bypass: a prerequisite whose Result is cacheable does not need to
// bypass L1, because an L1 hit restores the Result alongside
// diagnostics + facts.
func descriptorCanRoundTripResult(an *analysisNode, a *analysis.Analyzer) bool {
	if an == nil || an.batch == nil || an.batch.l1Registry == nil {
		return false
	}
	d := an.batch.l1Registry.Lookup(a)
	return d != nil && d.CanCacheResult()
}

// analyzerVersionFor returns a stable per-analyzer version string. The
// W7 descriptor carries a binary-derived value; the W6 stub
// (sha256(name)[:8]) survives as a fallback when no descriptor is
// registered, so tests that don't register descriptors keep working.
//
// The result also folds in the descriptor's per-wrapper CacheVersion
// and the engine-level analyzers.EngineCacheVersion, so a bump to
// either invalidates this analyzer's L1 entries without changing the
// L1Entry on-disk schema. See Phase 5.10.
func analyzerVersionFor(a *analysis.Analyzer, d *analyzers.AnalyzerDescriptor) string {
	base := l1AnalyzerVersionStub(a)
	if d != nil && d.AnalyzerVersion != "" {
		base = d.AnalyzerVersion
	}
	var wrapper uint8
	if d != nil {
		wrapper = d.CacheVersion
	}
	// "cv" tag is self-describing in debug dumps; e.g.
	//   clk-bin-deadbeefcafe1234.cv1.e1
	// means wrapper version 1, engine version 1.
	return fmt.Sprintf("%s.cv%d.e%d", base, wrapper, analyzers.EngineCacheVersion)
}

// configSaltFor returns the descriptor's ConfigSalt for nil (no
// config) input, or the W6 name-only salt when no descriptor is
// registered.
func configSaltFor(a *analysis.Analyzer, d *analyzers.AnalyzerDescriptor) [32]byte {
	if d != nil && d.ConfigSalt != nil {
		return d.ConfigSalt(nil)
	}
	return clcache.ConfigSaltForAnalyzer(a.Name, nil)
}

// depFactsDigest returns a SHA-256 over the (vdepID, FactsHash) pairs of
// every vdep, sorted by vdepID. A vdep whose actions map lacks act.a
// (e.g. a non-facty vdep when act.a IS facty) contributes a zero hash
// rather than being skipped: the absence is itself an input that the L1
// key must capture.
func (act *action) depFactsDigest() [32]byte {
	var ids []string
	for id := range act.vdeps {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	h := sha256.New()
	var zero [32]byte
	for _, id := range ids {
		vdep := act.vdeps[PackageID(id)]
		fmt.Fprintf(h, "%s\n", id)
		summary, ok := vdep.actions[act.stableName]
		if !ok || summary == nil {
			_, _ = h.Write(zero[:])
			continue
		}
		_, _ = h.Write(summary.FactsHash[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// inputDigest returns the per-action InputDigest folded into the L1
// action ID. The shape depends on the analyzer's TypeUseScope:
//
//   - TypeUseFullTypeGraph (default): ph.key — the gopls reachability
//     hash. The prior behavior; any transitive content change in any
//     reachable dep propagates into the importer's ph.key by
//     construction, invalidating the L1 entry.
//   - TypeUseSyntaxOnly: ph.localKey — the package-local input hash
//     (sources + import map IDs + sizes + viewType), no dep content
//     composition. A comment-only edit to a dep leaves every
//     importer's localKey unchanged → L1 hits on the importers.
//   - TypeUseExportedTypesOnly: degrades to ph.key. The per-vdep
//     gcexportdata-keyed InputDigest is a follow-up (the export blob
//     is itself keyed by ph.key and only available post-typecheck,
//     so reusing it here requires separate plumbing).
//
// Variant separation (test vs non-test variant) rides on
// inputs.id (PackageID, encoded with the variant marker) and
// inputs.viewType, both already folded into localPackageKey.
func (act *action) inputDigest() [32]byte {
	ph := act.an.ph
	if act.depTypeUseScope() == analyzers.TypeUseSyntaxOnly {
		// Defensive: if localKey is unset (e.g. test scaffolds that
		// build a packageHandle without going through the check
		// pipeline), fall back to ph.key so the digest is still
		// well-defined. Production paths always populate localKey
		// before L1 derivation runs.
		if ph.localKey != (file.Hash{}) {
			return ph.localKey
		}
	}
	return ph.key
}

// depTypeDigest returns the per-action dep-type-use digest folded
// into the L1 action ID. The shape depends on the analyzer's
// descriptor TypeUseScope:
//
//   - TypeUseFullTypeGraph (default, unregistered analyzers): hash
//     every vdep's ph.key, sorted by vdepID. The prior behavior. Any
//     transitive dep change invalidates the L1 entry.
//   - TypeUseSyntaxOnly: a domain-tagged constant. The analyzer does
//     not read dep types, so the digest must not depend on vdeps;
//     only the package-local InputDigest (ph.key) keys the entry.
//     Cascade-edits to dep internals leave the digest unchanged →
//     L1 hits.
//   - TypeUseExportedTypesOnly: per-vdep exported-API hash. Not yet
//     wired (would need a separate per-vdep exported-symbol digest);
//     currently falls back to TypeUseFullTypeGraph. A follow-up.
//
// The "domain-tagged" preamble (the leading scope byte) ensures
// SyntaxOnly digests never collide with FullTypeGraph digests on
// the empty-vdep edge case, so accidentally toggling an analyzer's
// scope at the same engine version invalidates rather than hits.
func (act *action) depTypeDigest() [32]byte {
	scope := act.depTypeUseScope()
	h := sha256.New()
	// Domain tag: scope byte so SyntaxOnly's empty body and
	// FullTypeGraph's empty-vdep body produce distinct digests.
	_, _ = h.Write([]byte{byte(scope)})
	switch scope {
	case analyzers.TypeUseSyntaxOnly:
		// Constant per scope tag; no vdep contributions.
	default:
		// TypeUseFullTypeGraph and TypeUseExportedTypesOnly (until
		// the latter is wired) fall through to the prior path.
		var ids []string
		for id := range act.vdeps {
			ids = append(ids, string(id))
		}
		sort.Strings(ids)
		for _, id := range ids {
			vdep := act.vdeps[PackageID(id)]
			fmt.Fprintf(h, "%s\n", id)
			_, _ = h.Write(vdep.ph.key[:])
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// depTypeUseScope returns the effective TypeUseScope for act's
// analyzer. Unregistered analyzers, or descriptors that don't set the
// field, get TypeUseFullTypeGraph — the conservative default that
// preserves the prior behavior. TypeUseExportedTypesOnly is not yet
// wired and silently degrades to TypeUseFullTypeGraph; the descriptor-
// level opt-in is harmless until the per-vdep exported-API digest is
// implemented.
func (act *action) depTypeUseScope() analyzers.TypeUseScope {
	d := act.descriptor()
	if d == nil {
		return analyzers.TypeUseFullTypeGraph
	}
	switch d.TypeUseScope {
	case analyzers.TypeUseSyntaxOnly:
		return analyzers.TypeUseSyntaxOnly
	case analyzers.TypeUseExportedTypesOnly:
		// Not yet wired. Degrade safely.
		return analyzers.TypeUseFullTypeGraph
	default:
		return analyzers.TypeUseFullTypeGraph
	}
}

// bumpScopeHit increments the per-TypeUseScope hit counter on the
// batch's l1Metrics. Total Hits is incremented separately by the
// caller — these counters are a strict subset so SyntaxOnly hits +
// FullTypeGraph hits <= Hits (a hit on a yet-to-be-added scope falls
// outside both buckets, but that's a TODO marker, not double-count).
func (act *action) bumpScopeHit() {
	m := act.an.batch.l1Metrics
	if m == nil {
		return
	}
	switch act.depTypeUseScope() {
	case analyzers.TypeUseSyntaxOnly:
		m.syntaxOnlyHits.Add(1)
	case analyzers.TypeUseFullTypeGraph:
		m.fullTypeGraphHits.Add(1)
	}
}

// bumpScopeMiss increments the per-TypeUseScope miss counter. Same
// shape and caveat as bumpScopeHit.
func (act *action) bumpScopeMiss() {
	m := act.an.batch.l1Metrics
	if m == nil {
		return
	}
	switch act.depTypeUseScope() {
	case analyzers.TypeUseSyntaxOnly:
		m.syntaxOnlyMisses.Add(1)
	case analyzers.TypeUseFullTypeGraph:
		m.fullTypeGraphMisses.Add(1)
	}
}

// l1AnalyzerVersionStub is the W6 stub for AnalyzerVersion used as a
// fallback when the registry has no descriptor for an analyzer (e.g.
// in unit tests that bypass the registry). Production code paths land
// on analyzers.ProcessBinaryVersion via the descriptor.
func l1AnalyzerVersionStub(a *analysis.Analyzer) string {
	h := sha256.Sum256([]byte(a.Name))
	return fmt.Sprintf("%x", h[:8])
}

// tryL1Lookup attempts to read and decode the L1 entry for act. On
// success it returns the cached actionSummary and the cached Result
// value (if any); on any failure (miss, decode error, fact-decoder
// failure) it returns (nil, nil, false) and the caller falls through
// to the analyzer's body. The function records hit / miss / error
// counters on the batch's l1Metrics.
//
// On a hit the caller MUST treat the returned summary as immutable.
// Diagnostics are reconstituted from JSON; facts are decoded via the
// objectpath-based encoder in x/tools/internal/facts. The Result, when
// present, is decoded via the analyzer's descriptor ResultCodec.
func (act *action) tryL1Lookup(ctx context.Context) (any, *actionSummary, bool) {
	b := act.an.batch
	if b.l1 == nil {
		return nil, nil, false
	}
	id := act.l1ActionID()
	entry, err := b.l1.ReadL1(act.a.Name, id)
	if err != nil {
		if b.l1Metrics != nil {
			b.l1Metrics.misses.Add(1)
			act.bumpScopeMiss()
		}
		return nil, nil, false
	}

	// Decode diagnostics back into the cache's gobDiagnostic shape and
	// resolve any canonical-form URIs back to absolute file URIs using
	// the batch's PkgPath → dir map. Older entries (no canonical
	// URIs) pass through resolveGobDiagnostic unchanged.
	//
	// The on-disk URI for canonicalised entries is
	// "plaid-canonical:<pkgPath>/<basename>", but
	// protocol.DocumentURI.UnmarshalText rejects any scheme that is
	// not file://. Rewrite the raw JSON bytes so the unmarshal
	// succeeds; resolveURI recognises the rewritten sentinel form.
	_, pkgDirs := b.l1CanonicalMaps()
	var diagnostics []gobDiagnostic
	for _, raw := range entry.Diagnostics {
		var d gobDiagnostic
		if err := json.Unmarshal(rewriteCanonicalToFileForm(raw), &d); err != nil {
			if b.l1Metrics != nil {
				b.l1Metrics.errors.Add(1)
			}
			return nil, nil, false
		}
		resolveGobDiagnostic(&d, pkgDirs)
		diagnostics = append(diagnostics, d)
	}

	factsBlob := entry.ObjectFacts
	if !validateFactsBlob(act, factsBlob) {
		if b.l1Metrics != nil {
			b.l1Metrics.errors.Add(1)
		}
		return nil, nil, false
	}

	// Restore the cached Result for analyzers consumed as Requires
	// prerequisites by another action.
	//
	// Hit-safety: when the descriptor opts into Result caching (a
	// working codec), an L1 entry with no Result bytes cannot
	// satisfy a downstream consumer — returning ok=true here would
	// leave pass.ResultOf[prereq] nil and crash the consumer's Run
	// body. Treat empty-Result entries for Result-bearing
	// descriptors as a miss so the analyzer re-runs and produces a
	// fresh Result. (Result-less descriptors — those without a
	// codec — that are consumed as prereqs are gated upstream by
	// isPrerequisiteOfEnabled at action.exec.)
	var result any
	desc := act.descriptor()
	codecOptIn := desc != nil && desc.CanCacheResult()
	if len(entry.Result) > 0 {
		if codecOptIn {
			r, err := desc.ResultCodec.Decode(entry.Result)
			if err != nil {
				if b.l1Metrics != nil {
					b.l1Metrics.errors.Add(1)
				}
				return nil, nil, false
			}
			result = r
		}
	} else if codecOptIn {
		// Codec opt-in + empty Result section: bug-shape; refuse.
		if b.l1Metrics != nil {
			b.l1Metrics.misses.Add(1)
			act.bumpScopeMiss()
		}
		return nil, nil, false
	}

	if b.l1Metrics != nil {
		b.l1Metrics.hits.Add(1)
		act.bumpScopeHit()
	}
	_ = ctx
	return result, &actionSummary{
		Diagnostics: diagnostics,
		Facts:       factsBlob,
		FactsHash:   sha256Hash(factsBlob),
	}, true
}

// validateFactsBlob does a structural sanity check on the facts blob
// before handing it back.
func validateFactsBlob(act *action, blob []byte) bool {
	_ = act // reserved for future descriptor-based validation.
	return facts.IsWellFormed(blob)
}

// l1Store writes an L1 entry for act using the just-produced summary
// and (when the analyzer's descriptor opts in) the Run Result. Errors
// are recorded and swallowed: the L1 write is opportunistic.
//
// Skip-on-present: the L1 ID is derivable from the per-action header
// fields alone (Analyzer, PackageID, AnalyzerVersion, ConfigSalt,
// ToolVersion, InputDigest, DepFactsDigest, DepTypeDigest) — all of
// which are cheap to compute. When the on-disk file already exists
// the encode + write is elided; bytes-on-disk are content-addressed
// by construction so a present entry is valid for the same id.
// Mirrors the L2 skip-on-present.
func (act *action) l1Store(summary *actionSummary, result any) {
	if summary == nil {
		return
	}
	b := act.an.batch
	if b.l1 == nil {
		return
	}
	desc := act.descriptor()

	// Cheap skip path: compute just the header fields and the L1 ID,
	// then short-circuit on a stat hit. The Diagnostics / Facts /
	// Result encode work is the expensive part — keeping it behind
	// the HasL1 check is what makes this fix worthwhile.
	id := act.l1ActionID()
	if b.l1.HasL1(act.a.Name, id) {
		if b.l1Metrics != nil {
			b.l1Metrics.skipped.Add(1)
		}
		return
	}

	// Re-encode diagnostics as canonical JSON so the on-disk format is
	// stable across encoder changes. Walk every Location.URI through
	// the batch's canonicalize map first so the on-disk bytes do not
	// carry absolute machine-local paths. Diagnostics are
	// copied (not aliased) so the in-memory summary returned to the
	// engine still carries the absolute URI for its post-engine L0
	// canonicalisation pass.
	uriPkg, _ := b.l1CanonicalMaps()
	rawDiags := make([]json.RawMessage, 0, len(summary.Diagnostics))
	for _, d := range summary.Diagnostics {
		canon := cloneGobDiagnostic(d)
		canonicalizeGobDiagnostic(&canon, uriPkg)
		raw, err := json.Marshal(canon)
		if err != nil {
			if b.l1Metrics != nil {
				b.l1Metrics.errors.Add(1)
			}
			return
		}
		rawDiags = append(rawDiags, raw)
	}

	var resultBlob []byte
	if result != nil && desc != nil && desc.CanCacheResult() {
		blob, err := desc.ResultCodec.Encode(result)
		if err != nil {
			// Skip the L1 write entirely on encode failure. Writing
			// an entry with Result=nil for a Result-bearing
			// descriptor would let a future run hit L1 with
			// pass.ResultOf[prereq]=nil and crash the consumer's
			// Run body. We bump a dedicated EncodeFailures counter
			// so the operationally-unusual case is visible without
			// being conflated with read-side decode errors.
			if b.l1Metrics != nil {
				b.l1Metrics.encodeFailures.Add(1)
			}
			return
		}
		resultBlob = blob
	}

	entry := &clcache.L1Entry{
		Analyzer:        act.a.Name,
		PackageID:       string(act.an.ph.mp.ID),
		AnalyzerVersion: analyzerVersionFor(act.a, desc),
		ConfigSalt:      configSaltFor(act.a, desc),
		ToolVersion:     b.l1ToolVer,
		Diagnostics:     rawDiags,
		ObjectFacts:     summary.Facts,
		Result:          resultBlob,
	}
	entry.InputDigest = act.inputDigest()
	entry.DepFactsDigest = act.depFactsDigest()
	entry.DepTypeDigest = act.depTypeDigest()
	if err := b.l1.WriteL1(entry, id); err != nil {
		if b.l1Metrics != nil {
			b.l1Metrics.errors.Add(1)
		}
		return
	}
	if b.l1Metrics != nil {
		b.l1Metrics.stores.Add(1)
	}
}

// sha256Hash is a small helper that returns the SHA-256 of b as a
// file.Hash, mirroring file.HashOf without importing the file package
// directly (kept local so this file has minimal cross-imports).
func sha256Hash(b []byte) [32]byte {
	return sha256.Sum256(b)
}

// Silence unused-import linters if the import set ever loses its uses.
var _ = bytes.NewReader
