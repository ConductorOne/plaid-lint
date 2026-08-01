# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

"""Implementation of the plaid_lint aspect and plaid_module_lint rule.

Consumer API lives in //bazel:defs.bzl. The action topology mirrors
rules_go's nogo:

  GoCompilePkg (exists) ──.x──▶ PlaidLint ──sarif──▶ ValidatePlaidLint
       dep facts ──.plaidfacts──▶   │                  (fails iff findings;
                                    └─▶ .plaidfacts      _validation group)
                                        for dependents

PlaidLint never fails on findings (they are SARIF results), so
`--keep_going` aggregates findings across targets and the analysis
result stays remote-cacheable whatever the verdict. ValidatePlaidLint
is the only thing that fails, via the official validations mechanism
(`--run_validations`, default on; `--norun_validations` to disable).
"""

load("@bazel_skylib//lib:dicts.bzl", "dicts")
load("@bazel_skylib//rules:common_settings.bzl", "BuildSettingInfo")
load("@rules_go//go:def.bzl", "GoArchive", "go_context")

GO_TOOLCHAIN = "@rules_go//go:toolchain"

PlaidFactsInfo = provider(
    doc = "Analysis facts produced by plaid-lint for one Go package.",
    fields = {
        "facts": "File: the package's .plaidfacts output.",
        "by_key": "dict[string, File]: facts files for packages an " +
                  "EMBEDDING target's archive may list as direct deps " +
                  "(this target's direct deps, plus embedded targets' " +
                  "maps), keyed by compiler package path.",
    },
)

PlaidReportInfo = provider(
    doc = "One archive's lint report, as an element of PlaidLintInfo.reports.",
    fields = {
        "sarif": "File: the archive's SARIF report.",
        "package": "string: the compiler package path the report covers.",
        "mode": "string: 'full' or 'facts_only'.",
        "label": "Label: the target that produced the report.",
    },
)

PlaidLintInfo = provider(
    doc = """Aggregatable lint reports. Carried by every target the
plaid aspect visited: the target's own archives' reports plus the
transitive reports of its deps and embeds. An aggregation rule
(plaid_lint_suite_test) consumes this instead of walking bazel-bin —
typed inputs with package identity, which the collector's
test-variant supersede rule needs.""",
    fields = {
        "reports": "depset[PlaidReportInfo]: transitive lint reports.",
    },
)

# Attributes an aspect propagates along. embed edges matter: an
# embedding go_library's archive covers the embedded sources, and
# go_test internal archives reach their library through embed.
_DEP_ATTRS = ["deps", "embed"]

def _pkg_key(data):
    """The compiler package path for a GoArchiveData — the key both
    the importcfg and the dep-facts map use, matching what the dep's
    deep export data self-describes."""
    return data.importmap or data.importpath

def _is_external(target):
    return target.label.workspace_root.startswith("external/")

def _matches_prefix(importpath, prefixes):
    for p in prefixes:
        if importpath == p or importpath.startswith(p if p.endswith("/") else p + "/"):
            return True
    return False

def _resource_set(_os_name, inputs_size):
    """Coarse memory tiers by declared-input count. The input count is
    dominated by the dependency closure's export-data files, which
    tracks type-check working-set size well enough for scheduling;
    refine with measured per-package data when available."""
    if inputs_size > 2000:
        return {"memory": 6144, "cpu": 1}
    if inputs_size > 500:
        return {"memory": 2048, "cpu": 1}
    return {"memory": 512, "cpu": 1}

