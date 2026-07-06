# `internal/config` schema map

Side-by-side map of upstream golangci-lint's `pkg/config/` schema (master
`72798d3`, "chore: prepare release", surveyed 2026-05-21) against
plaid-lint's `internal/config` mapping. Captures **semantic gotchas**
only — for the canonical struct shape, read upstream directly.

Companion reading: `/faster-golangci-lint/research/r7.md` (16-config
corpus + per-linter complexity buckets).

## Top-level `Config`

| Field            | Upstream type   | YAML key       | plaid-lint mapping | Notes |
| ---------------- | --------------- | -------------- | -------------------- | ----- |
| Version          | string          | `version`      | mirror               | Upstream rejects anything ≠ `"2"`. Per r7, plaid-lint **accepts v1 and v2** (legacy keys downconverted to canonical v2; warning emitted). |
| Run              | `Run`           | `run`          | mirror               | See below. |
| Output           | `Output`        | `output`       | mirror               | v1 `format` → v2 `formats` aliasing handled at decode time. |
| Linters          | `Linters`       | `linters`      | mirror               | v1 `linters-settings` (top-level) collapses into `linters.settings`. |
| Issues           | `Issues`        | `issues`       | mirror               | v1 `exclude-rules`/`exclude-files`/`exclude-dirs`/`exclude-generated` migrate into `linters.exclusions.*`. |
| Severity         | `Severity`      | `severity`     | mirror               | v1 `default-severity` → `default`. |
| Formatters       | `Formatters`    | `formatters`   | mirror (v2-only)     | v2-only section. v1 formatter knobs live under `linters-settings.{gofmt,goimports,gci,gofumpt}` and are mirrored across at canonicalization time. |
| InternalCmdTest  | bool            | n/a            | omit                 | Upstream test-only knob. Not load-bearing. |
| InternalTest     | bool            | n/a            | omit                 | Upstream test-only knob. Not load-bearing. |
| cfgDir, basePath | string (unexp.) | n/a            | mirror (unexported)  | Populated by `Load`; used by relative-path mode. |

## `Run`

