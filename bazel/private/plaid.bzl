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

load("@rules_go//go:def.bzl", "GoArchive", "go_context")

GO_TOOLCHAIN = "@rules_go//go:toolchain"

PlaidFactsInfo = provider(
    doc = "Analysis facts produced by plaid-lint for one Go package.",
    fields = {
        "facts": "File: the package's .plaidfacts output.",
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
    data = archive.data
    srcs = [s for s in data.srcs if s.extension == "go"]
    if not srcs or not data.export_file:
        return []

    go = go_context(ctx, ctx.attr)
    binary = ctx.executable._plaid_lint

    base_name = target.label.name + output_suffix
    facts_out = ctx.actions.declare_file(base_name + ".plaidfacts")
    sarif_out = ctx.actions.declare_file(base_name + ".plaid.sarif")
    cfg_out = ctx.actions.declare_file(base_name + ".plaid-unit.json")

    # Scope: external-repo packages and configured prefix matches run
    # facts_only — their facts feed importers, but they are not lint
    # subjects (nogo's includes/excludes semantics). Likewise archives
    # made entirely of generated files (rules_go's synthesized
    # testmain, protoc output): build machinery, not lint subjects.
    mode = "full"
    if _is_external(target) or _matches_prefix(data.importpath, facts_only):
        mode = "facts_only"
    if not any([s.is_source for s in srcs]):
        mode = "facts_only"

    # Direct dep artifacts. Deep gc export data covers transitive
    # types, so direct deps suffice — the nogo model.
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

    for dep in ctx.rule.attr.deps + getattr(ctx.rule.attr, "embed", []):
        if PlaidFactsInfo in dep:
            info = dep[PlaidFactsInfo]
            darchive = dep[GoArchive] if GoArchive in dep else None
            if darchive:
                dep_facts[_pkg_key(darchive.data)] = info.facts.path
                dep_inputs.append(info.facts)

    importcfg_out = ctx.actions.declare_file(base_name + ".plaid-importcfg")
    ctx.actions.write(importcfg_out, "\n".join(importcfg_lines) + "\n")

    # Standard-library types come from rules_go's compiled stdlib tree
    # (a directory artifact laid out pkg/<goos>_<goarch>/<path>.a) —
    # the same input GoCompilePkg resolves std imports against.
    stdlib_dir = ""
    stdlib_inputs = []
    for lib in go.stdlib.libs.to_list():
        stdlib_inputs.append(lib)
        if lib.is_directory and stdlib_dir == "":
            stdlib_dir = lib.path

    unit_cfg = {
        "schema": 1,
        "package": {
            "path": _pkg_key(data),
            "go_files": [s.path for s in srcs],
            "goos": go.mode.goos,
            "goarch": go.mode.goarch,
            "go_version": _major_minor(go.sdk.version),
        },
        "deps": {
            "importcfg": importcfg_out.path,
            "facts": dep_facts,
            "stdlib_dir": stdlib_dir,
        },
        "analysis": {
            "mode": mode,
        },
        "out": {
            "facts": facts_out.path,
            "sarif": sarif_out.path,
        },
    }
    if config:
        unit_cfg["analysis"]["config"] = config.files.to_list()[0].path
    if module_path:
        unit_cfg["module"] = {"path": module_path}
    ctx.actions.write(cfg_out, json.encode(unit_cfg))

    inputs = list(srcs) + dep_inputs + stdlib_inputs + [cfg_out, importcfg_out]
    if config:
        inputs.extend(config.files.to_list())

    args = ctx.actions.args()
    args.add("unit")
    args.add("--cfg", cfg_out.path)
    execution_requirements = {
        "supports-path-mapping": "1",
    }
    if use_worker:
        # Worker protocol: the per-request args must arrive via a
        # params file; plaid-lint expands @file in its non-worker
        # fallback execution.
        args.use_param_file("@%s", use_always = True)
        args.set_param_file_format("multiline")
        execution_requirements["supports-workers"] = "1"
        execution_requirements["requires-worker-protocol"] = "json"
        worker_args = ctx.actions.args()
        worker_args.add("unit")
        worker_args.add("--worker")
        all_args = [worker_args, args]
    else:
        all_args = [args]

    ctx.actions.run(
        mnemonic = "PlaidLint",
        progress_message = "Linting %{label} (plaid-lint)",
        executable = binary,
        arguments = all_args,
        inputs = inputs,
        outputs = [facts_out, sarif_out],
        execution_requirements = execution_requirements,
        resource_set = _resource_set,
        toolchain = GO_TOOLCHAIN,
    )

    providers = [PlaidFactsInfo(facts = facts_out)]
    output_groups = {
        "plaid_report": depset([sarif_out]),
        "plaid_facts": depset([facts_out]),
    }

    # Validation: only full-mode (lint-subject) targets gate.
    if mode == "full" and not no_validation:
        validation_out = ctx.actions.declare_file(base_name + ".plaid-validation")
        vargs = ctx.actions.args()
        vargs.add("collect")
        vargs.add("--fail-on-findings")
        vargs.add("--out", validation_out.path)
        for l in validation_ignore_linters:
            vargs.add("--ignore-linter", l)
        vargs.add(sarif_out.path)
        ctx.actions.run(
            mnemonic = "ValidatePlaidLint",
            progress_message = "Validating lint results for %{label}",
            executable = binary,
            arguments = [vargs],
            inputs = [sarif_out],
            outputs = [validation_out],
            execution_requirements = {"supports-path-mapping": "1"},
            toolchain = GO_TOOLCHAIN,
        )
        output_groups["_validation"] = depset([validation_out])

    providers.append(OutputGroupInfo(**output_groups))
    return providers

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
    sarif_out = ctx.actions.declare_file(ctx.label.name + ".plaid.sarif")
    cfg_out = ctx.actions.declare_file(ctx.label.name + ".plaid-unit.json")
    go_mod = ctx.file.go_mod

    unit_cfg = {
        "schema": 1,
        "package": {},
        "module": {"go_mod": go_mod.path, "path": ctx.attr.module_path},
        "analysis": {"mode": "module"},
        "out": {"sarif": sarif_out.path},
    }
    config_files = []
    if ctx.attr.config:
        config_files = ctx.files.config
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
        execution_requirements = {"supports-path-mapping": "1"},
    )

    validation_out = ctx.actions.declare_file(ctx.label.name + ".plaid-validation")
    vargs = ctx.actions.args()
    vargs.add("collect")
    vargs.add("--fail-on-findings")
    vargs.add("--out", validation_out.path)
    vargs.add(sarif_out.path)
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
