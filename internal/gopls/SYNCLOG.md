# SYNCLOG: gopls fork sync log

> Tracks every upstream-sync event for the forked `gopls/internal/cache` tree. Reviewed on each Phase 1 rebase scan (cadence per rfc-v2 OQ #1).

## Pin: `a3954b5c7496c91c1095bd368722a4e80d793f28`

- **Date pinned:** 2026-05-16
- **Upstream:** `github.com/golang/tools` master
- **Commit message:** `gopls/internal/settings: move file watcher setting to user options`
- **Source path:** `gopls/internal/cache/`
- **Vendored to:** `internal/gopls/cache/`
- **License:** `internal/gopls/LICENSE.upstream` + `PATENTS.upstream` (BSD-3-Clause + Google Patents grant; preserved per Apache-2.0 / BSD-3-Clause compatibility).

### Scope (per ADR-4 + rfc-v2 §1)

**Vendored (~10 kLOC across 59 files):**
- Root cache files: `snapshot.go`, `check.go`, `analysis.go`, `package.go`, `parse.go`, `parse_cache.go`, `load.go`, `mod*.go`, plus support code (`cache.go`, `errors.go`, `filemap*.go`, `fs_*.go`, `keys.go`, `constraints.go`, `debug.go`, `diagnostics.go`, `filterer.go`, `future.go`, `imports.go`, `os_darwin.go`, `os_windows.go`).
- Subdirs: `metadata/`, `parsego/`, `typerefs/`, `symbols/`, `testfuncs/`.
- All `_test.go` retained — verifies the fork builds and tests pass (Phase 1 W2 exit criterion).

**Dropped at fork time** (per [[r2]] response, no Phase 1 analyzer consumes these):
- `methodsets/` (~500 LOC; gopls hover feature).
- `xrefs/` (~800 LOC; gopls find-references feature).

**Not yet vendored (Phase 1 W1–W2 transitive deps):**
- `gopls/internal/protocol` — currently imported transitively. URI/Location types will be replaced with `string`/`token.Position` in mechanical rewrites; the package itself does not get forked.
- `gopls/internal/file` — file abstraction; may need shim or fork.
- `gopls/internal/golang`, `gopls/internal/settings`, `gopls/internal/label` — likely shims; some may not be needed once LSP-specific code is dropped.

These will land as separate SYNCLOG entries during W1–W2 as the URI rewrites surface each dep.

### Shim packages

W1 created these shim packages under `internal/gopls/`. Each carries
only the surface the forked cache references; LSP-specific code is
omitted.

- `bloom/` — copied as-is from upstream `gopls/internal/bloom`.
- `file/` — copied as-is from upstream `gopls/internal/file`.
- `filecache/` — copied as-is from upstream `gopls/internal/filecache`.
- `label/` — copied as-is from upstream `gopls/internal/label`.
- `progress/` — **stub**, ~25 LOC. Carries `Tracker` and `WorkDone`
  with no-op methods (`SupportsWorkDoneProgress`, `Start`, `End`,
  `Report`). The CLI does not emit LSP progress notifications.
- `protocol/` — pruned copy of upstream `gopls/internal/protocol`.
  Kept: `uri.go`, `mapper.go`, `span.go`, `edits.go`, `enums.go`,
  `form.go`, `helpers.go` (UnmarshalJSON + NonNilSlice extracted from
  upstream protocol.go), `tsdocument_changes.go`,
  `tsinsertreplaceedit.go`, `tsprotocol.go` (the type definitions),
  `doc.go`. Dropped: `protocol.go`, `context.go`, `log.go`,
  `tsclient.go`, `tsserver.go`, `tsjson.go` (all LSP-RPC wire-format).
- `protocol/command/` — copied (`command_gen.go`, `interface.go`,
  `util.go`). Drops `generate.go` and the test-only `commandmeta/`,
  `gen/` subdirs.
- `settings/` — **stub**, ~120 LOC (`settings.go`). Carries
  `Options`/`BuildOptions`/`UIOptions`/`InternalOptions`/`UserOptions`
  with only the fields cache reads (~16 fields), `Analyzer` with
  Enabled/ActionKinds/Severity/Tags/String, and `SubdirWatchPatterns*`
  constants. `AllAnalyzers` is an empty slice. The 1700-LOC upstream
  `settings.go` plus `analysis.go`, `default.go`, `staticcheck.go`,
  `codeactionkind.go` were *not* copied.
- `util/bug,constraints,frob,immutable,memoize,moremaps,pathutil,persistent,safetoken,tokeninternal,lru/`
  — copied as-is from upstream `gopls/internal/util/*`.
- `vulncheck/` — **stub**, single-field `Result` struct with no fields.
  Only present because `protocol/command/interface.go` mentions it in
  the (otherwise unused) command interface.
- `internal/` — copied from upstream `golang.org/x/tools/internal/*`:
  `aliases`, `analysis/driverutil`, `astutil`, `astutil/free`, `diff`,
  `diff/difftest`, `diff/lcs`, `event`, `event/core`, `event/export`,
  `event/export/eventtest`, `event/keys`, `event/label`, `expect`,
  `facts`, `gcimporter`, `gocommand`, `goroot`, `gopathwalk`,
  `imports`, `jsonrpc2`, `jsonrpc2/stack`,
  `jsonrpc2/stack/stacktest`, `jsonrpc2_v2`, `modindex`, `moreiters`,
  `packagesinternal`, `packagestest`, `pkgbits`, `proxydir`,
  `robustio`, `stdlib` (with testdata), `testenv`, `typeparams`,
  `typesinternal`, `versions`. Necessary because Go's
  internal-package rule blocks our module from importing
  `golang.org/x/tools/internal/*` directly. See NOTES.md D-5.

### Files deleted at fork

Category-C (per [[r2]]; replaced by `WorkspaceState` in W3):
- `cache/session.go`, `cache/session_test.go`
- `cache/view.go`, `cache/view_test.go`
- `cache/workspace.go`

Out-of-scope (vulncheck is not a lint engine concern):
- `cache/mod_vuln.go`

### New files added

- `cache/view_stub.go` — carrier stubs for the deleted Category-C
  types. See NOTES.md D-1. Replaced wholesale by `WorkspaceState`
  in W3.

### Delta inventory (against upstream a3954b5c)

Format: `- <file>: <delta>`.

- `cache/snapshot.go`:
  - Dropped imports of `cache/methodsets`, `cache/xrefs`, `vulncheck`,
    `go/types/objectpath` (unused after methodsets/xrefs/vulncheck
    removal).
  - Removed `vulns` field, `vulns.Destroy()` call, and `vulns:` /
    `changed.Vulns` from the `Snapshot.clone()` body.
  - Removed `References`, `MethodSets` methods, `xrefIndex` type,
    `xrefsKind` / `methodSetsKind` constants. The CLI engine does
    cross-reference resolution differently.
- `cache/package.go`:
  - Dropped `methodsets`/`xrefs` imports and the corresponding
    `syntaxPackage.xrefs()` / `methodsets()` methods plus the
    `_xrefs`, `_methodsets`, `xrefsOnce`, `methodsetsOnce` fields.
- `cache/check.go`:
  - In `storePackageResults`, removed the `xrefsKind` and
    `methodSetsKind` entries from `toCache`.
- *(all `cache/*.go` files)*: import paths rewritten from
  `golang.org/x/tools/gopls/internal/...` to
  `github.com/conductorone/plaid-lint/internal/gopls/...`.

### Pending transitive deps

None as of W1 close: every gopls-internal import has either a shim
package (this tree), a stub, or a dropped call site.

### Tests known to break post-fork

None at W1 close. `go test ./internal/gopls/cache/...` is green.

### Build / test status (W1 close — 2026-05-16)

- `go build ./internal/gopls/cache/...` — **PASS**
- `go build ./...` — **PASS**
- `go vet ./internal/gopls/cache/...` — **PASS**
- `go test ./internal/gopls/cache/...` — **PASS** (4 tested
  packages, 2 with no test files)

## W3 (2026-05-17): view_stub.go → WorkspaceState swap

### File-level changes

- `cache/view_stub.go` → **renamed** to `cache/view.go`. The file
  is no longer a stub; it holds the package-internal View/Folder/
  Session carrier types that the public WorkspaceState wraps. See
  NOTES.md D-6 for the placement rationale.
- Header docstring rewritten to drop the "carrier stub, will be
  replaced wholesale in W3" framing.
- Per-method docstrings tagged `Carrier returns nil/zero…` were
  reworded to describe the actual semantics (still minimal for
  fields the CLI engine doesn't drive, but no longer "will be
  filled in by W3").
- `View.snapshotMu` and `View.snapshot` fields **removed**. They
  were never read; the snapshot lifecycle now lives entirely in
  `internal/workspace.WorkspaceState`.

### New cache-package files

- `cache/lifecycle.go` — public construction surface the
  `internal/workspace` package calls:
  - `NewView(c *Cache, moduleRoot, opts) (*View, *Snapshot)` —
    builds a fully-initialised View with its initial Snapshot
    (refcount=1, all persistent maps fresh).
  - `CloneSnapshot(ctx, base, change, done)` — thin wrapper over
    the existing `Snapshot.clone()` so external code can produce
    successor snapshots.
  - `ApplyOverlay(v, uri, content, version, kind) file.Handle` —
    mutates the View's overlayFS in place. Returns the new
    overlay handle for the caller to thread into a StateChange.
  - `View.ReadFile(ctx, uri)` — overlay-aware passthrough.
  - `View.Shutdown()` — stops the parseCache GC goroutine and
    cancels the initial-workspace-load context.
  - `SnapshotIDComponents(s) (viewID, sequenceID)` — exposes the
    pair used to compose wire-format snapshot IDs (NOTES.md D-8).
  - `ReleaseInitialRef(s)` — the only sanctioned way for code
    outside the cache package to drop the "born referenced" ref
    on a freshly-constructed Snapshot.

### Snapshot lifecycle wiring

- `newInitialSnapshot`'s `done` callback is now a no-op. The
  parseCache GC goroutine, which the upstream snapshot.decref
  stopped via `s.done`, is now owned by `View.Shutdown` (the
  parseCache is a View-level resource shared by every Snapshot
  derived from that View, so per-snapshot teardown was always
  wrong; W1's carrier code papered over it because no snapshot
  was ever actually decref'd to zero in the test harness).

### settings shim

- Added `settings.DefaultOptions()` returning an `*Options` with
  the workspace-files glob list pre-populated. WorkspaceState
  calls this when the caller passes a nil options pointer.

### New package: internal/workspace

- `internal/workspace/workspace.go` — implements
  `WorkspaceState` per rfc-v2 §2:
  - `New(moduleRoot string) *WorkspaceState`
  - `SetContent(path string, content []byte)` — stages overlay
    via `cache.ApplyOverlay`; clears overlay when content==nil.
  - `Invalidate(paths []string) string` — produces a new
    `cache.CloneSnapshot`, releases the prior WS-owned ref,
    returns the new snapshot's wire-format id.
  - `Snapshot() *Snapshot` — refcounted handle; nil if closed.
  - `Snapshot.Release() error` — double-release returns
    `ErrReleased`.
  - `Close()` — releases WS-owned ref on current snapshot and
    calls View.Shutdown. Outstanding external refs keep the
    snapshot alive until released.
- `internal/workspace/workspace_test.go` — covers overlay
  visibility, Invalidate id semantics, refcount lifecycle, old
  snapshot still-readable invariant, double-release error,
  Close idempotency, overlays surviving across snapshots, and
  concurrent snapshot/release/invalidate under `-race`.

### Build / test status (W3 close — 2026-05-17)

- `go build ./...` — **PASS**
- `go vet ./internal/workspace/... ./internal/gopls/cache/...` —
  **PASS**
- `go test ./internal/workspace/...` — **PASS**
- `go test -race ./internal/workspace/...` — **PASS**
- `go test ./internal/gopls/cache/...` — **PASS**
- `go test -race ./internal/gopls/cache/...` — **PASS**
- `go test ./...` — preexisting failures in
  `internal/gopls/internal/{expect, gcimporter, modindex}` from
  vendored upstream test data that needs external SDK / network
  access; not introduced by W3.

### Next sync ceremony

- Quarterly per rfc-v2 OQ #1 default; escalate on security CVE.
- First scheduled: 2026-08-16.
- Owner: project lead until handed off.

---

## W4 addendum (2026-05-17): `internal/cache/` is NOT a gopls fork

The new `internal/cache/` package introduced in W4 is the plaid-lint
content-addressed cache (rfc-v2 §3, L1/L2 entries). It is **separate from
the upstream-vendored `internal/gopls/cache/`** in this SYNCLOG and is
NOT subject to the quarterly rebase ceremony documented above.

- `internal/cache/` — plaid-lint-original code (no upstream).
- `internal/gopls/cache/` — vendored from `golang.org/x/tools/gopls/internal/cache`
  at pin `a3954b5c…`, tracked by this SYNCLOG.

The two are not connected at the type level. The plaid-lint cache
stores results computed *by* the gopls fork (typecheck export blobs in
L2; analyzer output in L1) but holds no gopls types in its public
surface — only `[32]byte` digests, `[]byte` opaque blobs, and
`json.RawMessage` diagnostics.

## W5 (2026-05-17): L2 wiring into the cache.Snapshot type-check path

### Forked-cache delta (against the W4-closed state)

- `cache/cache.go`:
  - Added optional `l2 *clcache.Cache` (alias for
    `github.com/conductorone/plaid-lint/internal/cache`), plus
    `l2BuildEnv`, `l2GoVersion`, `l2ToolVer` strings and an
    `l2Metrics` (atomic int64 counters).
  - Public setter `Cache.AttachL2(l2 *clcache.Cache, buildEnv,
    goVersion, toolVer string)` installs the L2 store on a Cache.
    Designed to be called by `internal/workspace` after `cache.New`
    but before the Cache is used to build a View. Nil-L2 is the
    default; AttachL2 is opt-in.
  - Public accessor `Cache.L2Metrics() L2Metrics` returns a snapshot
    of the hit / miss / store / error counters. Diagnostic-only;
    never affects behaviour.
- `cache/view.go`:
  - Added `c *Cache` back-reference on `View`. The cache-package
    `View` type historically held no `*Cache` pointer (upstream
    relied on the Session layer for this). W5 needs to reach
    `Cache.l2` from `Snapshot.acquireTypeChecking` without threading
    a `*Cache` argument through every batch caller; storing the
    back-reference is the smallest change.
- `cache/lifecycle.go`:
  - `NewView` now wires the new `View.c` field.
- `cache/check.go`:
  - `typeCheckBatch` gained five new fields: `l2`, `l2BuildEnv`,
    `l2GoVersion`, `l2ToolVer`, `l2Metrics`. They are populated in
    `Snapshot.acquireTypeChecking` from the View's Cache.
  - `typeCheckBatch.tryL2Lookup(ph)` — looks up the L2 entry for
    `ph`, decodes the gcexportdata blob directly into `b.fset` per
    NOTES.md D-16 (option A), returns `(*types.Package, true)` on
    success. Errors are recorded in `l2Metrics` and converted to a
    fall-through.
  - `typeCheckBatch.l2Store(ph, pkg)` — writes a new L2 entry for
    `(ph, pkg)`. Encodes `b.fset` into `L2Entry.FileSetSnapshot` so
    cross-process consumers can recover positions; failures fall
    through.
  - `typeCheckBatch.l2ActionID(ph)` — folds `ph.key`,
    `ph.mp.ID`, and the batch's BuildEnv / GoVersion / ToolVer into
    the L2 action ID. `ph.key` is the gopls reachability hash
    already covering source-content + transitive deps; we drop it
    into `L2Entry.InputDigest` and leave `L2Entry.DepTypeDigest`
    zero (see NOTES.md D-15).
  - `getImportPackage` consults `tryL2Lookup` *before* the existing
    `filecache.Get(exportDataKind, ...)` call. On miss or any error,
    the existing path runs unchanged.
  - `storePackageResults` writes a parallel L2 entry alongside the
    existing `filecache.Set(exportDataKind, ...)` call. Signature
    changed: it now takes `(ctx, b *typeCheckBatch, ph, p)` instead
    of `(ctx, ph, p)`. Internal caller in `getPackage` was updated.
  - `checkPackageForImport` mirrors the same L2 write in its
    async-recorder goroutine.

### Invariants preserved

- Nil-L2 short-circuit: every L2 hook is a no-op when `b.l2 == nil`,
  so a `Cache` constructed without `AttachL2` is bit-for-bit
  equivalent to W4-close.
- Same-action invariant for L2 writes is delegated to the
  `plaid-lint/internal/cache` layer; the gopls wiring only
  constructs `L2Entry` values with deterministic field contents.
- The legacy `filecache`-backed `IExportShallow`/`IImportShallow`
  path remains the source of truth for the in-process gopls
  pipeline. L2 is purely additive: it sits in front of the
  filecache for reads and beside it for writes.

### Tests added

- `cache/l2wiring_test.go`:
  - `TestL2WiringRoundTrip` — store a real `*types.Package` via
    `l2Store`, look it up via `tryL2Lookup`, verify the rehydrated
    package preserves all exported names. Metrics tick once each
    for miss → store → hit.
  - `TestL2WiringDisabled` — nil-`l2` short-circuit works without
    panicking on nil metrics.
  - `TestAttachL2` — exported setter records fields correctly and
    initial metrics snapshot is zero.

### Tests known to break post-W5

None. Full `go test -race ./internal/cache/... ./internal/workspace/...
./internal/gopls/cache/...` PASSes.

### Build / test status (W5 close — 2026-05-17)

- `go build ./...` — PASS
- `go vet ./internal/cache/... ./internal/workspace/... ./internal/gopls/cache/...` — PASS
- `go test -race -count=1 ./internal/cache/...` — PASS
- `go test -race -count=1 ./internal/workspace/...` — PASS
- `go test -race -count=1 ./internal/gopls/cache/...` — PASS

### W5 gate evidence

See `NOTES.md` D-19 (W5 gate outcome). Headline: 4-analyzer
equivalence test PASS, position-fidelity PASS, agent recommendation
GO. Project-lead decision pending.

## W5-fix (2026-05-17): shared imports map + AttachL2 setup-time guard

Follow-up to Codex's W5 review. Three changes; all confined to the
forked `internal/gopls/cache/` tree (relevant for the quarterly
rebase scan).

### Forked-cache delta (against the W5-closed state)

- `cache/check.go`:
  - `typeCheckBatch` gained two fields: `l2Imports map[string]*types.
    Package` and `l2ImportsMu sync.Mutex`. The map is the
    canonicalization table handed to every `gcexportdata.Read` call
    in this batch (one map per batch, lazily created on first L2
    hit) so transitive package references in two L2-cached deps
    resolve to the same `*types.Package` pointer for any shared
    third package. The pre-fix code allocated a fresh map per L2
    read, which silently produced distinct identities for the
    shared dep — the rfc-v2 §5 cache must preserve identity for
    downstream analyzers to compare types across deps. See
    `NOTES.md` D-20.
  - `tryL2Lookup` now locks `l2ImportsMu` for the `gcexportdata.
    Read` call (the function mutates the imports map). Lazy
    initialization happens inside the same critical section.
- `cache/cache.go`:
  - `Cache` gained `viewCount atomic.Int64`. Used solely to enforce
    the AttachL2 setup-time contract (see below).
  - `AttachL2` doc rewritten to state that the function must be
    called before any View is created; a panic guards the contract.
    See `NOTES.md` D-21.
- `cache/lifecycle.go`:
  - `NewView` increments `c.viewCount` so a later `AttachL2` call
    panics. Cost is one atomic increment per View construction;
    Views are constructed once per workspace open, so the cost is
    negligible.

### Non-fork delta (plaid-lint-original code)

- `internal/cache/l2.go`: `L2Entry.FileSetSnapshot` doc-comment
  expanded. Clarifies that the field is future-proofing for
  cross-process consumers (W8); the in-process L2 read path
  ignores it and decodes ExportData straight into the batch's
  master FileSet. No behaviour change.

### Tests added

- `cache/l2_identity_test.go::TestL2SharedImportIdentity` — three-
  package fixture (`pkgc` shared dep; `pkga` and `pkgb` both
  exporting types referencing `pkgc.Token`). Pre-populates L2 with
  `pkga` and `pkgb`, performs two L2 reads in one batch, asserts
  the `*types.Package` for `pkgc` referenced by `pkga.GetToken`'s
  return type is the same pointer as the one referenced by
  `pkgb.TakeToken`'s parameter type. The test fails on the pre-fix
  code and passes on the post-fix code (verified by stash-revert
  cycle).

### Build / test status (W5-fix close — 2026-05-17)

- `go build ./...` — PASS
- `go vet ./internal/cache/... ./internal/workspace/... ./internal/gopls/cache/...` — PASS
- `go test -race -count=1 ./internal/cache/...` — PASS
- `go test -race -count=1 ./internal/workspace/...` — PASS
- `go test -race -count=1 ./internal/gopls/cache/...` — PASS

## W6 (2026-05-17): L1 per-(package, analyzer) cache wiring + pipeline equivalence

This entry covers the W6 changes inside the gopls fork
(`internal/gopls/`). For the non-fork delta — the L1 actionID helper
on `internal/cache/L1Entry` and the pipeline equivalence test — see
the corresponding commits on `phase1-w6-l1-cache`.

### Fork delta

- `cache/cache.go`:
  - `Cache` gained `l1 *clcache.Cache`, `l1ToolVer string`, and
    `l1Metrics l1Metrics` (mirroring the existing L2 fields).
  - New `L1Metrics` snapshot type (`Hits/Misses/Stores/Errors`).
  - New `Cache.AttachL1(l1, toolVer)` setter, panicking on post-View
    invocation just like `AttachL2` (same `viewCount` guard).
  - New `Cache.L1Metrics()` returning a snapshot. See `NOTES.md`
    D-22 for the insertion-point decision.
- `cache/check.go`:
  - `typeCheckBatch` gained `l1 *clcache.Cache`, `l1ToolVer string`,
    `l1Metrics *l1Metrics`. The batch construction in
    `acquireTypeChecking` copies these from the owning View's Cache
    (mirrors the W5 L2 wiring shape).
- `cache/analysis.go`:
  - `action` gained an `an *analysisNode` back-pointer (set by
    `mkAction`) so `exec` can reach the batch (for the L1 store)
    and the package's reachability hash (for the L1 actionID).
  - `action.exec` now consults L1 before running the analyzer body:
    on hit, the cached `actionSummary` is returned and the
    analyzer's `Run` function is skipped entirely. On miss, the
    analyzer runs and the result is written to L1 via `l1Store`
    inline (a cheap small-blob write).
  - `runCached` bypasses gopls's own analyzeSummary filecache when
    the batch has L1 attached (`NOTES.md` D-24). Without this the
    filecache short-circuits the cold-run path and L1 is never
    exercised.
- `cache/l1.go` (new):
  - `action.l1ActionID()` — derives the L1 content-addressed key per
    rfc-v2 §4 from the analyzer name + ph.key (input) + per-vdep
    FactsHash (DepFactsDigest) + per-vdep ph.key (DepTypeDigest) +
    AnalyzerVersion + ConfigSalt + ToolVersion.
  - `action.depFactsDigest()` — SHA-256 over (PackageID, FactsHash)
    pairs across vdeps, sorted by PackageID for determinism.
  - `action.depTypeDigest()` — SHA-256 over (PackageID, ph.key)
    pairs across vdeps, sorted by PackageID. Conservative
    stand-in for AnalyzerDescriptor.DepsTypeUse (W7).
  - `action.tryL1Lookup` / `action.l1Store` — the lookup/store
    triad. On lookup, the diagnostics are deserialized from JSON
    and the facts blob is structurally validated via
    `facts.IsWellFormed`. On store, diagnostics are re-serialized
    as canonical JSON.
  - `l1AnalyzerVersion(a)` — W6 stub: first 8 hex chars of
    sha256(analyzer.Name). W7's AnalyzerDescriptor replaces this
    (NOTES.md D-26).
- `cache/lifecycle.go`:
  - `NewView` detects `go.mod` at the module root and sets
    `view.typ = GoModView` + seeds `workspaceModFiles` so the load
    path's `isWorkspacePackageLocked` admits packages from the
    module. Without this the W6 pipeline test (and any CLI user
    pointing plaid-lint at a real module) gets only the root
    package and the analyzer set never sees sub-packages. W3 left
    `view.typ` pinned to AdHocView.
- `cache/view.go`:
  - `View.Env` now returns `os.Environ()` instead of `nil` (the W3
    stub). The earlier stub blocked subprocess `go list` calls
    from finding GOCACHE.
  - New `Snapshot.InitializeWorkspace(ctx)` — explicit
    first-load entry point. W3 left the `initialize()` path as a
    stub that only closed the `initialWorkspaceLoad` channel
    without doing a `s.load`. Production callers (the W6 pipeline
    test, and later the CLI engine) call InitializeWorkspace to
    actually run `s.load(viewLoadScope)` so the metadata graph
    is populated before any caller blocks in `awaitLoaded`.
- `internal/gopls/internal/facts/facts.go`:
  - Added `facts.IsWellFormed(b)` — structural well-formedness
    check on an encoded facts.Set blob. Used by the W6 L1 read
    path to reject corrupt entries without needing a
    *types.Package for full decoding.
- `settings/settings.go`:
  - Added `settings.NewAnalyzer(a)` — minimal constructor for
    wrapping a `*analysis.Analyzer` as a `*settings.Analyzer`.
    Tests register the pipeline-analyzer set via this. Production
    `AnalyzerDescriptor` (W7) replaces it.

### Non-fork delta (plaid-lint-original code)

- `internal/cache/l1.go`:
  - `ComputeL1ActionID(*L1Entry)` — folds the L1Entry header into
    the canonical SHA-256 action ID.
  - `ConfigSaltForAnalyzer(name, canonical)` — W6 stub for
    per-linter ConfigSalt. W7 replaces with the schema-aware
    canonicalizer (r7, rfc-v2 §7).
- `internal/workspace/workspace.go`:
  - `NewWithCache(moduleRoot, c)` — like `New` but accepts a
    pre-configured `*cache.Cache`. Used by the W6 gate test to
    attach an L1 store before any View is created.

### Tests added

- `internal/cache/l1_actionid_test.go` — determinism, sensitivity
  (Analyzer / PackageID / InputDigest / DepFactsDigest /
  DepTypeDigest / AnalyzerVersion / ConfigSalt / ToolVersion each
  individually move the ID), and outputs-don't-leak (Diagnostics,
  ObjectFacts, PackageFacts insensitive).
- `internal/gopls/cache/l1wiring_test.go` — round-trip via the
  batch's tryL1Lookup/l1Store triad on a synthesized action;
  sensitivity probes on action ID inputs; AttachL1 setter and
  post-View panic guard.
- `internal/pipelinetest/l1_pipeline_test.go` — the W6 gate
  evidence: drives `Snapshot.Analyze` end-to-end against a
  4-package on-disk go-module fixture, asserts cold→warm
  diagnostic equivalence and ≥95% L1 hit rate.
- `internal/pipelinetest/doc.go` — package doc; pipelinetest is
  test-only and exists to host cross-package integration tests
  that would create an import cycle if placed in `internal/cache`
  or `internal/workspace` directly.

### Build / test status (W6 close — 2026-05-17)

- `go build ./...` — PASS
- `go vet ./internal/cache/... ./internal/gopls/cache/... ./internal/workspace/... ./internal/pipelinetest/...` — PASS
- `go test -race -count=1 ./internal/cache/...` — PASS (1.3s)
- `go test -race -count=1 ./internal/gopls/cache/...` — PASS (10.0s)
- `go test -race -count=1 ./internal/workspace/...` — PASS (1.0s)
- `go test -race -count=1 ./internal/pipelinetest/...` — PASS (1.1s)