def _plaid_lint_aspect_impl(target, ctx, config, module_path, facts_only, no_validation, validation_ignore_linters, use_worker, output_suffix):
    if GoArchive not in target:
        return []

    archive = target[GoArchive]

    go = go_context(ctx, ctx.attr)

    # Standard-library types come from rules_go's compiled stdlib tree
    # (a directory artifact laid out pkg/<goos>_<goarch>/<path>.a) —
    # the same input GoCompilePkg resolves std imports against.
    stdlib_dir = ""
    stdlib_inputs = []
    for lib in go.stdlib.libs.to_list():
        stdlib_inputs.append(lib)
        if lib.is_directory and stdlib_dir == "":
            stdlib_dir = lib.path

    # Facts available to this target's archives, keyed by compiler
    # package path (matching importcfg keys by construction). `deps`
    # contribute their own facts; `embed` contributes the embedded
    # target's whole map, because embed flattening lifts the embedded
    # library's deps into this archive's `direct` set.
    by_key = {}
    for dep in getattr(ctx.rule.attr, "embed", []):
        if PlaidFactsInfo in dep:
            by_key.update(dep[PlaidFactsInfo].by_key)
            if GoArchive in dep:
                by_key[_pkg_key(dep[GoArchive].data)] = dep[PlaidFactsInfo].facts
    for dep in getattr(ctx.rule.attr, "deps", []):
        if PlaidFactsInfo in dep and GoArchive in dep:
            by_key[_pkg_key(dep[GoArchive].data)] = dep[PlaidFactsInfo].facts

    env = struct(
        ctx = ctx,
        binary = ctx.executable._plaid_lint,
        go = go,
        config = config,
        module_path = module_path,
        facts_only = facts_only,
        use_worker = use_worker,
        stdlib_dir = stdlib_dir,
        stdlib_inputs = stdlib_inputs,
    )

    base_name = target.label.name + output_suffix
    lints = []
    main = _lint_archive(env, target, archive, base_name, by_key)
    if main:
        lints.append(main)

    # go_test's own archive is the synthesized testmain package
    # (generated-only ⇒ facts_only above). The archives that carry the
    # target's _test.go sources — the internal test archive (package
    # srcs + in-package tests) and the external one (package foo_test)
    # — sit in archive.direct under the test's own label. nogo lints
    # them because it hooks every compile; the aspect must reach them
    # explicitly. The internal archive's facts feed the external one
    # (foo_test imports foo, resolved to the internal archive).
    if ctx.rule.kind == "go_test":
        own = [d for d in archive.direct if d.data.label == target.label]
        internal = [d for d in own if not d.data.importpath.endswith("_test")]
        external = [d for d in own if d.data.importpath.endswith("_test")]
        test_by_key = dict(by_key)
        for d in internal:
            res = _lint_archive(env, target, d, base_name + ".internal", test_by_key)
            if res:
                lints.append(res)
                test_by_key[_pkg_key(d.data)] = res.facts
        for d in external:
            res = _lint_archive(env, target, d, base_name + ".xtest", test_by_key)
            if res:
                lints.append(res)

    transitive_reports = []
    for attr_name in _DEP_ATTRS:
        for dep in getattr(ctx.rule.attr, attr_name, []):
            if PlaidLintInfo in dep:
                transitive_reports.append(dep[PlaidLintInfo].reports)

    if not lints:
        # Nothing of this target's own to lint (no compiled Go srcs);
        # still forward deps' reports so an aggregation rule sees
        # through wrapper targets.
        if transitive_reports:
            return [PlaidLintInfo(reports = depset(transitive = transitive_reports))]
        return []

    reports = depset(
        direct = [
            PlaidReportInfo(
                sarif = l.sarif,
                package = l.package,
                mode = l.mode,
                label = target.label,
            )
            for l in lints
        ],
        transitive = transitive_reports,
    )

    providers = [
        PlaidFactsInfo(facts = main.facts if main else lints[0].facts, by_key = by_key),
        PlaidLintInfo(reports = reports),
    ]
    output_groups = {
        "plaid_report": depset([l.sarif for l in lints]),
        "plaid_facts": depset([l.facts for l in lints]),
    }

    # Validation: one collect over every full-mode SARIF this target
    # produced. Only full-mode (lint-subject) archives gate.
    gating = [l.sarif for l in lints if l.mode == "full"]
    if gating and not no_validation:
        validation_out = ctx.actions.declare_file(base_name + ".plaid-validation")
        vargs = ctx.actions.args()
        vargs.add("collect")
        vargs.add("--fail-on-findings")
        vargs.add("--out", validation_out)
        for l in validation_ignore_linters:
            vargs.add("--ignore-linter", l)
        vargs.add_all(gating)
        ctx.actions.run(
            mnemonic = "ValidatePlaidLint",
            progress_message = "Validating lint results for %{label}",
            executable = env.binary,
            arguments = [vargs],
            inputs = gating,
            outputs = [validation_out],
            execution_requirements = {"supports-path-mapping": "1"},
            toolchain = GO_TOOLCHAIN,
        )
        output_groups["_validation"] = depset([validation_out])

    providers.append(OutputGroupInfo(**output_groups))
    return providers

