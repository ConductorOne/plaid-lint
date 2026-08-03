#!/usr/bin/env bash
# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# End-to-end harness for the rules_plaid example workspace. Runs real
# bazel builds against the seeded findings in this workspace and
# asserts the aspect's observable contract:
#
#   1. validation failure on seeded findings (printf-via-fact, errcheck)
#   2. report-only mode (--norun_validations) succeeds and emits SARIF
#   3. PlaidLint actions are cached (a repeat build executes nothing)
#   4. plaid_module_lint fails on the seeded gomoddirectives finding
#   5. worker mode (use_worker=True) produces byte-identical SARIF
#   6. go_test with an in-package test builds and lints cleanly
#   7. the aggregate suite gate (bazel test //:lint) fails with exactly
#      the seeded enforced findings; superseded findings are absent
#   8. a clean suite scope (bazel test //:lint_clean) passes
#   9. the suite test result is cached on an immediate rerun
#  10. the suite's aggregate SARIF is byte-identical across worker and
#      one-shot execution
#  11. bazel build of a suite target is report-only (a red gate never
#      fails a plain build) and publishes the text report
#
# Runnable locally and in CI from this directory: ./e2e.sh
# Override the bazel binary with BAZEL=... (defaults to bazel).
set -euo pipefail
cd "$(dirname "$0")"

BAZEL="${BAZEL:-bazel}"

OUT=""
CODE=0

# run <bazel args...>: capture combined stdout+stderr and the exit
# code without tripping set -e. Findings are echoed by bazel from the
# failing validation action, so one combined stream is what we grep.
run() {
  set +e
  OUT="$("$BAZEL" "$@" 2>&1)"
  CODE=$?
  set -e
}

fail() {
  echo "FAIL: $*" >&2
  echo "---- last bazel output ----" >&2
  echo "$OUT" >&2
  exit 1
}

pass() { echo "PASS: $*"; }

BAZEL_BIN="$("$BAZEL" info bazel-bin)"

# (a) Seeded findings fail the build through the validations
# mechanism, and both seeded findings are reported: the printf
# misuse (only detectable through lib's exported printf-wrapper
# fact) and the unchecked error.
run build --config=lint //app:app
[[ "$CODE" -ne 0 ]] || fail "(a) expected nonzero exit from lint build of //app:app with seeded findings"
grep -q "printf" <<<"$OUT" || fail "(a) expected a printf finding in build output"
grep -q "errcheck" <<<"$OUT" || fail "(a) expected an errcheck finding in build output"
pass "(a) --config=lint //app:app fails with printf + errcheck findings"

# (b) Report-only mode: --norun_validations disables enforcement but
# the PlaidLint actions still run and publish SARIF via the
# plaid_report output group (already in --config=lint).
run build --config=lint --norun_validations //app:app //lib:lib
[[ "$CODE" -eq 0 ]] || fail "(b) expected report-only build to succeed"
SARIF="$BAZEL_BIN/app/app.plaid.sarif"
[[ -f "$SARIF" ]] || fail "(b) expected SARIF report at $SARIF"
python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$SARIF" \
  || fail "(b) $SARIF is not valid JSON"
grep -q "printf" "$SARIF" || fail "(b) expected the printf finding in $SARIF"
pass "(b) report-only build succeeds and emits valid SARIF with the printf finding"

# (c) Caching: an immediate repeat of (b) must execute ZERO PlaidLint
# actions. Assertion: bazel's terminating "N processes: ..." summary
# line categorizes every action it ran by spawn strategy; a fully
# cached build reports only bookkeeping categories ("internal",
# "action cache hit", "disk cache hit", "remote cache hit"). Any
# execution strategy name (worker, *-sandbox, local, standalone,
# remote) in that line means an action actually executed. This is
# robust where grepping progress messages is not: progress lines are
# tty/rate dependent, while the summary is always printed.
run build --config=lint --norun_validations //app:app //lib:lib
[[ "$CODE" -eq 0 ]] || fail "(c) expected cached repeat build to succeed"
SUMMARY="$(grep -E '^INFO: [0-9]+ process(es)?:' <<<"$OUT" || true)"
[[ -n "$SUMMARY" ]] || fail "(c) no process summary line in bazel output"
if grep -qE 'sandbox|worker|\blocal\b|standalone' <<<"$SUMMARY"; then
  fail "(c) expected zero executed actions on repeat build; summary was: $SUMMARY"
