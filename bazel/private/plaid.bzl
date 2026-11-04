# Copyright 2026 The plaid-lint Authors. All rights reserved.
# Use of this source code is governed by a BSD-style
# license that can be found in the LICENSE file.

"""Implementation of the plaid_lint aspect and plaid_module_lint rule.

Consumer API lives in //bazel:defs.bzl. The action topology mirrors
rules_go's nogo, except that type information flows between lint
actions instead of coming from compiles:

  PlaidStdlibExport ──stdlib tree──▶ PlaidLint ──sarif──▶ ValidatePlaidLint
  dep PlaidLint ─.plaidexport + .plaidfacts─▶ │          (fails iff findings;
                                              ├─▶ .plaidfacts  _validation group)
                                              └─▶ .plaidexport for dependents

Each PlaidLint action type-checks its package anyway, so it writes the
package's deep gc export data (.plaidexport) and dependents' importcfgs
name that file instead of rules_go's compiled .x. Linting therefore
schedules no GoCompilePkg or GoStdlib action. The .x is still used for
a dependency the aspect did not lint (an implicit dep outside
deps/embed, or a cgo archive in a cgo-enabled build, which the driver
cannot type-check from its original sources).

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

# Every plaid action runs in this exec group. It requires the
# lint_queue constraint, so a build system with a dedicated lint
# execution platform routes lint work there by registering a platform
# carrying @plaid_lint//bazel/platforms:lint_queue; a root module
# without one registers the host fallback //bazel/platforms:host_lint.
LINT_EXEC_GROUP = "lint"

_LINT_EXEC_GROUPS = {
    LINT_EXEC_GROUP: exec_group(
        exec_compatible_with = [str(Label("//bazel/platforms:lint_queue"))],
    ),
}

PlaidFactsInfo = provider(
    doc = "Analysis facts and export data produced by plaid-lint for one Go package.",
    fields = {
        "facts": "File: the package's .plaidfacts output.",
        "by_key": "dict[string, File]: facts files for packages an " +
                  "EMBEDDING target's archive may list as direct deps " +
                  "(this target's direct deps, plus embedded targets' " +
                  "maps), keyed by compiler package path.",
        "export": "File or None: the package's .plaidexport (deep gc " +
                  "export data written by its PlaidLint action). None " +
                  "when the package's export data must come from the " +
                  "compiler (a cgo archive in a cgo-enabled build).",
        "by_key_export": "dict[string, File]: .plaidexport files keyed " +
                         "like by_key, for the same set of packages.",
    },
)

PlaidStdlibInfo = provider(
    doc = "Standard-library export data written by plaid_stdlib_export.",
    fields = {
        "dir": "File: tree artifact laid out <goos>_<goarch>/<importpath>.a " +
               "(raw gc export data), the unit driver's deps.stdlib_dir.",
        "goos": "string: GOOS the tree was built for.",
        "goarch": "string: GOARCH the tree was built for.",
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
        "direct_reports": "depset[PlaidReportInfo]: reports for THIS " +
                          "target's own archives only, with nothing " +
                          "contributed by deps or embeds. What an " +
                          "aggregation rule collects when its scope is " +
                          "the listed targets rather than their closure; " +
                          "empty for a target with no compiled Go sources " +
                          "of its own.",
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

def _is_external(target, match_paths, module_path):
    if not target.label.workspace_root.startswith("external/"):
        return False

    # Bazel roots the main repository at "", but other Bazel-compatible
    # clients (e.g. Bonanza) give the root module a canonical name like
    # "c1+", placing its packages under external/ too. A package inside
    # the configured module path belongs to the main module regardless.
    return not (module_path and _matches_prefix_any(match_paths, [module_path]))

def _is_vendored(target):
    package = target.label.package
    return (
        package == "vendor" or
        package.startswith("vendor/") or
        package == "local_vendor" or
        package.startswith("local_vendor/")
    )

def _matches_prefix(importpath, prefixes):
    for p in prefixes:
        if importpath == p or importpath.startswith(p if p.endswith("/") else p + "/"):
            return True
    return False

def _matches_prefix_any(importpaths, prefixes):
    return any([_matches_prefix(p, prefixes) for p in importpaths])

def _memory_tier_mb(inputs_size):
    if inputs_size > 2000:
        return 6144
    if inputs_size > 500:
        return 2048
    return 512

def _resource_set(_os_name, inputs_size):
    """Coarse memory tiers by declared-input count. The input count is
    dominated by the dependency closure's export-data files, which
    tracks type-check working-set size well enough for scheduling;
    refine with measured per-package data when available, or override
    per target with the plaid-lint-memory-mb tag."""
    return {"memory": _memory_tier_mb(inputs_size), "cpu": 1}

# Lint-only memory override, read from the linted target's tags:
#
#   plaid-lint-memory-mb=<N>           every PlaidLint action of the target
#   plaid-lint-memory-mb=internal:<N>  a go_test's internal test archive(s)
#   plaid-lint-memory-mb=xtest:<N>     a go_test's external (_test) archive(s)
#
# A scoped tag takes precedence over the plain one for its archives.
# The tag only reaches PlaidLint actions; unlike a `resources:memory:N`
# tag on the target itself, it leaves the target's compile and test
# reservations alone.
_MEMORY_TAG_PREFIX = "plaid-lint-memory-mb="
_MEMORY_SCOPES = ["internal", "xtest"]

def _parse_memory_tags(label, rule_kind, tags):
    """Parses plaid-lint-memory-mb tags into {scope: megabytes}.

    The plain form is keyed by "". Malformed values fail analysis with
    a message naming the target and the offending tag: a typo that
    silently reserved nothing would defeat the purpose of the tag.
    """
    out = {}
    for tag in tags:
        if not tag.startswith(_MEMORY_TAG_PREFIX):
            continue
        value = tag[len(_MEMORY_TAG_PREFIX):]
        scope = ""
        if ":" in value:
            scope, _, value = value.partition(":")
            if scope not in _MEMORY_SCOPES:
                fail("%s: tag %r: unknown scope %r (want <N>, internal:<N> or xtest:<N>)" % (label, tag, scope))
            if rule_kind != "go_test":
                fail("%s: tag %r: the %r scope applies only to go_test targets, not %s" % (label, tag, scope, rule_kind))
        if not value.isdigit() or value.startswith("0"):
            fail("%s: tag %r: %r is not a positive integer number of megabytes" % (label, tag, value))
        if scope in out:
            fail("%s: tag %r: more than one plaid-lint-memory-mb tag for %s" % (label, tag, "scope %r" % scope if scope else "the unscoped form"))
        out[scope] = int(value)
    return out

def _memory_requirements(overrides, scope, inputs_size):
    """Execution requirements reserving max(tier, tag) MB for one action.

    Bazel replaces the resource_set's memory with a `resources:memory:N`
    execution requirement, so the requirement carries the larger of the
    two: a tag never lowers a reservation the input-count tier would
    have made. Execution info is not part of the remote-execution
    action digest, so adding or changing the tag reuses cached results.
    """
    mb = overrides.get(scope, overrides.get(""))
    if mb == None:
        return {}
    tier = _memory_tier_mb(inputs_size)
    if mb <= tier:
        return {}
    return {"resources:memory:%d" % mb: ""}

def _plaid_lint_aspect_impl(target, ctx, config, module_path, facts_only, no_validation, validation_ignore_linters, use_worker, output_suffix, stdlib):
    if GoArchive not in target:
        return []

    archive = target[GoArchive]

    go = go_context(ctx, ctx.attr)

    # Standard-library types come from plaid's own stdlib export tree
    # (plaid_stdlib_export: one cached action per Go mode, no
    # compilation). With no tree configured they come from rules_go's
    # compiled stdlib (pkg/<goos>_<goarch>/<path>.a, the GoStdlib
    # action's output) — the same layout, so the driver cannot tell.
    stdlib_dir = ""
    stdlib_inputs = []
    if stdlib:
        info = stdlib[PlaidStdlibInfo]
        if (info.goos, info.goarch) != (go.mode.goos, go.mode.goarch):
            fail("plaid_lint: stdlib export %s is built for %s_%s but %s is configured for %s_%s" %
                 (stdlib.label, info.goos, info.goarch, target.label, go.mode.goos, go.mode.goarch))
        stdlib_dir = info.dir.path
        stdlib_inputs = [info.dir]
    else:
        for lib in go.stdlib.libs.to_list():
            stdlib_inputs.append(lib)
            if lib.is_directory and stdlib_dir == "":
                stdlib_dir = lib.path

    # Facts available to this target's archives, keyed by compiler
    # package path (matching importcfg keys by construction). `deps`
    # contribute their own facts; `embed` contributes the embedded
    # target's whole map, because embed flattening lifts the embedded
    # library's deps into this archive's `direct` set.
    #
    # by_key_export mirrors by_key for export data. A dep without an
    # entry (not visited, or cgo) falls back to its compiled .x.
    by_key = {}
    by_key_export = {}
    for dep in getattr(ctx.rule.attr, "embed", []):
        if PlaidFactsInfo in dep:
            by_key.update(dep[PlaidFactsInfo].by_key)
            by_key_export.update(dep[PlaidFactsInfo].by_key_export)
            if GoArchive in dep:
                _record_dep(by_key, by_key_export, dep)
    for dep in getattr(ctx.rule.attr, "deps", []):
        if PlaidFactsInfo in dep and GoArchive in dep:
            _record_dep(by_key, by_key_export, dep)

    env = struct(
        ctx = ctx,
        memory_overrides = _parse_memory_tags(target.label, ctx.rule.kind, getattr(ctx.rule.attr, "tags", [])),
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

    # go_test's own archive is rules_go's synthesized testmain: no
    # package imports it, so its facts and export data feed nothing,
    # and it is generated-only (never a lint subject). Linting it would
    # only pull in its implicit deps' compiled .x (bzltestutil and its
    # closure live outside deps/embed).
    main = None
    if ctx.rule.kind != "go_test":
        main = _lint_archive(env, target, archive, base_name, by_key, by_key_export, "")
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
    #
    # Classification uses source.testfilter — the same field rules_go's
    # emit_archive keys its own .internal.a/.external.a artifact names
    # on ("exclude" = internal, "only" = external). Importpath is NOT a
    # discriminator: a go_test with only external _test.go sources and
    # no embed infers its internal archive's importpath from the label
    # (".../caep_test"), which also ends in "_test" and previously
    # misfiled it as a second external archive — colliding on the
    # .xtest output names. Identical archives reached through two dep
    # edges are deduplicated by export file; genuinely distinct
    # same-class archives (future rules_go shapes) get enumerated
    # suffixes rather than colliding.
    if ctx.rule.kind == "go_test":
        own = [d for d in archive.direct if d.data.label == target.label]
        internal = []
        external = []
        seen_exports = {}
        for d in own:
            if d.data.export_file and d.data.export_file.path in seen_exports:
                continue
            if d.data.export_file:
                seen_exports[d.data.export_file.path] = None
            if getattr(d.source, "testfilter", None) == "only":
                external.append(d)
            else:
                internal.append(d)
        test_by_key = dict(by_key)
        test_by_key_export = dict(by_key_export)
        for i, d in enumerate(internal):
            suffix = ".internal" if i == 0 else ".internal.%d" % i
            res = _lint_archive(env, target, d, base_name + suffix, test_by_key, test_by_key_export, "internal")
            if res:
                lints.append(res)
                test_by_key[_pkg_key(d.data)] = res.facts
                if res.export:
                    test_by_key_export[_pkg_key(d.data)] = res.export
        for i, d in enumerate(external):
            suffix = ".xtest" if i == 0 else ".xtest.%d" % i
            res = _lint_archive(env, target, d, base_name + suffix, test_by_key, test_by_key_export, "xtest")
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
        # through wrapper targets. direct_reports stays empty: a
        # wrapper is not itself a lint subject, and a targets-scoped
        # suite listing one must say so rather than silently enforce
        # nothing.
        if transitive_reports:
            return [PlaidLintInfo(
                reports = depset(transitive = transitive_reports),
                direct_reports = depset(),
            )]
        return []

    direct_reports = [
        PlaidReportInfo(
            sarif = l.sarif,
            package = l.package,
            mode = l.mode,
            label = target.label,
        )
        for l in lints
    ]
    reports = depset(
        direct = direct_reports,
        transitive = transitive_reports,
    )

    primary = main if main else lints[0]
    providers = [
        PlaidFactsInfo(
            facts = primary.facts,
            by_key = by_key,
            export = primary.export,
            by_key_export = by_key_export,
        ),
        PlaidLintInfo(
            reports = reports,
            direct_reports = depset(direct_reports),
        ),
    ]
    output_groups = {
        "plaid_report": depset([l.sarif for l in lints]),
        "plaid_facts": depset([l.facts for l in lints]),
        "plaid_export": depset([l.export for l in lints if l.export]),
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
            exec_group = LINT_EXEC_GROUP,
        )
        output_groups["_validation"] = depset([validation_out])

    providers.append(OutputGroupInfo(**output_groups))
    return providers

def _record_dep(by_key, by_key_export, dep):
    """Records a visited dep's facts and export data under its key."""
    key = _pkg_key(dep[GoArchive].data)
    info = dep[PlaidFactsInfo]
    by_key[key] = info.facts
    if info.export:
        by_key_export[key] = info.export
    elif key in by_key_export:
        by_key_export.pop(key)