def _lint_archive(env, target, archive, base_name, by_key):
    """Emits the PlaidLint action for one GoArchive. Returns a struct
    (facts, sarif, mode) or None when the archive has nothing to lint."""
    ctx = env.ctx
    data = archive.data
    srcs = [s for s in data.srcs if s.extension == "go"]
    if not srcs or not data.export_file:
        return None

    facts_out = ctx.actions.declare_file(base_name + ".plaidfacts")
    sarif_out = ctx.actions.declare_file(base_name + ".plaid.sarif")
    cfg_out = ctx.actions.declare_file(base_name + ".plaid-unit.json")
    importcfg_out = ctx.actions.declare_file(base_name + ".plaid-importcfg")

    # Scope: external-repo packages and configured prefix matches run
    # facts_only — their facts feed importers, but they are not lint
    # subjects (nogo's includes/excludes semantics). Likewise archives
    # made entirely of generated files (rules_go's synthesized
    # testmain, protoc output): build machinery, not lint subjects.
    mode = "full"
    if _is_external(target) or _matches_prefix(data.importpath, env.facts_only):
        mode = "facts_only"
    if not any([s.is_source for s in srcs]):
        mode = "facts_only"

    # Direct dep artifacts. Deep gc export data covers transitive
    # types, so direct deps suffice — the nogo model. Facts are
    # selected from by_key by the same key the importcfg uses; deps
    # without an entry (stdlib, implicit runtime deps) contribute no
    # facts, which the driver tolerates.
    importcfg_lines = []
    dep_facts = {}
    dep_inputs = []
    for dep in archive.direct:
        ddata = dep.data
        key = _pkg_key(ddata)
        if ddata.export_file:
            importcfg_lines.append("packagefile %s=%s" % (key, ddata.export_file.path))
            dep_inputs.append(ddata.export_file)
            if ddata.importmap and ddata.importmap != ddata.importpath:
                importcfg_lines.append("importmap %s=%s" % (ddata.importpath, ddata.importmap))
        if key in by_key:
            dep_facts[key] = by_key[key].path
            dep_inputs.append(by_key[key])
    ctx.actions.write(importcfg_out, "\n".join(importcfg_lines) + "\n")

    unit_cfg = {
        "schema": 1,
        "package": {
            "path": _pkg_key(data),
            "go_files": [s.path for s in srcs],
            "goos": env.go.mode.goos,
            "goarch": env.go.mode.goarch,
            "go_version": _major_minor(env.go.sdk.version),
        },
        "deps": {
            "importcfg": importcfg_out.path,
            "facts": dep_facts,
            "stdlib_dir": env.stdlib_dir,
        },
        "analysis": {
            "mode": mode,
        },
        "out": {
            "facts": facts_out.path,
            "sarif": sarif_out.path,
        },
    }

    # go_test archives declare every test source and rely on the
    # compiler's -testfilter to split the internal (package foo) and
    # external (package foo_test) sides; the unit driver mirrors that
    # filter from the same GoInfo field.
    test_filter = getattr(archive.source, "testfilter", None)
    if test_filter in ("only", "exclude"):
        unit_cfg["package"]["test_filter"] = test_filter
    if env.config:
        unit_cfg["analysis"]["config"] = env.config.files.to_list()[0].path
    if env.module_path:
        unit_cfg["module"] = {"path": env.module_path}
    ctx.actions.write(cfg_out, json.encode(unit_cfg))

    inputs = list(srcs) + dep_inputs + env.stdlib_inputs + [cfg_out, importcfg_out]
    if env.config:
        inputs.extend(env.config.files.to_list())

    # NOTE: no supports-path-mapping here — the unit.json and
    # importcfg CONTENTS embed configuration-full paths that Bazel's
    # path mapper cannot rewrite (it maps File-typed command-line
    # arguments, not bytes inside written files), so PlaidLint actions
    # are not safe under --experimental_output_paths=strip. Moving
    # the path universe onto the command line is tracked follow-on
    # work; the validation action passes only File args and keeps the
    # requirement.
    startup_args = ctx.actions.args()
    startup_args.add("unit")
    request_args = ctx.actions.args()
    request_args.add("--cfg", cfg_out.path)
    execution_requirements = {}
    if env.use_worker:
        # Per-request args travel via a params file; the startup args
        # (everything before the first params file) carry only the
        # subcommand. When Bazel runs the action as a persistent
        # worker it strips the trailing @flagfile and launches
        # `plaid-lint unit --persistent_worker`; under every OTHER
        # strategy (sandboxed, remote, dynamic's non-worker branch)
        # the raw argv `plaid-lint unit @flagfile` executes one-shot —
        # so the startup args must NOT contain a worker flag.
        request_args.use_param_file("@%s", use_always = True)
        request_args.set_param_file_format("multiline")
        execution_requirements["supports-workers"] = "1"
        execution_requirements["requires-worker-protocol"] = "json"

    ctx.actions.run(
        mnemonic = "PlaidLint",
        progress_message = "Linting %{label} (plaid-lint)",
        executable = env.binary,
        arguments = [startup_args, request_args],
        inputs = inputs,
        outputs = [facts_out, sarif_out],
        execution_requirements = execution_requirements,
        resource_set = _resource_set,
        toolchain = GO_TOOLCHAIN,
    )

    return struct(facts = facts_out, sarif = sarif_out, mode = mode, package = _pkg_key(data))

