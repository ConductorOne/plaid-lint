# plaid-lint vs golangci-lint v2.9.0 — c1 full-repo residual triage (post-D-107)

**Target:** full c1 corpus `./...` from `/data/squire/src/c1/`
**Date:** 2026-05-23
**plaid HEAD at baseline:** `residual-divergence-triage` off main `28db9f0`
**c1 HEAD at run:** `6d09432a07` (current main, dirty: `M pkg/c1semconv/ratelimit.go`)
**golangci-lint:** v2.9.0
**Subprocess linter set disabled** via `-D gochecknoinits -D dogsled -D dupl -D gocyclo -D godox -D lll -D nestif -D unconvert` (D-101 BLOCKER-1; the dispatch's documented `PLAID_DISABLE` env var does not actually exist in plaid — the right knob is `-D <linter>` repeated on the CLI). Cache wiped before each cold measurement.

## Headline

D-107 closed >99% of post-D-106 divergence on sampled scopes but left ~30 estimated residuals on full c1. The actual full-c1 measurement at the start of D-113 was **16 plaid-only diagnostics**. Six classes; three fix-class (a) wins (govet sub-analyzer attribution); one fix-class (b) win (gosec G124 library skew); one fix-class (b) win (noctx httptest.NewRequest library skew); one residual deferred as fix-class (c) (goconst cross-file scope).

| Class | Pre-fix | Verdict | Fix | Post-fix |
|---|---:|---|---|---:|
| copylocks (govet sub-analyzer) | 2 | (a) plaid bug | govet family alias in familyByPrefix | 0 |
| waitgroup (govet sub-analyzer) | 1 | (a) plaid bug | same govet family alias | 0 |
| gosec G124 | 2 | (b) library skew v2.26 vs v2.22 | add to gosec AnalyzerFilter | 0 |
| noctx httptest | 10 | (b) library skew v0.5.1 vs v0.4.0 | post-filter by message | 0 |
| goconst "pending" cross-file | 1 | (c) accept; document | — | 1 |
| **Total** | **16** | | | **1** |

golangci-lint v2.9.0 reports **0 issues** on full c1 (7m53s wall on a 64-core box). Post-fix plaid reports **1** — the one accepted residual.

## Residual classes (full c1)

### 1. govet sub-analyzer attribution (3 hits: 2 copylocks + 1 waitgroup) — fix-class (a)

**Root cause.** plaid attributes a govet sub-analyzer's diagnostic to the sub-analyzer's own name (e.g. `Linter: copylocks`). golangci-lint v2 attributes every govet sub-analyzer diagnostic to the umbrella `Linter: govet` (see `pkg/goanalysis/runners.go:131` — `FromLinter: linterName`, where `linterName` is the parent registry name). Both implementations run the same `golang.org/x/tools/go/analysis/passes/copylock` analyzer code (plaid pins x/tools v0.44.0, golangci v0.42.0 — copylock.go is byte-identical between these two versions). The divergence is purely in diagnostic attribution downstream.

c1 has nine `//nolint:govet` directives across the tree (e.g. `pkg/controller/app/controller/entitlement_proxy_binding_test.go:95,157`, `pkg/ugrpc/kuberesolver_v5/builder.go:180`, plus the WebSocket controller's slices-inline-info directives). golangci-lint's nolint filter sees the diagnostic with `Linter: govet` and matches; plaid's nolint filter sees `Linter: copylocks` / `Linter: waitgroup` and doesn't. Same root cause manifests in path-rules (a `linters: [govet]` rule wouldn't cover sub-analyzer diagnostics either).

**Fix.** `internal/exclusion/exclusion.go::familyByPrefix` already maps the staticcheck-family per-check names (SA####/ST####/QF####/S1###) to the umbrella `staticcheck`. Extended the function to also map the 45 govet sub-analyzer names (copylocks, printf, shift, waitgroup, …) to `govet`. The lookup uses an explicit `govetSubAnalyzers` set rather than a prefix rule because the sub-analyzer names share no common prefix.

Both consumers of `familyByPrefix` automatically pick up the new mapping:

- `internal/exclusion/nolint.go::rangeCoversLinter` — `//nolint:govet` now covers `copylocks` / `waitgroup` / etc.
- `internal/exclusion/exclusion.go::matchLinterName` — a `linters: [govet]` path-rule now covers sub-analyzer diagnostics (e.g. c1's `_test\.go` rule didn't list govet, but other configurations do).

**Tests.**

- `internal/exclusion/nolint_test.go::TestNolintFilter_GovetFamilyAlias` — pins that `//nolint:govet` suppresses `copylocks`, `printf`, `shift`, `structtag`, `unreachable` diagnostics, and does NOT suppress `gosec` or `errcheck`.
- `internal/exclusion/exclusion_test.go::TestFilter_RuleGovetAlias` — pins that a `linters: [govet]` path-rule scoped to `_test\.go` drops `copylocks` and `printf` diagnostics in test files while preserving them in non-test files, and does not affect non-govet diagnostics.

**Post-fix count.** 0.

### 2. gosec G124 (2 hits) — fix-class (b)

**Root cause.** Same library-version-skew pattern as gosec G702/G703 closed in D-107. plaid pins `github.com/securego/gosec/v2 v2.26.1`; golangci-lint v2.9 pins v2.22.11. v2.26 added the `G124` analyzer (`Insecure HTTP cookie configuration missing Secure, HttpOnly, or SameSite attributes`) via `analyzers/analyzerslist.go:159`. v2.22 doesn't have it. Two c1 sites use `&http.Cookie{Name:…, Value:…}` without setting Secure/HttpOnly/SameSite: `pkg/authn/rpc_cookie.go:52` (real cookie-jar parsing) and `pkg/utest/fixtures.go:592` (test helper).

**Fix.** Append `"G124"` to the existing `gosecanalyzers.NewAnalyzerFilter(true, "G407", "G702", "G703")` call in `internal/registry/wire_analyzers_polya.go:253`. gosec's own analyzer-filter API drops the analyzer pre-scan; no diagnostics emitted.

**Tests.** Append-only change to a pre-existing list. Integration tested by the full c1 re-run; the existing `TestPolyBatchA_Gosec_IncludeExcludeFilters` validates the wire harness still produces a non-nil Analyzer.

**Post-fix count.** 0.

### 3. noctx `net/http/httptest.NewRequest` (10 hits) — fix-class (b)

**Root cause.** Library version skew. plaid pins `github.com/sonatard/noctx v0.5.1`; golangci-lint v2.9 pins v0.4.0. v0.5.0 added a new rule to noctx's `ngFuncMessages` map: `"net/http/httptest.NewRequest": "must not be called. use net/http/httptest.NewRequestWithContext"`. v0.4.0 doesn't have this entry, so the diagnostic doesn't fire under golangci-lint.

`pkg/api/ssf_receiver/push_handler_test.go` makes 10 `httptest.NewRequest(http.MethodPost, …, …)` calls across various sub-test cases — all surface only under plaid.

The noctx library exposes no rule-include/exclude API (the `ngFuncMessages` map is package-level and immutable from the consumer's perspective). Two options:

1. **Pin to v0.4.0.** Risk: gives up unrelated bug fixes in v0.5.x. Not chosen — out-of-scope mass downgrade.
2. **Post-filter by message.** Drop noctx diagnostics whose `Message` starts with `net/http/httptest.NewRequest must not be called`.

**Fix.** Option (2). New helper `internal/exclusion/exclusion.go::dropLibraryVersionSkew` invoked between the staticcheck-default-disabled filter and the user path/text rules. The helper takes one `output.Diagnostic` and returns true to drop. Currently covers only the noctx httptest case; the function is structured to take additional skew entries as we discover them. The non-httptest noctx rules (the v0.4.0-baseline set) pass through untouched — confirmed by the matching test fixture.

**Tests.** `internal/exclusion/exclusion_test.go::TestFilter_NoctxHttptestSkewDropped` — feeds two noctx diagnostics into Apply, asserts the httptest-prefixed one is dropped and the `net/http.NewRequest` one (in the v0.4.0 baseline) is kept.

**Post-fix count.** 0.

### 4. goconst `"pending"` cross-file scoping (1 hit) — fix-class (c) accept

**Site.** `pkg/services/support_dashboard/http_admin_tenant_objects.go:296` — `return "pending"`. The function returns one of several status strings; in the same package `accessReviewActionStatePending = "pending"` is defined as a const, and two sibling files carry `//nolint:goconst` annotations on the same string literal but the directive on _this_ file is missing.

**Root cause.** plaid pins `github.com/jgautheron/goconst v1.10.1`; golangci-lint v2.9 pins v1.8.2. D-107 closed the bulk of the v1.10-vs-v1.8 divergence by masking the CompositeLit visitor (added in v1.10). The remaining cross-file scope difference relates to how v1.10 enumerates matching constants across files in the same package — v1.10's MatchWithConstants traversal sees `accessReviewActionStatePending` as a matching constant and fires the "string `pending` has 3 occurrences, but such constant `…Pending` already exists" diagnostic; v1.8's narrower scope skips it.

**Verdict.** Accept. Three reasons:

1. **The diagnostic is correct.** The c1 maintainers added `//nolint:goconst` on sibling files because they agreed with goconst's "use the constant" recommendation but locally chose not to apply it. The missing nolint on line 296 is an oversight in c1, not a plaid bug.
2. **Downgrading is the wrong direction.** Pinning back to v1.8 would give up v1.10's CompositeLit *fix* (which exists for a reason — there's a fair number of false positives if you don't mask it, but the fix landed because v1.8 was missing real findings on struct-literal contexts that v1.10 catches).
3. **Reimplementing v1.8's narrower scope from plaid is high-risk.** The right place for parity is upstream — when golangci-lint v2.10 picks up goconst v1.10+ (likely soon, the library is actively maintained), this divergence resolves itself.

**Recommended c1 fix-up.** Add `//nolint:goconst // … reason …` on `pkg/services/support_dashboard/http_admin_tenant_objects.go:296` to match the two sibling files. That makes the file consistent with the rest of the package's stance on this constant.

**Post-fix count.** 1 (accepted residual).

## Surprises

(a) **`PLAID_DISABLE` env var is documentation-only.** The D-107 NOTES.md (and this dispatch's brief) reference `PLAID_DISABLE=...` as the workaround for the gochecknoinits subprocess `argument list too long` error (D-101 BLOCKER-1). That env var does NOT actually exist in plaid's code — `grep -rn 'PLAID_DISABLE' --include='*.go'` finds only `PLAID_DISABLE_L0_CACHE`. The functional workaround is `-D <linter>` repeated on the CLI (per `plaid run --help`). Updating the dispatch playbook to reflect this is a recommended follow-on. The full c1 baseline run was retried three times before this was caught — first attempt failed silently on D-101 BLOCKER-1; second attempt set the env var but the symptom was unchanged; CLI flags worked first try.

(b) **noctx + httptest is a NEW residual class not surfaced by D-107.** D-107 sampled `cmd/...` + 5 controllers + `pkg/services/...`; `pkg/api/ssf_receiver/...` was outside that scope so the 10 httptest hits never surfaced. This is exactly what the dispatch's "Stop conditions" section flagged: "NEW divergence classes that weren't in D-107's residuals — would indicate a regression from L0 or later changes." In this case it's not a regression but a coverage gap from the sampling strategy. Confirmed by reading the noctx v0.4 → v0.5 diff (the httptest entry was added in v0.5.0; plaid's v0.5.1 inherits it; golangci's v0.4.0 doesn't have it).

(c) **gosec G124 (insecure cookie) wasn't surfaced by D-107 either.** Same coverage gap as (b) — the two G124 sites (`pkg/authn/rpc_cookie.go` and `pkg/utest/fixtures.go`) live outside the D-107 sampled scopes. D-107 caught G702/G703 in the cmd-tree but missed G124 in pkg/authn.

## Methodology notes

- Single replicate per measurement (no variance band).
- `-D` mask retained for the 8 subprocess linters that hit D-101 BLOCKER-1 (`gochecknoinits dogsled dupl gocyclo godox lll nestif unconvert`). None of these contributed to the 5 target classes.
- Cache wiped (`rm -rf $HOME/.cache/plaid-lint`) before each cold measurement.
- Both linters run from `/data/squire/src/c1` with the c1 `.golangci.yml` after `cmd/strip-tracecheck-config/` removes the custom plugin block (plaid doesn't load the c1 tracecheck `.so`; same masking D-107 used).

## Timing

| Run | Wall | CPU |
|---|---:|---:|
| golangci-lint full c1 (cold) | 7m53s | 63m04s |
| plaid baseline (no fixes) | 6m22s | 51m37s |
| plaid baseline (with `-D` mask) | 6m20s | 51m44s |
| plaid post-fix | 6m28s | 51m49s |

## LOC delta

| File | New / modified |
|---|---|
| `internal/exclusion/exclusion.go` | +51 -7 (familyByPrefix expansion + govetSubAnalyzers set + dropLibraryVersionSkew helper + filter wire) |
| `internal/exclusion/nolint_test.go` | +27 (TestNolintFilter_GovetFamilyAlias) |
| `internal/exclusion/exclusion_test.go` | +59 -0 (TestFilter_NoctxHttptestSkewDropped, TestFilter_RuleGovetAlias, +1 import) |
| `internal/registry/wire_analyzers_polya.go` | +1 -1 (G124 appended to AnalyzerFilter) |
| `NOTES.md` | D-113 entry |
| `compare/c1-2026-05-22-residual-triage.md` | new (this file) |

Total: **~140 new lines, ~9 modified**, plus this doc and the NOTES.md entry.

## Artifacts

- `/tmp/g.json` — golangci-lint output (3194 bytes; 0 issues, 119 linters listed).
- `/tmp/c.json` — plaid baseline output (4203 bytes; 16 issues).
- `/tmp/c2.json` — plaid post-fix output.

All ephemeral; rerun against c1 `6d09432a07` to regenerate.
