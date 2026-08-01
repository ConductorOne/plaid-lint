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

echo "OK: all e2e assertions passed"