fi
pass "(c) repeat build is fully cached ($SUMMARY)"

# (d) Module-scoped lint: the seeded local `replace` directive in
# go.mod fails //:module_lint through gomoddirectives.
run build //:module_lint
[[ "$CODE" -ne 0 ]] || fail "(d) expected //:module_lint to fail on the seeded replace directive"
grep -q "gomoddirectives" <<<"$OUT" || fail "(d) expected a gomoddirectives finding in build output"
pass "(d) //:module_lint fails with the gomoddirectives finding"

# (e) Worker mode: the %plaid_worker aspect (use_worker=True,
# output_suffix=".worker") runs PlaidLint through the persistent
# JSON worker protocol. The distinct output_suffix keeps its declared
# outputs from colliding with %plaid's, so the SARIF pairs can be
# byte-compared within one output tree. Identical analysis must
# produce identical bytes regardless of execution mode.
run build --aspects=//tools/lint:linters.bzl%plaid_worker \
  --output_groups=+plaid_report --norun_validations --worker_verbose \
  //app:app //lib:lib
[[ "$CODE" -eq 0 ]] || fail "(e) expected worker-mode build to succeed"
for pkg in app lib; do
  plain="$BAZEL_BIN/$pkg/$pkg.plaid.sarif"
  worker="$BAZEL_BIN/$pkg/$pkg.worker.plaid.sarif"
  [[ -f "$worker" ]] || fail "(e) expected worker-mode SARIF at $worker"
  cmp -s "$plain" "$worker" || fail "(e) worker-mode SARIF $worker differs from $plain"
done
pass "(e) worker-mode SARIF is byte-identical to the default aspect's"

# (f) go_test with an in-package test: lib_test.go lives in package
# lib and is the only referent of lib.testOnly. The test target must
# build, lint, and pass — the aspect lints the internal and external
# test archives (not rules_go's generated testmain, which is
# facts_only as a generated-only archive), and per-target validation
# must not enforce `unused` against testOnly (default
# validation_ignore_linters).
run test --config=lint //lib:lib_test
[[ "$CODE" -eq 0 ]] || fail "(f) expected bazel test --config=lint //lib:lib_test to pass"
pass "(f) go_test with in-package test lints and passes"

# (g) Test-archive coverage + the collect supersede rule: linting
# //lib:lib_test must produce SARIF for the internal test archive
# (lib.go + lib_test.go), whose run supersedes the library run's
# `unused` finding about testOnly when aggregated with collect —
# the test-variant rule that per-target validation cannot apply.
run build --config=lint --norun_validations //lib:lib //lib:lib_test
[[ "$CODE" -eq 0 ]] || fail "(g) expected report-only test-archive build to succeed"
INTERNAL_SARIF="$BAZEL_BIN/lib/lib_test.internal.plaid.sarif"
[[ -f "$INTERNAL_SARIF" ]] || fail "(g) expected internal test-archive SARIF at $INTERNAL_SARIF"
run build @plaid_lint//cmd/plaid-lint
[[ "$CODE" -eq 0 ]] || fail "(g) expected @plaid_lint//cmd/plaid-lint to build"
PLAID_REL="$("$BAZEL" cquery --output=files @plaid_lint//cmd/plaid-lint 2>/dev/null | head -1)"
COLLECT_BIN="./$PLAID_REL"
[[ -x "$COLLECT_BIN" ]] || fail "(g) could not locate the plaid-lint binary for collect (got: $PLAID_REL)"
SUPERSEDE_OUT="$("$COLLECT_BIN" collect "$BAZEL_BIN/lib/lib.plaid.sarif" "$INTERNAL_SARIF" 2>&1 || true)"
grep -q "superseded by test-variant runs" <<<"$SUPERSEDE_OUT" \
  || fail "(g) expected the library's unused finding to be superseded; collect said: $SUPERSEDE_OUT"
pass "(g) internal test archive is linted and supersedes the library's unused finding via collect"

# (h) Worker fallback: a use_worker aspect's actions must run
# correctly under a NON-worker strategy too (sandboxed here — the
# same argv shape a remote executor would run). Bazel only rewrites
# the argv into worker form when it actually launches a worker; the
# one-shot fallback is `plaid-lint unit @flagfile`.
run clean
run build --aspects=//tools/lint:linters.bzl%plaid_worker \
  --strategy=PlaidLint=sandboxed --output_groups=+plaid_report \
  --norun_validations //lib:lib
