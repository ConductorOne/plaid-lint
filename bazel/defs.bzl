# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

"""Public Bazel API for plaid-lint: golangci-lint-compatible Go
linting as native, cacheable, remote-executable Bazel actions.

Setup (consumer workspace):

    # MODULE.bazel
    bazel_dep(name = "plaid_lint", version = "...")

    # tools/lint/linters.bzl
    load("@plaid_lint//bazel:defs.bzl", "plaid_lint_aspect")
    plaid = plaid_lint_aspect(
        config = Label("//:.golangci.yml"),
        facts_only = ["example.com/mod/gen"],   # importpath prefixes
    )

    # .bazelrc
    build:lint --aspects=//tools/lint:linters.bzl%plaid
    build:lint --output_groups=+plaid_report

Then `bazel build --config=lint //...` lints every Go target. Findings
fail the build through Bazel's validations mechanism (ValidatePlaidLint
actions in the `_validation` output group); `--norun_validations`
turns enforcement off while still producing SARIF reports, and
`--keep_going` collects findings across all targets.

Per package, one `PlaidLint` action runs `plaid-lint unit`: sources +
direct deps' export data (types) + direct deps' `.plaidfacts`
(cross-package analysis facts) in; `.plaidfacts` + SARIF out. The
actions are ordinary Bazel actions — content-keyed, remote-cacheable,
remote-executable. `unused` findings are excluded from per-target
validation by default (an aspect cannot see whether an in-package
test archive supersedes a library — aggregate with `plaid-lint
collect`, which applies the test-variant supersede rule, if you want
them enforced).

Module-scoped linters (gomoddirectives) run once per module via
plaid_module_lint — see that rule's doc.
"""

load(
    "//bazel/private:plaid.bzl",
    _PlaidFactsInfo = "PlaidFactsInfo",
    _PlaidLintInfo = "PlaidLintInfo",
    _PlaidReportInfo = "PlaidReportInfo",
    _make_plaid_lint_aspect = "make_plaid_lint_aspect",
    _plaid_lint_suite_aspect = "plaid_lint_suite_aspect",
    _plaid_lint_suite_test = "plaid_lint_suite_test",
    _plaid_module_lint = "plaid_module_lint",
)

PlaidFactsInfo = _PlaidFactsInfo

PlaidLintInfo = _PlaidLintInfo

PlaidReportInfo = _PlaidReportInfo

plaid_module_lint = _plaid_module_lint

def plaid_lint_aspect(
        binary = Label("@plaid_lint//cmd/plaid-lint"),
        config = None,
        module_path = "",
        facts_only = [],
        no_validation = False,
        validation_ignore_linters = ["unused"],
        use_worker = False,
        output_suffix = ""):
    """Returns a configured plaid_lint aspect.

    Call this in a .bzl file of your workspace (aspects applied from
    the command line cannot take parameters, so configuration is bound
    at construction).

    Args:
      binary: the plaid-lint executable. Defaults to building it from
        source via rules_go — hermetic and version-locked to this
        module. Point at a prebuilt binary target to skip that build.
      config: label of the workspace's .golangci.{yml,yaml,json}. None
        applies plaid-lint's defaults.
      module_path: the Go module path, populating package module
        identity for analyzers that consult it.
      facts_only: list of importpath prefixes analyzed for facts only
        (dependencies of the lint scope, e.g. generated code).
        External-repository packages are always facts_only.
      no_validation: produce reports but never register validation
        actions (report-only mode; consume the `plaid_report` output
        group).
      validation_ignore_linters: linters whose findings are printed
        but do not fail per-target validation. Default ["unused"] —
        see the module doc.
      use_worker: run PlaidLint actions via the Bazel persistent
        worker protocol (JSON). Serial workers; safe default off.
      output_suffix: string inserted between the target name and the
        output extensions (e.g. ".worker" makes app.worker.plaid.sarif).
        Two differently-configured plaid aspects applied in one build
        would otherwise declare colliding outputs; give each variant a
        distinct suffix.

    Returns:
      An aspect to apply via --aspects or from a rule attribute.
    """
    return _make_plaid_lint_aspect(
        binary = binary,
        config = config,
        module_path = module_path,
        facts_only = facts_only,
        no_validation = no_validation,
        validation_ignore_linters = validation_ignore_linters,
        use_worker = use_worker,
        output_suffix = output_suffix,
    )

# The aggregate-enforcement path. plaid_lint_suite_test is a test
# rule: it carries plaid_lint_suite_aspect on its `targets` attribute
# (report-only per target), aggregates every report through the typed
# PlaidLintInfo provider, applies the test-variant unused supersede
# rule (an in-package test archive analyzing a strict superset of its
# library's files invalidates the library run's `unused` findings),
# and fails on what survives. This is how `unused` is enforced with
# test-awareness and without whole-repository reachability analysis —
# a per-target aspect alone cannot do it, because it never sees
# reverse-dependent test archives.
#
# Aspects carried on rule attributes must be top-level values of
# their defining .bzl file, so — unlike plaid_lint_aspect above — the
# suite path is configured through build settings, set once in the
# consumer's .bazelrc:
#
#   common --@plaid_lint//bazel:config=//:.golangci.yml
#   common --@plaid_lint//bazel:module_path=example.com/mod
#   # optional: --@plaid_lint//bazel:facts_only=example.com/mod/gen
#   # optional: --@plaid_lint//bazel:use_worker=true
#
# and one BUILD target:
#
#   load("@plaid_lint//bazel:defs.bzl", "plaid_lint_suite_test")
#   plaid_lint_suite_test(
#       name = "lint",
#       targets = [...top-level Go targets...],
#       go_mod = "//:go.mod",
#       module_path = "example.com/mod",
#   )
#
# `bazel test //:lint` enforces; `bazel build //:lint` produces the
# aggregate SARIF + text report without enforcing. Failure classes
# stay distinct: findings fail the TEST; an unreadable report or
# analyzer crash fails the PlaidCollect ACTION (a build error); a bad
# .golangci config fails the PlaidLint actions themselves.
plaid_lint_suite_test = _plaid_lint_suite_test

plaid_lint_suite_aspect = _plaid_lint_suite_aspect
