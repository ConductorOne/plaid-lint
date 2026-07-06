// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"sync/atomic"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
)

// inputDigestEnabled reports whether the input-based cacheKey
// path AND the typecheck-before-analyze barrier are engaged.
//
// Default ON. When the path is enabled,
// analysisNode.cacheKey routes through cacheKeyInputBased instead of
// cacheKeyOutputBased — the resulting key is computable BEFORE any
// vdep finishes runCached, which is the structural prerequisite the
// phase-ordering barrier rides on. The L1 ActionID's
// DepFactsDigest field is unaffected.
//
// Set PLAID_INPUT_DIGEST=0 to revert to the prior path (single-
// pass enqueue, output-based cacheKey, no barrier). Unset is the
// default-ON case.
//
// Cache namespacing: the on-disk L1 key shape is identical across
// flag states (L1 keys do not flow through cacheKey), but the
// in-process fullAnalysisKeys / factyAnalysisKeys / inFlightAnalyses
// maps and any shared filecache lookups partition the keyspace
// differently. cacheKeyInputBased folds inputDigestDomain into its
// hash to isolate from cacheKeyOutputBased entries inside one cache
// root (rollback-safe).
func inputDigestEnabled() bool {
	if v, ok := os.LookupEnv("PLAID_INPUT_DIGEST"); ok {
		return v != "0"
	}
	return true
}

// inputDigestVerifyEnabled reports whether dual-path verify mode is on.
// Only active when inputDigestEnabled() is true. Computes BOTH the new
// input-based digest and the legacy output-based digest, then asserts
// neither is unreachable for the wrong reason. The two are NOT
// bit-equivalent (one hashes inputs, the other outputs); verify mode
// instead asserts that the input-derivation does not read post-exec
// state, and surfaces structural surprises (e.g. an analyzer's
// FactsHash differing across runs of identical inputs) via the
// determinism instrumentation in inputDigest.
func inputDigestVerifyEnabled() bool {
	return inputDigestEnabled() && os.Getenv("PLAID_INPUT_DIGEST_VERIFY") == "1"
}

// inputDigestDomain is folded into cacheKeyInputBased to isolate
// flag=1 entries from flag=0 entries in any in-process keyspace shared
// across the two paths. Independent of EngineCacheVersion: a flag flip
// inside one process lifetime would otherwise alias a stale flag=0
// entry to a flag=1 derivation.
const inputDigestDomain = "plaid:r31b:input-digest:v1\n"

// vdepActionRead is incremented every time the cacheKey path reads
// vdep.actions. Under flag=1 it MUST stay at zero for any node whose
// cacheKey ran before the vdep finished runCached — that's the
// structural decoupling the refactor is claiming. The hook is
// read-only at runtime; tests reset it via vdepActionReadReset and
// assert vdepActionReadCount() == 0 after the cacheKey path runs under
// flag=1. Production behavior is unchanged whether the counter is read
// or not.
var vdepActionRead atomic.Uint64

func vdepActionReadReset()           { vdepActionRead.Store(0) }
func vdepActionReadCount() uint64    { return vdepActionRead.Load() }
func vdepActionReadObserve(n uint64) { vdepActionRead.Add(n) }

// inputDigestHook, when non-nil, is invoked at the tail of
// (*analysisNode).inputDigest and may mutate the returned hash. The
// production path leaves the hook nil; only tests install one, and only
// to drive TestInputDigest_VerifyMode_DetectsDivergence. Guarded by
// inputDigestEnabled — the hook is only consulted on the new path.
var inputDigestHook func(an *analysisNode, h file.Hash) file.Hash

// outputDigestHook is the parallel injection point for
// summaryHash-driven derivations. Same constraints as inputDigestHook;
// only tests install one.
var outputDigestHook func(an *analysisNode, h file.Hash) file.Hash

// verifyDivergenceError formats the panic message verify mode raises
// when consecutive invocations of inputDigest or summaryHash produce
// different bytes for the same node — a determinism violation.
func verifyDivergenceError(kind string, an *analysisNode, a, b file.Hash) string {
	return fmt.Sprintf(
		"PLAID_INPUT_DIGEST_VERIFY: %s non-determinism on package %q: %x != %x",
		kind, an.ph.mp.PkgPath, a[:], b[:],
	)
}

// inputDigest computes a per-node digest from inputs known BEFORE
// the node's execActions runs. Used by cacheKeyInputBased when
// PLAID_INPUT_DIGEST=1 is set.
//
// Inputs folded:
//   - an.ph.key (the reachability hash: this package's local content
//     plus the transitive content of every reachable dep).
//   - an.analyzers stableName set, sorted (the analyzer set ID).
//   - analyzers.EngineCacheVersion (the engine-level cache stamp).
//
// Not folded here: AnalyzerVersion + ConfigSalt per-descriptor. These
// already participate in the L1 ActionID composition (l1.go: l1ActionID);
// at the cacheKey level the node-wide analyzer set ID is enough because
// the engine version + ph.key carry the cross-cutting invalidation
// signal that cacheKey is responsible for. The L1 ID still narrows
// per-action.
//
// Determinism claim: this function reads only fields set at analysisNode
// construction time, so the digest is computable BEFORE any of the
// node's vdeps have called runCached. TestAnalyzerDeterminism_
// NRepeats pins that the same inputs produce byte-identical outputs
// across runs for every analyzer in BundledRegistry, which is the
// substitution claim the input-based cacheKey relies on.
func (an *analysisNode) inputDigest() file.Hash {
	h := sha256.New()
	// (A) package identity. Mirrors summaryHash's "dep:" line.
	fmt.Fprintf(h, "dep: %s\n", an.ph.mp.PkgPath)
	// (B) reachability content hash: package source plus transitive dep
	// content. ph.key is set at handle build, well before any analyzer
	// has run.
	h.Write(an.ph.key[:])
	// (C) analyzer set ID. an.analyzers is set at construction
	// (analysis.go: makeNode). Sort by stableName so the digest is
	// stable across slice orderings.
	names := make([]string, 0, len(an.analyzers))
	for _, a := range an.analyzers {
		if n, ok := an.stableNames[a]; ok {
			names = append(names, n)
		} else {
			names = append(names, a.Name)
		}
	}
	sort.Strings(names)
	fmt.Fprintf(h, "analyzers: %d\n", len(names))
	for _, n := range names {
		fmt.Fprintln(h, n)
	}
	// (D) engine cache version. Bumped manually per cache_version.go.
	fmt.Fprintf(h, "engine: %d\n", analyzers.EngineCacheVersion)

	var out file.Hash
	h.Sum(out[:0])
	if hook := inputDigestHook; hook != nil {
		out = hook(an, out)
	}
	return out
}