[[ "$CODE" -eq 0 ]] || fail "(h) expected worker-configured aspect to succeed under --strategy=PlaidLint=sandboxed"
[[ -f "$BAZEL_BIN/lib/lib.worker.plaid.sarif" ]] || fail "(h) expected fallback run to produce SARIF"
pass "(h) use_worker actions fall back to one-shot execution under non-worker strategies"

# (i) Aggregate suite gate: bazel test //:lint is red BY DESIGN and
# must fail with exactly the seeded enforced findings — `unused`
# survives only where no test-variant run supersedes it (neverUsed,
# xHelper), plus the printf, errcheck, and gomoddirectives findings —
# while superseded findings (lib.testOnly, unusedpkg.testedOnly), exported
# symbols, and private declarations in main-repository vendor packages never
# appear. The runner's verdict line pins the
# exact enforced count so a new seeded finding (or a lost one) fails
# this assertion, not just "some findings exist".
run test //:lint
[[ "$CODE" -ne 0 ]] || fail "(i) expected bazel test //:lint to fail on enforced findings"
TESTLOG="bazel-testlogs/lint/test.log"
[[ -f "$TESTLOG" ]] || fail "(i) expected test log at $TESTLOG"
for want in \
  "func neverUsed is unused" \
  "func xHelper is unused" \
  "gomoddirectives" \
  "printf" \
  "errcheck" \
  "superseded by test-variant runs" \
  "FAIL — 5 enforced finding(s)"; do
  grep -qF "$want" "$TESTLOG" || fail "(i) expected '$want' in $TESTLOG"
done
for absent in "testedOnly" "testOnly is unused" "Exported" "vendorDead" "localVendorDead"; do
  if grep -qF "$absent" "$TESTLOG"; then
    fail "(i) did not expect '$absent' in $TESTLOG"
  fi
done
pass "(i) suite keeps vendored dependencies facts-only and enforces exactly the 5 first-party findings"

# (j) A clean suite scope passes: lib's only finding (unused
# testOnly) is superseded by the in-package test archive, so
# //:lint_clean must be green and say so.
run test //:lint_clean
[[ "$CODE" -eq 0 ]] || fail "(j) expected bazel test //:lint_clean to pass"
CLEANLOG="bazel-testlogs/lint_clean/test.log"
[[ -f "$CLEANLOG" ]] || fail "(j) expected test log at $CLEANLOG"
grep -qF "plaid-lint suite: clean" "$CLEANLOG" || fail "(j) expected 'plaid-lint suite: clean' in $CLEANLOG"
pass "(j) bazel test //:lint_clean passes and reports a clean suite"

# (k) Suite caching: an immediate rerun of //:lint_clean must be a
# test-result cache hit ("(cached) PASSED") and execute zero actions
# — same process-summary reasoning as (c).
run test //:lint_clean
[[ "$CODE" -eq 0 ]] || fail "(k) expected cached rerun of //:lint_clean to pass"
grep -qF "(cached) PASSED" <<<"$OUT" || fail "(k) expected '(cached) PASSED' in rerun output"
SUMMARY="$(grep -E '^INFO: [0-9]+ process(es)?:' <<<"$OUT" || true)"
[[ -n "$SUMMARY" ]] || fail "(k) no process summary line in bazel output"
if grep -qE 'sandbox|worker|\blocal\b|standalone' <<<"$SUMMARY"; then
  fail "(k) expected zero executed actions on cached test rerun; summary was: $SUMMARY"
fi
pass "(k) //:lint_clean rerun is a test-result cache hit ($SUMMARY)"