def _is_cgo_archive(env, archive):
    """Whether the archive's compile runs cgo, whose generated sources
    the driver never sees: its own type-check of the original files
    fails, so its export data must come from the compiler."""
    return not env.go.mode.pure and getattr(archive.source, "cgo", False)

def _lint_archive(env, target, archive, base_name, by_key, by_key_export, memory_scope):
    """Emits the PlaidLint action for one GoArchive. Returns a struct
    (facts, sarif, export, mode, package) or None when the archive has
    nothing to lint. export is None for cgo archives in a cgo build.
    memory_scope selects which plaid-lint-memory-mb tag applies:
    "internal" or "xtest" for a go_test's test archives, "" otherwise.

    Deliberately independent of the archive's export_file: the lint
    action must not depend on the compile of its own package."""
    ctx = env.ctx
    data = archive.data
    srcs = [s for s in data.srcs if s.extension == "go"]
    if not srcs:
        return None

    facts_out = ctx.actions.declare_file(base_name + ".plaidfacts")
    sarif_out = ctx.actions.declare_file(base_name + ".plaid.sarif")
    cfg_out = ctx.actions.declare_file(base_name + ".plaid-unit.json")
    importcfg_out = ctx.actions.declare_file(base_name + ".plaid-importcfg")

    # Scope: external-repo and main-repository vendored packages plus
    # configured prefix matches run facts_only — their facts feed importers,
    # but they are not lint subjects (nogo's includes/excludes semantics).
    # Likewise archives made entirely of generated files (rules_go's
    # synthesized testmain, protoc output) are build machinery, not subjects.
    #
    # A facts_only prefix covering a package must cover its test
    # variants too, and test-archive importpaths live in synthesized
    # namespaces: an embed-based go_test appends "_test"
    # (".../unusedpkg_test"), while a no-embed go_test derives BOTH
    # archives' importpaths from the label with NO module prefix
    # ("pkg/ssf/caep/caep_test", "..._test_test"). So test archives
    # (the only ones carrying a testfilter) additionally match by
    # trimmed "_test" suffixes AND by their label package — bare and
    # module-qualified — which is exactly the directory a facts_only
    # tree entry names. Non-test archives match by their canonical
    # importpath only.
    match_paths = [data.importpath]
    if getattr(archive.source, "testfilter", None) in ("exclude", "only"):
        p = data.importpath
        for _ in range(2):
            if not p.endswith("_test"):
                break
            p = p[:-len("_test")]
            match_paths.append(p)
        pkg_dir = target.label.package
        if pkg_dir:
            match_paths.append(pkg_dir)
            if env.module_path:
                match_paths.append(env.module_path + "/" + pkg_dir)
    mode = "full"
    if _is_external(target, match_paths, env.module_path) or _is_vendored(target) or _matches_prefix_any(match_paths, env.facts_only):
        mode = "facts_only"
    if not any([s.is_source for s in srcs]):
        mode = "facts_only"

    # Direct dep artifacts. Deep gc export data covers transitive
    # types, so direct deps suffice — the nogo model. Export data is
    # the dep's own PlaidLint output when the aspect linted it, and
    # the compiled .x only otherwise; facts are selected from by_key
    # by the same key the importcfg uses; deps without an entry
    # (stdlib, implicit runtime deps) contribute no facts, which the
    # driver tolerates.
    importcfg_lines = []
    dep_facts = {}
    dep_inputs = []
    for dep in archive.direct:
        ddata = dep.data
        key = _pkg_key(ddata)
        dep_export = by_key_export.get(key) or ddata.export_file
        if dep_export:
            importcfg_lines.append("packagefile %s=%s" % (key, dep_export.path))
            dep_inputs.append(dep_export)
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
    export_out = None
    if not _is_cgo_archive(env, archive):
        export_out = ctx.actions.declare_file(base_name + ".plaidexport")
        unit_cfg["out"]["export"] = export_out.path

    # go_test archives declare every test source and rely on the
    # compiler's -testfilter to split the internal (package foo) and
    # external (package foo_test) sides; the unit driver mirrors that
    # filter from the same GoInfo field.
    test_filter = getattr(archive.source, "testfilter", None)
    if test_filter in ("only", "exclude"):
        unit_cfg["package"]["test_filter"] = test_filter

    # Build tags: the same set GoCompilePkg's -tags gets (gotags +
    # race/msan implieds; see rules_go go/private/context.bzl). The
    # driver evaluates build constraints against them so its file
    # selection matches the compile action's.
    if env.go.mode.tags:
        unit_cfg["package"]["tags"] = list(env.go.mode.tags)
    # CGO_ENABLED for the compile actions: rules_go's pure mode off
    # means cgo is available. The driver mirrors the builder's cgo
    # file selection from this.
    if not env.go.mode.pure:
        unit_cfg["package"]["cgo"] = True
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

    # Bazel passes resource_set the action's flattened input count,
    # which also includes the tool; count it too, so the tier compared
    # against the tag is the one Bazel computes (barring tool runfiles,
    # which can only matter within a few inputs of a tier boundary).
    execution_requirements.update(_memory_requirements(env.memory_overrides, memory_scope, len(inputs) + 1))

    ctx.actions.run(
        mnemonic = "PlaidLint",
        progress_message = "Linting %{label} (plaid-lint)",
        executable = env.binary,
        arguments = [startup_args, request_args],
        inputs = inputs,
        outputs = [facts_out, sarif_out] + ([export_out] if export_out else []),
        execution_requirements = execution_requirements,
        resource_set = _resource_set,
        exec_group = LINT_EXEC_GROUP,
    )

    return struct(facts = facts_out, sarif = sarif_out, export = export_out, mode = mode, package = _pkg_key(data))