def _major_minor(version):
    """'1.26.5' -> '1.26' (types.Config.GoVersion wants a language
    version; a patch level is legal but noisier in action keys)."""
    parts = version.split(".")
    if len(parts) >= 2:
        return parts[0] + "." + parts[1]
    return version

def make_plaid_lint_aspect(
        binary = Label("//cmd/plaid-lint"),
        config = None,
        module_path = "",
        facts_only = [],
        no_validation = False,
        validation_ignore_linters = ["unused"],
        use_worker = False,
        output_suffix = ""):
    """Constructs a plaid_lint aspect bound to a configuration.

    See //bazel:defs.bzl for argument documentation; this factory is
    what lets a consumer bind the .golangci.yml and binary once, in
    their own linters.bzl, and apply the aspect from the command line
    (aspects cannot take label-typed parameters directly).
    """
    attrs = {
        "_plaid_lint": attr.label(
            default = binary,
            executable = True,
            cfg = "exec",
        ),
        # go_context(ctx, ctx.attr) reads the stdlib + mode through
        # this, exactly like rules_go's own rules.
        "_go_context_data": attr.label(
            default = Label("@rules_go//:go_context_data"),
        ),
    }
    if config:
        attrs["_plaid_config"] = attr.label(
            default = config,
            allow_files = True,
        )

    def _impl(target, ctx):
        return _plaid_lint_aspect_impl(
            target,
            ctx,
            config = getattr(ctx.attr, "_plaid_config", None),
            module_path = module_path,
            facts_only = facts_only,
            no_validation = no_validation,
            validation_ignore_linters = validation_ignore_linters,
            use_worker = use_worker,
            output_suffix = output_suffix,
        )

    return aspect(
        implementation = _impl,
        attr_aspects = _DEP_ATTRS,
        attrs = attrs,
        required_providers = [GoArchive],
        toolchains = [GO_TOOLCHAIN],
        doc = "Runs plaid-lint unit per Go package; see @plaid_lint//bazel:defs.bzl.",
    )