# (l) Aggregate byte-identity across execution modes: the suite's
# aggregate SARIF from a one-shot build must be byte-identical to the
# worker-mode build's. The use_worker build setting changes the
# output configuration (distinct bazel-out subtree), so the worker
# SARIF path is resolved via cquery WITH the flag rather than the
# BAZEL_BIN captured at the top of this script.
run build //:lint
[[ "$CODE" -eq 0 ]] || fail "(l) expected one-shot bazel build //:lint to succeed"
ONE_SHOT_SARIF="$(mktemp)"
cp "$BAZEL_BIN/lint.plaid.sarif" "$ONE_SHOT_SARIF"
run build --@plaid_lint//bazel:use_worker=true //:lint
[[ "$CODE" -eq 0 ]] || fail "(l) expected worker-mode bazel build //:lint to succeed"
WORKER_SARIF="$("$BAZEL" cquery --output=files --@plaid_lint//bazel:use_worker=true //:lint 2>/dev/null | grep '\.plaid\.sarif$' | head -1)"
[[ -n "$WORKER_SARIF" && -f "$WORKER_SARIF" ]] || fail "(l) could not resolve the worker-mode aggregate SARIF (got: ${WORKER_SARIF:-<empty>})"
cmp -s "$ONE_SHOT_SARIF" "$WORKER_SARIF" \
  || fail "(l) worker-mode aggregate SARIF differs from one-shot ($WORKER_SARIF)"
rm -f "$ONE_SHOT_SARIF"
pass "(l) suite aggregate SARIF is byte-identical across worker and one-shot execution"

# (m) Build-not-test is report-only: a plain `bazel build` of the RED
# suite target must succeed (the gate lives in the test runner, never
# in the build) and publish the human-readable text report.
run build //:lint
[[ "$CODE" -eq 0 ]] || fail "(m) expected plain bazel build //:lint to succeed (report-only)"
[[ -f "$BAZEL_BIN/lint.plaid-report.txt" ]] || fail "(m) expected aggregate text report at $BAZEL_BIN/lint.plaid-report.txt"
pass "(m) bazel build //:lint is report-only and publishes lint.plaid-report.txt"

# (n) Failure-class separation at the Bazel level: a malformed
# .golangci config must fail the PlaidLint ACTIONS (a build error) —
# never surface as lint findings or a red-but-built test.
run test //:lint_clean --@plaid_lint//bazel:config=//:bad-config.yml
[[ "$CODE" -ne 0 ]] || fail "(n) expected a build failure under a malformed config"
grep -q "FAILED TO BUILD" <<<"$OUT" || fail "(n) expected FAILED TO BUILD (action failure), got a test verdict instead"
if grep -qE "enforced finding|plaid-lint suite: FAIL" <<<"$OUT"; then
  fail "(n) config failure was reported as lint findings"
fi
pass "(n) a malformed config fails the build, never masquerading as findings"

# (o) Requirement-9 flags: --keep_going must not change the suite
# verdict, and --norun_validations must NOT disable the suite gate
# (it only silences per-target validation actions; the suite is an
# explicitly requested test).
run test //:lint --keep_going
[[ "$CODE" -ne 0 ]] || fail "(o) expected the red suite to stay red under --keep_going"
grep -q "FAIL — 5 enforced finding(s)" <<<"$OUT" || grep -q "FAILED" <<<"$OUT" || fail "(o) missing failure under --keep_going"
run test //:lint --norun_validations
[[ "$CODE" -ne 0 ]] || fail "(o) --norun_validations must not disable the suite gate"
pass "(o) suite verdict stable under --keep_going and --norun_validations"

# (p) facts_only packages cannot gate: with unusedpkg configured
# facts_only (the generated-tree exclusion, e.g. a monorepo's checked
# in protoc output), its unused findings vanish from the suite while
# every other finding still enforces. Its facts still flow to
# importers — mode only controls lint-subject status.
TESTLOG_P="$("$BAZEL" info bazel-testlogs)/lint/test.log"
run test //:lint --@plaid_lint//bazel:facts_only=example.com/plaidexample/unusedpkg
[[ "$CODE" -ne 0 ]] || fail "(p) suite must stay red on the non-excluded findings"
grep -q "FAIL — 3 enforced finding(s)" "$TESTLOG_P" || fail "(p) expected exactly 3 enforced findings with unusedpkg facts_only"
if grep -q "unusedpkg" "$TESTLOG_P"; then
  fail "(p) facts_only package findings leaked into the suite gate"
fi
pass "(p) facts_only packages produce facts but can never gate the suite"

# (q) All-external-test targets (go_test with only package foo_test
# sources and no embed — the c1 //pkg/ssf/caep shape): the internal
# archive's inferred importpath also ends in _test, so classification
# must key on source.testfilter or both archives collide on the
# .xtest output names at analysis time.
run build --config=lint //xtestonly:xtestonly_test
[[ "$CODE" -eq 0 ]] || fail "(q) all-external-test target failed lint analysis/build: $OUT"
[[ -f "$BAZEL_BIN/xtestonly/xtestonly_test.xtest.plaid.sarif" ]] || fail "(q) external test archive was not linted"
[[ -f "$BAZEL_BIN/xtestonly/xtestonly_test.internal.plaid.sarif" ]] || fail "(q) internal archive missing its (empty) lint output"
pass "(q) all-external-test go_test lints cleanly; testfilter-keyed archive classification"