def _major_minor(version):
    """'1.26.5' -> '1.26' (types.Config.GoVersion wants a language
    version; a patch level is legal but noisier in action keys)."""
    parts = version.split(".")
    if len(parts) >= 2:
        return parts[0] + "." + parts[1]
    return version

def _config_exec_lint():
    """cfg for the binary the lint exec group runs: built for that
    group's execution platform, which need not be the default one."""
    return config.exec(LINT_EXEC_GROUP)

def _plaid_stdlib_export_impl(ctx):
    go = go_context(ctx, ctx.attr)
    out = ctx.actions.declare_directory(ctx.label.name)
    srcs = go.sdk.srcs.to_list()

    args = ctx.actions.args()
    args.add("stdlib")
    args.add("--goroot", go.sdk.root_file.dirname)
    args.add("--goos", go.mode.goos)
    args.add("--goarch", go.mode.goarch)
    args.add("--go-version", _major_minor(go.sdk.version))
    if go.mode.tags:
        args.add("--tags", ",".join(go.mode.tags))
    if not go.mode.pure:
        args.add("--cgo")
    args.add("--out", out.path)

    # The SDK's declared sources are the exact file set exported, so
    # the tree does not depend on whether the action is sandboxed.
    files = ctx.actions.args()
    files.add_all([f for f in srcs if f.extension == "go"])
    files.use_param_file("@%s", use_always = True)
    files.set_param_file_format("multiline")

    ctx.actions.run(
        mnemonic = "PlaidStdlibExport",
        progress_message = "Writing standard-library export data for %s_%s (plaid-lint)" % (go.mode.goos, go.mode.goarch),
        executable = ctx.executable._plaid_lint,
        arguments = [args, files],
        inputs = srcs + [go.sdk.root_file],
        outputs = [out],
        exec_group = LINT_EXEC_GROUP,
    )
    return [
        DefaultInfo(files = depset([out])),
        PlaidStdlibInfo(dir = out, goos = go.mode.goos, goarch = go.mode.goarch),
    ]