def _plaid_module_lint_impl(ctx):
    binary = ctx.executable._plaid_lint
    config_files = ctx.files.config if ctx.attr.config else []
    sarif_out = _emit_module_lint(
        ctx,
        binary,
        ctx.file.go_mod,
        config_files,
        ctx.attr.module_path,
        ctx.label.name,
    )

    validation_out = ctx.actions.declare_file(ctx.label.name + ".plaid-validation")
    vargs = ctx.actions.args()
    vargs.add("collect")
    vargs.add("--fail-on-findings")
    vargs.add("--out", validation_out)
    vargs.add(sarif_out)
    ctx.actions.run(
        mnemonic = "ValidatePlaidLint",
        progress_message = "Validating go.mod lint results",
        executable = binary,
        arguments = [vargs],
        inputs = [sarif_out],
        outputs = [validation_out],
        execution_requirements = {"supports-path-mapping": "1"},
    )

    return [
        DefaultInfo(files = depset([sarif_out])),
        OutputGroupInfo(
            plaid_report = depset([sarif_out]),
            _validation = depset([validation_out]),
        ),
    ]

plaid_module_lint = rule(
    implementation = _plaid_module_lint_impl,
    doc = """Runs the module-scoped linters (e.g. gomoddirectives)
against go.mod. Instantiate once per module — package-level lint
actions deliberately exclude module-scoped linters, whose upstream
wrappers rediscover go.mod through the Go toolchain (see plaid-lint's
unit-mode hermeticity contract).""",
    attrs = {
        "go_mod": attr.label(
            allow_single_file = True,
            mandatory = True,
            doc = "The module's go.mod file.",
        ),
        "module_path": attr.string(
            doc = "The module path (informational; populates module identity).",
        ),
        "config": attr.label(
            allow_files = True,
            doc = "The .golangci.{yml,yaml,json} config file.",
        ),
        "_plaid_lint": attr.label(
            default = Label("//cmd/plaid-lint"),
            executable = True,
            cfg = "exec",
        ),
    },
)

def _emit_module_lint(ctx, binary, go_mod, config_files, module_path, base_name):
    """Emits the module-mode PlaidLint action (go.mod-scoped linters,
    e.g. gomoddirectives) and returns its SARIF output. Shared by
    plaid_module_lint and the suite rule so module coverage cannot
    drift between the two. base_name is the full output base: the
    standalone rule passes its label name (the shipped rc.8 artifact
    name), the suite appends ".module" to avoid colliding with its
    aggregate output."""
    sarif_out = ctx.actions.declare_file(base_name + ".plaid.sarif")
    cfg_out = ctx.actions.declare_file(base_name + ".plaid-unit.json")
    unit_cfg = {
        "schema": 1,
        "package": {},
        "module": {"go_mod": go_mod.path, "path": module_path},
        "analysis": {"mode": "module"},
        "out": {"sarif": sarif_out.path},
    }
    if config_files:
        unit_cfg["analysis"]["config"] = config_files[0].path
    ctx.actions.write(cfg_out, json.encode(unit_cfg))
    args = ctx.actions.args()
    args.add("unit")
    args.add("--cfg", cfg_out.path)
    ctx.actions.run(
        mnemonic = "PlaidModuleLint",
        progress_message = "Linting go.mod (plaid-lint)",
        executable = binary,
        arguments = [args],
        inputs = [cfg_out, go_mod] + config_files,
        outputs = [sarif_out],
        execution_requirements = {},
    )
    return sarif_out