# (r) Stderr noise suppression: a PlaidLint action must not leak
# honnef.co/go/tools' unused debug traces ("new node, remapping X ->
# Y", "deduplicating ...") into the build output — an unused-enabled
# suite once emitted millions of such lines (71.7 MB of stderr) per
# uncached run. Bazel echoes a successful action's stderr under an
# "INFO: From PlaidLint" header, so the combined output is exactly
# where a leak would surface. A content change to lib.go forces a
# fresh (non-cached) compile + PlaidLint execution — cheap, and the
# process summary proves the action really ran rather than the
# assertion passing vacuously against a cache hit.
LIB_SRC="lib/lib.go"
cp "$LIB_SRC" "$LIB_SRC.e2e-bak"
trap '[[ -f "$LIB_SRC.e2e-bak" ]] && mv "$LIB_SRC.e2e-bak" "$LIB_SRC"' EXIT
printf '\n// e2e(r): content change forcing a fresh PlaidLint execution\n' >>"$LIB_SRC"
run build --config=lint --norun_validations //lib:lib
mv "$LIB_SRC.e2e-bak" "$LIB_SRC"
[[ "$CODE" -eq 0 ]] || fail "(r) expected report-only lint of the touched //lib:lib to succeed"
SUMMARY="$(grep -E '^INFO: [0-9]+ process(es)?:' <<<"$OUT" || true)"
grep -qE 'sandbox|worker|\blocal\b|standalone' <<<"$SUMMARY" \
  || fail "(r) expected the touched build to execute actions (got summary: ${SUMMARY:-<none>}); the noise assertion would be vacuous"
if grep -q "new node, remapping" <<<"$OUT"; then
  fail "(r) PlaidLint action leaked 'new node, remapping' debug traces into the build output"
fi
if grep -q "deduplicating " <<<"$OUT"; then
  fail "(r) PlaidLint action leaked 'deduplicating' debug traces into the build output"
fi
pass "(r) fresh PlaidLint execution emits no honnef unused debug traces"

# (s) Build-constraint file selection (the c1 //pkg/randkey shape):
# //tagged declares tagged_arm64.go (//go:build arm64) and
# tagged_noasm.go (//go:build !arm64), both defining the same private
# helper. GoCompilePkg selects one at action time; the unit driver
# must apply the same selection or it type-checks both and reports a
# spurious "hexExpand redeclared" finding that fails the lint gate.
# Must hold on EITHER host arch, so no arch is assumed here.
run build --config=lint //tagged:tagged
[[ "$CODE" -eq 0 ]] || fail "(s) expected lint build of //tagged:tagged to succeed on this host arch: $OUT"
if grep -q "redeclared" <<<"$OUT"; then
  fail "(s) constraint-excluded file leaked into type-checking (redeclared finding)"
fi
TAGGED_SARIF="$BAZEL_BIN/tagged/tagged.plaid.sarif"
run build --config=lint --norun_validations //tagged:tagged
[[ -f "$TAGGED_SARIF" ]] || fail "(s) expected SARIF report at $TAGGED_SARIF"
grep -q "redeclared" "$TAGGED_SARIF" && fail "(s) redeclared finding present in $TAGGED_SARIF"
# Exactly ONE of the mutually exclusive files may appear in the run's
# analyzed-file identity, whichever arch this host builds for.
python3 - "$TAGGED_SARIF" <<'PY' || fail "(s) SARIF goFiles must contain exactly one of tagged_arm64.go/tagged_noasm.go"
import json, sys
files = json.load(open(sys.argv[1]))["runs"][0]["properties"]["plaidUnit"]["goFiles"]
names = [f.rsplit("/", 1)[-1] for f in files]
impls = [n for n in names if n in ("tagged_arm64.go", "tagged_noasm.go")]
sys.exit(0 if len(impls) == 1 and "tagged.go" in names else 1)
PY
pass "(s) //tagged lints cleanly: constraint-excluded file never reaches the type-checker"

echo "OK: all e2e assertions passed"