plaid_stdlib_export = rule(
    implementation = _plaid_stdlib_export_impl,
    doc = """Standard-library export data for plaid lint actions.

Type-checks the Go toolchain's SDK sources with plaid-lint's `stdlib`
mode and writes one deep gc export file per standard-library package,
laid out like rules_go's compiled stdlib (`<goos>_<goarch>/<path>.a`).
The plaid aspects resolve std imports against it instead of the
GoStdlib action's output, so linting never compiles the standard
library. One action per Go mode (GOOS/GOARCH/tags/cgo) in the build,
cached by SDK content like any other action.""",
    attrs = {
        "_plaid_lint": attr.label(
            default = Label("//bazel:binary"),
            executable = True,
            cfg = _config_exec_lint(),
        ),
        "_go_context_data": attr.label(
            default = Label("@rules_go//:go_context_data"),
        ),
    },
    toolchains = [GO_TOOLCHAIN],
    exec_groups = _LINT_EXEC_GROUPS,
)

def make_plaid_lint_aspect(
        binary = Label("//bazel:binary"),
        config = None,
        module_path = "",
        facts_only = [],
        no_validation = False,
        validation_ignore_linters = ["unused"],
        use_worker = False,
        output_suffix = "",
        stdlib_export = Label("//bazel:stdlib_export")):
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
            cfg = _config_exec_lint(),
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
    if stdlib_export:
        attrs["_plaid_stdlib_export"] = attr.label(
            default = stdlib_export,
            providers = [PlaidStdlibInfo],
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
            stdlib = getattr(ctx.attr, "_plaid_stdlib_export", None),
        )

    return aspect(
        implementation = _impl,
        attr_aspects = _DEP_ATTRS,
        attrs = attrs,
        required_providers = [GoArchive],
        toolchains = [GO_TOOLCHAIN],
        exec_groups = _LINT_EXEC_GROUPS,
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
        exec_group = LINT_EXEC_GROUP,
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
            default = Label("//bazel:binary"),
            executable = True,
            cfg = _config_exec_lint(),
        ),
    },
    exec_groups = _LINT_EXEC_GROUPS,
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
        exec_group = LINT_EXEC_GROUP,
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

    # Report scope. "transitive" (the default) enforces every package
    # reachable from `targets` through deps/embeds. "targets" enforces
    # only the listed targets' own archives — their dependencies keep
    # producing export data and .plaidfacts for the roots' analysis
    # (that is the aspect's job and is unchanged), they simply stop
    # being lint subjects of THIS suite.
    if ctx.attr.report_scope == "targets":
        rootless = [
            str(t.label)
            for t in ctx.attr.targets
            if not t[PlaidLintInfo].direct_reports.to_list()
        ]
        if rootless:
            fail(("plaid_lint_suite_test %s: report_scope = \"targets\" enforces only the listed " +
                  "targets' own packages, but these contribute no reports of their own (they only " +
                  "forward their dependencies'): %s. List the Go targets to enforce, or use " +
                  "report_scope = \"transitive\".") %
                 (ctx.label, ", ".join(rootless)))
        report_depsets = [t[PlaidLintInfo].direct_reports for t in ctx.attr.targets]
    else:
        report_depsets = [t[PlaidLintInfo].reports for t in ctx.attr.targets]

    reports = depset(transitive = report_depsets).to_list()

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
        exec_group = LINT_EXEC_GROUP,
    )

    # The test runner prints the report and gates on the verdict's
    # enforced count. Runfiles resolve via $TEST_SRCDIR (bazel test)
    # with a $0.runfiles fallback (bazel run). The main repo's runfiles
    # directory is ctx.workspace_name ("_main") under Bazel, but some
    # Bazel-compatible clients name it after the root module's canonical
    # repo name, so try both, then any directory holding the report.
    runner = ctx.actions.declare_file(ctx.label.name + ".plaid-runner.sh")
    ctx.actions.write(
        runner,
        """#!/usr/bin/env bash
set -euo pipefail
ROOT="${{TEST_SRCDIR:-$0.runfiles}}"
RUNFILES=""
for d in "$ROOT/{workspace}" "$ROOT/{repo}" "$ROOT"/*; do
  if [[ -f "$d/{report}" ]]; then
    RUNFILES="$d"
    break
  fi
done
if [[ -z "$RUNFILES" ]]; then
  echo "plaid-lint suite: cannot find {report} in any runfiles directory under $ROOT" >&2
  exit 2
fi
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
            repo = ctx.label.repo_name,
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
    # Every plaid action runs in the lint exec group, so the binary is
    # configured for that group's platform (one binary configuration
    # for the aspect and the rule alike).
    "_plaid_lint": attr.label(
        default = Label("//bazel:binary"),
        executable = True,
        cfg = _config_exec_lint(),
    ),
    "_go_context_data": attr.label(
        default = Label("@rules_go//:go_context_data"),
    ),
}

# The suite aspect also takes the stdlib export tree, through the
# //bazel:stdlib_export label_flag.
_SUITE_ASPECT_ATTRS = dicts.add(_SUITE_SETTING_ATTRS, {
    "_plaid_stdlib_export": attr.label(
        default = Label("//bazel:stdlib_export"),
        providers = [PlaidStdlibInfo],
    ),
})

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
        stdlib = ctx.attr._plaid_stdlib_export,
    )

plaid_lint_suite_aspect = aspect(
    implementation = _suite_aspect_impl,
    attr_aspects = _DEP_ATTRS,
    attrs = _SUITE_ASPECT_ATTRS,
    required_providers = [GoArchive],
    toolchains = [GO_TOOLCHAIN],
    exec_groups = _LINT_EXEC_GROUPS,
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
    exec_groups = _LINT_EXEC_GROUPS,
    attrs = dicts.add(_SUITE_SETTING_ATTRS, {
        "targets": attr.label_list(
            aspects = [plaid_lint_suite_aspect],
            doc = "Go targets to lint; the aspect visits their " +
                  "transitive deps/embeds, so top-level targets " +
                  "(binaries, tests) suffice. Whether those visited " +
                  "deps are lint SUBJECTS of this suite or only " +
                  "fact/type providers for it is `report_scope`.",
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
        "report_scope": attr.string(
            default = "transitive",
            values = ["transitive", "targets"],
            doc = "Which packages this suite collects and enforces " +
                  "reports for.\n\n" +
                  "\"transitive\" (default): every package reachable " +
                  "from `targets` through deps/embeds — one top-level " +
                  "target gates its whole closure.\n\n" +
                  "\"targets\": only the listed targets' own packages. " +
                  "Dependencies are still analyzed and still supply the " +
                  "export data and .plaidfacts each listed target's " +
                  "analysis consumes, so cross-package facts (printf " +
                  "wrappers, and so on) keep firing on the roots; the " +
                  "dependencies' own findings are simply not this " +
                  "suite's to report or enforce. Use it to gate a " +
                  "subtree of a large repository without inheriting " +
                  "findings from everything it imports. Listing a " +
                  "target that contributes no reports of its own (a " +
                  "wrapper that only forwards its dependencies') is an " +
                  "error under this scope. If you rely on the " +
                  "test-variant `unused` supersede rule, list a " +
                  "package's go_test alongside its go_library — the " +
                  "superseding report must be inside the scope.",
        ),
    }),
    doc = """Aggregate lint gate. Collects every SARIF report the
plaid_lint_suite_aspect produced over `targets` (through the typed
PlaidLintInfo provider — never by walking bazel-bin), applies the
test-variant unused supersede rule, and fails the TEST on surviving
findings. `report_scope` selects whether that collection covers the
targets' whole dependency closure (default) or only the targets
themselves. Findings, analyzer/infrastructure failures, and config
errors stay distinct: findings fail this test; an unreadable report
fails the PlaidCollect action; a bad .golangci config fails the
PlaidLint actions. `bazel build` on this target produces the
aggregate SARIF and text report without enforcing.""",
)