def _suite_test_impl(ctx):
    binary = ctx.executable._plaid_lint

    # Every explicitly listed target must contribute reports: a
    # filegroup, wrapper rule, or non-Go target silently narrowing
    # the enforced scope is exactly the failure mode an aggregate
    # gate exists to prevent, so it is a configuration error here.
    unlintable = [
        str(t.label)
        for t in ctx.attr.targets
        if PlaidLintInfo not in t
    ]
    if unlintable:
        fail("plaid_lint_suite_test %s: targets not visited by the plaid aspect (not Go targets providing GoArchive): %s" %
             (ctx.label, ", ".join(unlintable)))

    reports = depset(transitive = [
        t[PlaidLintInfo].reports
        for t in ctx.attr.targets
    ]).to_list()

    # Only full-mode reports gate: facts_only archives (external
    # repos, generated code, configured prefixes) are dependencies of
    # the lint scope, not subjects.
    sarifs = [r.sarif for r in reports if r.mode == "full"]

    config_files = _setting_config_files(ctx.attr._plaid_config)
    if ctx.file.go_mod:
        sarifs.append(_emit_module_lint(
            ctx,
            binary,
            ctx.file.go_mod,
            config_files,
            ctx.attr.module_path,
            ctx.label.name + ".module",
        ))

    if not sarifs:
        fail("plaid_lint_suite_test %s: no lint reports reachable from targets %s — are they Go targets visited by the plaid aspect?" %
             (ctx.label, ctx.attr.targets))

    aggregate_sarif = ctx.actions.declare_file(ctx.label.name + ".plaid.sarif")
    report_txt = ctx.actions.declare_file(ctx.label.name + ".plaid-report.txt")
    verdict = ctx.actions.declare_file(ctx.label.name + ".plaid-verdict")

    # The collector always succeeds when its INPUTS are usable and
    # writes the verdict; findings live in the verdict, which the test
    # runner enforces. An unreadable/malformed SARIF fails this action
    # — a build error, cleanly distinct from a red test (findings).
    cargs = ctx.actions.args()
    cargs.add("collect")
    cargs.add("--out-sarif", aggregate_sarif)
    cargs.add("--out-text", report_txt)
    cargs.add("--verdict", verdict)
    for l in ctx.attr.ignore_linters:
        cargs.add("--ignore-linter", l)

    # The report list can reach monorepo scale (thousands of files);
    # it travels in a params file (plaid-lint expands @file), while
    # the subcommand and flags stay on the argv proper.
    file_args = ctx.actions.args()
    file_args.add_all(sarifs)
    file_args.use_param_file("@%s", use_always = True)
    file_args.set_param_file_format("multiline")
    ctx.actions.run(
        mnemonic = "PlaidCollect",
        progress_message = "Aggregating %d plaid-lint reports for %%{label}" % len(sarifs),
        executable = binary,
        arguments = [cargs, file_args],
        inputs = sarifs,
        outputs = [aggregate_sarif, report_txt, verdict],
        execution_requirements = {"supports-path-mapping": "1"},
    )

    # The test runner prints the report and gates on the verdict's
    # enforced count. Runfiles resolve via $TEST_SRCDIR (bazel test)
    # with a $0.runfiles fallback (bazel run).
    runner = ctx.actions.declare_file(ctx.label.name + ".plaid-runner.sh")
    ctx.actions.write(
        runner,
        """#!/usr/bin/env bash
set -euo pipefail
RUNFILES="${{TEST_SRCDIR:-$0.runfiles}}/{workspace}"
REPORT="$RUNFILES/{report}"
VERDICT="$RUNFILES/{verdict}"
cat "$REPORT"
enforced="$(sed -n 's/^enforced=//p' "$VERDICT")"
superseded="$(sed -n 's/^superseded=//p' "$VERDICT")"
if [[ "${{superseded:-0}}" -gt 0 ]]; then
  echo "plaid-lint suite: $superseded unused finding(s) superseded by test-variant runs" >&2
fi
if [[ "${{enforced:-0}}" -gt 0 ]]; then
  echo "plaid-lint suite: FAIL — $enforced enforced finding(s)" >&2
  exit 1
fi
echo "plaid-lint suite: clean"
""".format(
            workspace = ctx.workspace_name,
            report = report_txt.short_path,
            verdict = verdict.short_path,
        ),
        is_executable = True,
    )

    return [
        DefaultInfo(
            executable = runner,
            files = depset([aggregate_sarif, report_txt]),
            runfiles = ctx.runfiles(files = [report_txt, verdict]),
        ),
        OutputGroupInfo(
            plaid_report = depset([aggregate_sarif]),
        ),
    ]

def _setting_config_files(config_target):
    """Resolves the //bazel:config label_flag to the .golangci config
    file list. The default flag value points at the empty
    //bazel:no_config sentinel, which means 'plaid-lint defaults'."""
    if config_target == None:
        return []
    return config_target.files.to_list()