| Field                    | YAML key                    | Notes |
| ------------------------ | --------------------------- | ----- |
| Timeout                  | `timeout`                   | `time.Duration`. YAML accepts `"15m"`, `"1h30m"`. |
| Concurrency              | `concurrency`               | int. 0 = auto. |
| Go                       | `go`                        | string. Detected from go.mod if blank (we **don't** detect — empty stays empty; consumers handle). |
| RelativePathMode         | `relative-path-mode`        | Allowed: `wd`, `cfg`, `gomod`, `gitroot`. Validated. |
| BuildTags                | `build-tags`                | []string. |
| ModulesDownloadMode      | `modules-download-mode`     | Allowed: `mod`, `readonly`, `vendor`. Validated. |
| EnableBuildVCS           | `enable-build-vcs`          | bool. |
| ExitCodeIfIssuesFound    | `issues-exit-code`          | int. |
| AnalyzeTests             | `tests`                     | bool. |
| AllowParallelRunners     | `allow-parallel-runners`    | bool. |
| AllowSerialRunners       | `allow-serial-runners`      | bool. |

**v1 legacy keys (accepted, migrated, warned):**

- `run.skip-files` → emit deprecation; rewrite each entry into `linters.exclusions.paths`.
- `run.skip-dirs` → emit deprecation; rewrite each entry into `linters.exclusions.paths` (anchored at directory).
- `run.skip-dirs-use-default` → emit deprecation; no functional equivalent in v2.
- `run.show-stats` → emit deprecation; rewrite to `output.show-stats`.

## `Output`

| Field      | YAML key       | Notes |
| ---------- | -------------- | ----- |
| Formats    | `formats`      | `Formats` struct (per-format sub-config). |
| SortOrder  | `sort-order`   | []string from `{linter, file, severity}`. Validated for dups + valid names. |
| ShowStats  | `show-stats`   | bool. |
| PathPrefix | `path-prefix`  | string. |
| PathMode   | `path-mode`    | string. Allowed: `""`, `"abs"`. |

**v1 legacy keys:**

- `output.format` (single string) → migrated into `Formats.{name}.Path` with default name `text` and stdout path. v1's `colon-prefixed` `<format>:<path>` syntax is parsed into one entry.
- `output.print-issued-lines` / `output.print-linter-name` → migrated into `Formats.Text.PrintIssuedLine` / `Formats.Text.PrintLinterName`.
- `output.sort-results` → silently dropped (v2 is always sorted; not load-bearing).
- `output.uniq-by-line` → migrated to `issues.uniq-by-line`.

### `Formats` sub-structure

Nine named formats: `text`, `json`, `tab`, `html`, `checkstyle`,
`code-climate`, `junit-xml`, `teamcity`, `sarif`. Each is `{path: ...}`
plus per-format extras:

- `text` — `print-linter-name`, `print-issued-lines`, `colors`.
- `tab` — `print-linter-name`, `colors`.
- `junit-xml` — `extended`.

`IsEmpty()` — all `Path` fields blank — means "no explicit output
config" (CLI default takes over).

## `Linters`

| Field      | YAML key      | Notes |
| ---------- | ------------- | ----- |
| Default    | `default`     | `standard`, `all`, `none`, `fast`. Validation deferred (registry knows the linter universe). |
| Enable     | `enable`      | []string. |
| Disable    | `disable`     | []string. |
| FastOnly   | `fast-only`   | CLI-only flag. |
| Settings   | `settings`    | `LintersSettings` (~88 per-linter blocks). |
| Exclusions | `exclusions`  | See below. |

**v1 legacy keys (load-bearing, see r7):**

- `linters.enable-all` → `default: all`.
- `linters.disable-all` → `default: none`.
- `linters.fast` → `default: fast`.
- `linters.presets` (v1 only) → drop with warning (v2 has presets under exclusions, not linters).
- Top-level `linters-settings:` → migrated into `linters.settings:`.

### `Linters.Exclusions` (v2)

| Field        | YAML key        | Notes |
| ------------ | --------------- | ----- |
| Generated    | `generated`     | `lax`, `strict`, `disable`. Validated. Default: `strict` (set by upstream loader; we mirror). |
| WarnUnused   | `warn-unused`   | bool. |
| Presets      | `presets`       | from `{comments, std-error-handling, common-false-positives, legacy}`. Validated. |
| Rules        | `rules`         | []ExcludeRule. See `BaseRule`. |
| Paths        | `paths`         | []string (regex). |
| PathsExcept  | `paths-except`  | []string (regex). |

### `BaseRule` (shared by `ExcludeRule` and `SeverityRule`)

| Field              | YAML key           | Notes |
| ------------------ | ------------------ | ----- |
| Linters            | `linters`          | []string. |
| Path               | `path`             | string (regex). |
| PathExcept         | `path-except`      | string (regex). |
| Text               | `text`             | string (regex). |
| Source             | `source`           | string (regex). |
| InternalReference  | (none, `-`)        | upstream-internal; we omit. |

Validation: at least N non-blank conditions among
`{text, source, path[-except], linters}` (N = 2 for ExcludeRule, 1 for
SeverityRule). `path` + `path-except` count as one. Mutually exclusive
within a rule.

## `Issues`

| Field              | YAML key                | Notes |
| ------------------ | ----------------------- | ----- |
| MaxIssuesPerLinter | `max-issues-per-linter` | int. |
| MaxSameIssues      | `max-same-issues`       | int. |
| UniqByLine         | `uniq-by-line`          | bool. |
| DiffFromRevision   | `new-from-rev`          | string. |
| DiffFromMergeBase  | `new-from-merge-base`   | string. |
| DiffPatchFilePath  | `new-from-patch`        | string. |
| WholeFiles         | `whole-files`           | bool. |
| Diff               | `new`                   | bool. |
| NeedFix            | `fix`                   | bool. |

**v1 legacy keys (migrate into `linters.exclusions.*`):**

- `issues.exclude-rules` → `linters.exclusions.rules`.
- `issues.exclude-files` → `linters.exclusions.paths` (one per entry).
- `issues.exclude-dirs` → `linters.exclusions.paths` (one per entry, treated as path prefix; we anchor `^X(/.*)?$` for safety).
- `issues.exclude-generated` (string `none|default|strict`) → `linters.exclusions.generated` (`disable|lax|strict`; the `default` value maps to `strict` to match v2 default).
- `issues.exclude-generated-strict` (bool) → forces `linters.exclusions.generated: strict` when true.
- `issues.exclude` ([]string) → wrapped into ExcludeRule each, with `Text` + universal `Linters` scope (empty linters list = all).
- `issues.exclude-case-sensitive` → drop with warning (v2 always case-sensitive).
- `issues.exclude-use-default` (bool) → migrate to `linters.exclusions.presets += ["legacy"]` when true.
- `issues.exclude-dirs-use-default` (bool) → drop with warning (v2 dropped builtin dir excludes).
- `issues.include` → drop with warning (v2 removed; preset+rules are the new mechanism).

## `Severity`

| Field    | YAML key  | Notes |
| -------- | --------- | ----- |
| Default  | `default` | string. v1 alias `default-severity` accepted with deprecation. |
| Rules    | `rules`   | []SeverityRule. Each has `BaseRule` + `severity`. |

Validation: if `rules` non-empty, `default` must be set.

## `Formatters` (v2 only)

| Field      | YAML key      | Notes |
| ---------- | ------------- | ----- |
| Enable     | `enable`      | []string from `{gci, gofmt, gofumpt, goimports, golines, swaggo}`. |
| Settings   | `settings`    | `FormatterSettings` (gci/gofmt/gofumpt/goimports/golines/swaggo blocks). |
| Exclusions | `exclusions`  | `FormatterExclusions` (generated, paths, warn-unused). |

**v1 source:** `linters-settings.{gofmt,goimports,gofumpt,gci,golines}`
is mirrored across into `formatters.settings.*` during migration.

## `LintersSettings` (88 per-linter blocks)

We **mirror upstream's full struct shape verbatim** so the canonical
field set is unambiguous. The five complex linters per r7
(revive, gocritic, depguard, nolintlint, forbidigo) plus the heavy
hitter `govet` need extra care because of nested maps and arrays:

| Linter      | Tricky structure | Notes |
| ----------- | ---------------- | ----- |
| `revive`    | `rules[]` array  | Order-sensitive (precedence). Each rule has nested `arguments`. |
| `gocritic`  | `settings: map[string]map[string]any` | Free-form per-check settings. Keys sortable for canonicalization; values pass through verbatim. |
| `depguard`  | `rules: map[string]*DepGuardList` | Map keys sortable. Each list has typed `deny[]` (order-insensitive). |
| `nolintlint`| flat 5 keys      | Trivial. |
| `forbidigo` | `forbid[]` array | Order-insensitive but custom unmarshal: each entry can be a raw string (the pattern) OR a struct `{p, pkg, msg}`. Upstream uses `mapstructure.TextUnmarshallerHookFunc` — we implement via yaml.v3's custom `UnmarshalYAML`. |
| `govet`     | `enable`/`disable`/`enable-all`/`disable-all` + nested `settings: map[string]map[string]any` | Validates mutual exclusivity. |
| `custom`    | `map[string]CustomLinterSettings` | Plugin loader. We accept the shape; loading the plugin is engine-side. |

For all 88 blocks: define the typed struct mirroring upstream, plus a
catch-all `Extra map[string]any \`yaml:",inline"\`` so unknown keys
(future upstream additions) survive the round trip and don't fail the
parser.

## v1 → v2 migration pipeline

```
raw YAML  ──►  decode into both v2 Config + v1 LegacyShim
              ▼
        if version == "2": skip migration (v2-only)
        if version == "1" or "" with legacy keys present:
              migrate(LegacyShim → Config)
              emit []Warning{ field-path, message }
              ▼
        canonicalize defaults (Generated="strict" if blank)
              ▼
        Validate()
              ▼
        return *Config, []Warning, error
```

## Hidden landmines

1. **Upstream v2 rejects v1 outright.** `checkConfigurationVersion` errors when `cfgDir != ""` and `version != "2"`. r7 says plaid-lint must accept both; this is a deliberate divergence from upstream. We accept and emit a deprecation warning rather than mirror upstream's hard rejection.

2. **`Generated` default is set by the loader, not the struct's zero value.** Upstream's `Loader.Load` sets `cfg.Linters.Exclusions.Generated = "strict"` when blank. The struct default is `""`. Our `Load` does the same post-decode.

3. **String-slice from comma-split.** Upstream's `mapstructure.StringToSliceHookFunc(",")` lets a single YAML string `"a,b,c"` decode into a `[]string`. We replicate via `commaSplitSlice` post-decode walker for fields we expect to be CLI-supplied as `--enable=a,b,c`. Less common in pure YAML.

4. **`forbidigo.forbid[]` polymorphic entries.** `["p1", "p2"]` and `[{p: "p1", pkg: "foo"}, {p: "p2"}]` both decode the same struct slice. Upstream uses `mapstructure.TextUnmarshallerHookFunc` + `encoding.TextUnmarshaler` on `ForbidigoPattern`. We mirror.

5. **`gocritic.settings` is `map[string]map[string]any`.** Inner values are untyped (`yaml.Node` in our world); per-check schemas are owned by gocritic, not us. We pass through.

6. **Linter consolidation in v2.** v1's `gosimple`, `stylecheck`, `mnd` consolidated under v2's `staticcheck` and `mnd`-as-deprecated-alias. If a v1 config enables `gosimple`/`stylecheck`, we emit a warning and rewrite to `staticcheck`. (Registry work — T2.3 — actually enforces this; T2.1 just preserves the user-supplied names + emits the warning.)

7. **CLI flag ↔ config-key precedence.** Upstream uses viper, which gives CLI flags precedence over config file. T2.1 ships the config layer only; the CLI/merge semantics are T2.4's territory. Our `Merge(base, overlay)` is overlay-wins for scalars, append-for-slices, recursive-for-maps. Per upstream, CLI is the "overlay" in their merge.

8. **`exclude-rules` deduplication.** Upstream doesn't dedup. Two identical rules count twice. We preserve this — dedup is a tooling decision not a config-layer one.

9. **`run.go` version detection writes a default into multiple linter settings** (`govet.Go`, `revive.Go`, `gocritic.Go`, `paralleltest.Go`, `gofumpt.LangVersion`). That fan-out happens in `Loader.handleGoVersion`. We expose `*Config` post-detection, but the detection logic is engine-adjacent — T2.1 leaves `Run.Go` as the user-set value (or `""`); the linter registry (T2.3) fans out at use-time.

10. **`severity.case-sensitive` (v1)** — still in r7's table but absent from upstream's current struct. We accept-and-drop with a warning.

## Coverage targets

- 100% of v2 top-level keys (Config, Run, Output, Linters, Issues, Severity, Formatters).
- 100% of v2 `Linters.Exclusions` + `BaseRule` shape.
- 100% of v1 → v2 migration keys listed in r7's "v1 → v2 breaking changes" table.
- ≥ 95% of the 88 `LintersSettings` blocks defined with typed structs.
- The remaining ≤ 5% of `LintersSettings` (newest / experimental linters) lands in the `inline` catch-all so configs parse even when the typed shape isn't defined yet.

## Out of scope for T2.1

- Resolving CLI flag ↔ config precedence (T2.4).
- Translating Config into engine-side analyzer settings (T2.3).
- Running plugin loaders for `linters.settings.custom` (engine-side).
- Detecting Go version from go.mod (engine-side).
- Validating that enabled linter names exist (registry — T2.3).
