// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

// EngineCacheVersion is the cross-cutting cache-stamp folded into every
// L1 and L0 cache key alongside the per-descriptor CacheVersion. Bumped
// manually when an engine-, exclusion-, or cache-layout change
// invalidates every prior cache entry.
//
// Lives in the analyzers package (not internal/registry) so the gopls
// cache fork can fold it into the L1 key without importing the higher-
// level registry layer. The user-facing re-export is
// internal/registry.CacheVersion; both refer to this constant.
//
// Intentionally NOT auto-derived. See descriptor.AnalyzerDescriptor's
// CacheVersion field comment for the rationale and the motivating case.
//
// Bump history:
//   - 1: initial value.
//   - 2: DepTypeDigest is now scope-aware (SyntaxOnly
//     analyzers' digest no longer depends on vdep ph.key). Prior
//     entries for any analyzer would stale-hit (their stored
//     DepTypeDigest reflects the wider FullTypeGraph hash) under
//     the new key derivation, so every L1 entry from version 1 is
//     invalidated.
//   - 3: chain-1 narrowing — L1 InputDigest is now
//     scope-aware (SyntaxOnly uses ph.localKey, not ph.key). A
//     stored v2 entry for a SyntaxOnly analyzer would have keyed
//     InputDigest off the wider ph.key; under v3 derivation the
//     same logical action key off ph.localKey could in principle
//     collide with another v2 entry whose ph.key happened to equal
//     a new ph.localKey. The bump prevents that false-hit class.
//   - 4: Pos.Filename canonicalisation — engine now emits
//     <importPath>/<basename> instead of the absolute on-disk path.
//     Existing v3 L0 entries hold absolute paths that would fail
//     the cross-machine portability gate (the whole motivation for
//     the change). The bump invalidates every entry produced under
//     the pre-canonical encoding.
//   - 5: L1/L2 canonicalisation — localPackageKey now folds
//     the canonical "<pkgPath>/<basename>" form of each compiled-Go
//     file URI rather than the absolute one, so L1 ActionIDs (via
//     ph.localKey) and L2 ActionIDs (via ph.key) become machine-
//     portable. L1 entry values additionally canonicalise the
//     embedded gobDiagnostic Location.URI, and L2 entry values
//     canonicalise the gcexportdata FileSet. Existing v4 entries
//     hold absolute paths under the v4 key shape; the bump
//     invalidates them so the v5 keyspace is uncontaminated.
//   - 6: read-side workaround for the v5 canonicalisation regression — every
//     v5 diagnostic-bearing L1 entry was silently unreadable because
//     protocol.DocumentURI.UnmarshalText rejects the
//     "plaid-canonical:" scheme, so the warm read incremented
//     L1Metrics.Errors instead of Hits. The fix rewrites the raw
//     JSON bytes to a file-scheme sentinel before unmarshal; pre-fix
//     v5 entries on disk are valid under the new reader, but
//     bumping is still correct: any consumer pinned to v5 would
//     have been observing a broken cache, and v6 marks the read-
//     side fix point.
//   - 7: input-based cacheKey is now the default path
//     (PLAID_INPUT_DIGEST defaults to ON; set to "0" to revert).
//     Per-batch cacheKey derivation switches from output-based
//     (vdep.summaryHash, reads vdep.actions) to input-based
//     (vdep.inputDigest, reads only construction-time inputs); the
//     two derivations are semantically equivalent under the
//     analyzer-determinism contract but produce different cache
//     keys. The cross-deploy bump invalidates prior entries so a
//     warm process can't deliver a v6 entry against a v7 derivation.
//     Note: the in-process inputDigestDomain tag still namespaces
//     the two paths inside one process lifetime (rollback-safe
//     within a deploy).
//   - 8: L2 export-data blob format switches
//     from deep (gcexportdata.Write / IExportData(shallow=false))
//     to shallow (gcimporter.IExportShallow). The two blob formats
//     are NOT wire-compatible: IImportShallow rejects deep-format
//     blobs (it expects the shallow-only file-position table and
//     the single-pkg manifest), and gcexportdata.Read would mis-
//     decode shallow blobs the same way. v7 on-disk L2 entries
//     hold deep blobs that the v8 reader cannot parse, so the
//     bump invalidates the v7 L2 keyspace cleanly. L1 entries are
//     unaffected at the blob level but their ActionIDs fold
//     EngineCacheVersion so they invalidate too — that's
//     conservative (the L1 wire format didn't change) but matches
//     the cross-cutting stamp convention.
const EngineCacheVersion uint8 = 8