def _flag_string(t):
    return t[BuildSettingInfo].value

def _flag_string_list(t):
    return t[BuildSettingInfo].value

def _flag_bool(t):
    return t[BuildSettingInfo].value

# The build settings the suite path is configured through. Aspects
# carried on rule attributes must be top-level values of their
# defining .bzl file — a factory-parameterized aspect cannot ride a
# rule attribute — so configuration travels through build settings
# instead (the same model rules_go uses for nogo selection). Set them
# once in .bazelrc:
#
#   common --@plaid_lint//bazel:config=//:.golangci.yml
#   common --@plaid_lint//bazel:module_path=example.com/mod
#
_SUITE_SETTING_ATTRS = {
    "_plaid_config": attr.label(
        default = Label("//bazel:config"),
        allow_files = True,
    ),
    "_plaid_module_path": attr.label(
        default = Label("//bazel:module_path"),
    ),
    "_plaid_facts_only": attr.label(
        default = Label("//bazel:facts_only"),
    ),
    "_plaid_use_worker": attr.label(
        default = Label("//bazel:use_worker"),
    ),
    "_plaid_lint": attr.label(
        default = Label("//cmd/plaid-lint"),
        executable = True,
        cfg = "exec",
    ),
    "_go_context_data": attr.label(
        default = Label("@rules_go//:go_context_data"),
    ),
}

def _suite_aspect_impl(target, ctx):
    config_files = _setting_config_files(ctx.attr._plaid_config)
    return _plaid_lint_aspect_impl(
        target,
        ctx,
        config = ctx.attr._plaid_config if config_files else None,
        module_path = _flag_string(ctx.attr._plaid_module_path),
        facts_only = _flag_string_list(ctx.attr._plaid_facts_only),
        no_validation = True,
        validation_ignore_linters = [],
        use_worker = _flag_bool(ctx.attr._plaid_use_worker),
        output_suffix = ".suite",
    )

plaid_lint_suite_aspect = aspect(
    implementation = _suite_aspect_impl,
    attr_aspects = _DEP_ATTRS,
    attrs = _SUITE_SETTING_ATTRS,
    required_providers = [GoArchive],
    toolchains = [GO_TOOLCHAIN],
    doc = """The build-settings-configured plaid aspect the suite rule
carries on its `targets` attribute. Report-only per target (the suite
is the gate); configured via the @plaid_lint//bazel:{config,
module_path,facts_only,use_worker} settings. Its outputs use the
".suite" name suffix so it composes with factory aspects applied via
--aspects in the same build.""",
)

plaid_lint_suite_test = rule(
    implementation = _suite_test_impl,
    test = True,
    attrs = dicts.add(_SUITE_SETTING_ATTRS, {
        "targets": attr.label_list(
            aspects = [plaid_lint_suite_aspect],
            doc = "Go targets to lint; the aspect visits their " +
                  "transitive deps/embeds, so top-level targets " +
                  "(binaries, tests) suffice.",
        ),
        "go_mod": attr.label(
            allow_single_file = True,
            doc = "The module's go.mod; when set, module-scoped " +
                  "linters (gomoddirectives) are included in the " +
                  "aggregate. Omit only for module-less scopes.",
        ),
        "module_path": attr.string(
            doc = "The Go module path, for module-scoped linters.",
        ),
        "ignore_linters": attr.string_list(
            doc = "Linters whose findings the suite prints but never " +
                  "fails on. Default empty — the suite exists " +
                  "precisely to enforce `unused` after the " +
                  "test-variant supersede rule.",
        ),
    }),
    doc = """Aggregate lint gate. Collects every SARIF report the
plaid_lint_suite_aspect produced over `targets` (through the typed
PlaidLintInfo provider — never by walking bazel-bin), applies the
test-variant unused supersede rule, and fails the TEST on surviving
findings. Findings, analyzer/infrastructure failures, and config
errors stay distinct: findings fail this test; an unreadable report
fails the PlaidCollect action; a bad .golangci config fails the
PlaidLint actions. `bazel build` on this target produces the
aggregate SARIF and text report without enforcing.""",
)
