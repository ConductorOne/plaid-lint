#!/usr/bin/env bash
# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.
#
# C1-parity harness for the aggregate suite gate. Runs
# //c1parity:c1parity_lint under the C1-SHAPED fixture config
# (c1parity/golangci-c1.yml — C1's config classes with example.com
# paths) and asserts the parity surface from
# UNUSED-AGGREGATION-HANDOFF.md:
#
#   - bundled tracecheck fires with NO /linters/tracecheck.so anywhere;
#   - depguard, forbidigo, and exhaustive findings match their seeds;
#   - nolint behavior matches `plaid-lint run` (the explained
#     //nolint:errcheck line stays suppressed, no nolintlint finding);
#   - private unused findings match after test supersession
#     (parityNeverUsed enforced; parityTestedOnly superseded);
#   - exported declarations are never reported;
#   - the module scope is //:lint's job: no gomoddirectives finding
#     leaks in (the suite omits go_mod on purpose).
#
# The @plaid_lint//bazel:config build setting is configuration-global,
# so this suite runs in its own bazel invocation instead of inside
# e2e.sh's (which uses the workspace-default //:.golangci.yml).
#
# Runnable locally and in CI from this directory: ./e2e_c1parity.sh
# Override the bazel binary with BAZEL=... (defaults to bazel).
set -euo pipefail
cd "$(dirname "$0")"

BAZEL="${BAZEL:-bazel}"

# The two build settings every command in this file runs under; the
# config override is the whole point, and module_path keeps the
# fixture classified as in-module (full lint) rather than facts-only.
SETTINGS=(
  "--@plaid_lint//bazel:config=//c1parity:golangci-c1.yml"
  "--@plaid_lint//bazel:module_path=example.com/plaidexample"
)

OUT=""
CODE=0

# run <bazel args...>: capture combined stdout+stderr and the exit
# code without tripping set -e.
run() {
  set +e
  OUT="$("$BAZEL" "$@" 2>&1)"
  CODE=$?
  set -e
}

fail() {
  echo "FAIL: $*" >&2
  echo "--- last bazel output ---" >&2
  echo "$OUT" >&2
  exit 1
}

pass() { echo "PASS: $*"; }

# (a0) The suite target must BUILD cleanly (report-only path): a
# compile error or action failure here is a bug, not a red gate, and
# would otherwise leave assertions below reading a stale test.log.
run build //c1parity:c1parity_lint "${SETTINGS[@]}"
[[ "$CODE" -eq 0 ]] || fail "(a0) report-only build of //c1parity:c1parity_lint must succeed: $OUT"
pass "(a0) suite target builds; gate evaluated fresh"

# (a) The parity gate is red BY DESIGN: bazel test must fail on the
# seeded enforced findings.
run test //c1parity:c1parity_lint "${SETTINGS[@]}"
[[ "$CODE" -ne 0 ]] || fail "(a) expected bazel test //c1parity:c1parity_lint to fail on seeded findings"
TESTLOG="bazel-testlogs/c1parity/c1parity_lint/test.log"
[[ -f "$TESTLOG" ]] || fail "(a) expected test log at $TESTLOG"
pass "(a) //c1parity:c1parity_lint fails under golangci-c1.yml"

# (b) Every seeded finding is present, by linter name and by its
# identifying text, and the enforced count is pinned so a new stray
# finding (or a lost seed) fails here rather than hiding.
for want in \
  "(tracecheck)" \
  "'checkout_span' should be changed to 'record_checkout'" \
  "(depguard)" \
  "example.com/plaidexample/c1parity/internal/forbidden" \
  "(forbidigo)" \
  "fmt.Println" \
  "(exhaustive)" \
  "stateDone" \
  "(unused)" \
  "func parityNeverUsed is unused" \
  "superseded by test-variant runs" \
  "FAIL — 5 enforced finding(s)"; do
  grep -qF "$want" "$TESTLOG" || fail "(b) expected '$want' in $TESTLOG"
done
pass "(b) tracecheck, depguard, forbidigo, exhaustive, and unused seeds all reported; exactly 5 enforced"

# (c) Suppressed and superseded findings are absent: the explained
# //nolint:errcheck line holds (no errcheck, no nolintlint finding),
# the test-only private func is superseded, exported symbols are
# treated as used, and no module finding leaks in (go_mod omitted).
for absent in \
  "(errcheck)" \
  "os.Mkdir" \
  "(nolintlint)" \
  "parityTestedOnly" \
  "Exported" \
  "(gomoddirectives)"; do
  if grep -qF "$absent" "$TESTLOG"; then
    fail "(c) did not expect '$absent' in $TESTLOG"
  fi
done
pass "(c) nolint'd errcheck line and superseded test-only func absent; no module finding leaked"

# (d) Bundled tracecheck: the finding in (b) proves the analyzer ran,
# and no tracecheck.so is referenced anywhere — neither in the test
# log nor in bazel's own output. Upstream loads tracecheck as a Go
# plugin at /linters/tracecheck.so; plaid-lint vendors it natively,
# so a parity run must never mention the shared object.
if grep -qF "tracecheck.so" "$TESTLOG" || grep -qF "tracecheck.so" <<<"$OUT"; then
  fail "(d) tracecheck.so referenced — bundled tracecheck must not touch the plugin"
fi
pass "(d) bundled tracecheck fired natively; no tracecheck.so involved"

# (e) Path-scoped exclusion parity: the forbidigo violation seeded in
# c1parity_test.go is suppressed by the config's `path: _test\.go`
# exclusion rule and must not appear in the findings.
if grep -qF "c1parity_test.go" "$TESTLOG"; then
  fail "(e) a finding fired inside c1parity_test.go despite the _test.go path exclusion rule"
fi
pass "(e) path-scoped _test.go exclusion suppresses forbidigo in test files"

echo "OK: all c1parity assertions passed"
