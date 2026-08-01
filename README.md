# plaid-lint

[![Go Reference](https://pkg.go.dev/badge/github.com/conductorone/plaid-lint.svg)](https://pkg.go.dev/github.com/conductorone/plaid-lint)

`plaid-lint` is a Go linter CLI compatible with `golangci-lint` v2 config and command shapes, backed by an incremental analysis engine tuned for large workspaces.

It reads `.golangci.yml`, `.golangci.yaml`, or `.golangci.json` automatically and supports the familiar `run`, `linters`, `version`, `cache`, `config`, and `help` subcommands.

## Install

Install from source:

```sh
go install github.com/conductorone/plaid-lint/cmd/plaid-lint@latest
```

Build locally:

```sh
go build -o ./plaid-lint ./cmd/plaid-lint
```

Run with Docker after an image has been published:

```sh
docker run --rm -v "$PWD":/src -w /src ghcr.io/conductorone/plaid-lint:latest run ./...
```

## Usage

Run against the current module:

```sh
plaid-lint run ./...
```

Inspect the resolved linter set:

```sh
plaid-lint linters --json
```

Manage the local cache:

```sh
plaid-lint cache status
plaid-lint cache clean
```

Output format selection uses `--out-format` and supports `text`, `json`, `sarif`, `checkstyle`, `codeclimate`, `junit-xml`, `tab`, `html`, and `teamcity`.

## Unit Mode (build-system actions)

`plaid-lint unit` analyzes exactly one package from declared inputs — no `go list`, no module
resolution, no Go toolchain, no network. It is the execution mode a build system (Bazel, a REAPI
executor) invokes per package: dependency types come from compiler export data named by an
importcfg, cross-package analysis facts flow through `.plaidfacts` files along dependency edges,
and diagnostics are written as SARIF 2.1.0 (including suggested-fix edits).

```sh
plaid-lint unit --cfg unit.json
plaid-lint unit --worker     # Bazel persistent-worker JSON protocol
```

`unit.json` (schema 1):

```json
{
  "schema": 1,
  "package": {
    "path": "example.com/mod/pkg/foo",
    "go_files": ["pkg/foo/a.go"],
    "goos": "linux", "goarch": "arm64", "go_version": "1.26"
  },
  "deps": {
    "importcfg": "foo.importcfg",
    "facts": {"example.com/mod/pkg/bar": "bar.plaidfacts"}
  },
  "module": {"go_mod": "go.mod", "path": "example.com/mod"},
  "analysis": {"config": ".golangci.yml", "mode": "full"},
  "out": {"facts": "foo.plaidfacts", "sarif": "foo.plaid.sarif"}
}
```

- `analysis.mode` is `full` (default), `facts_only` (fact-producing analyzers only, no
  diagnostics — for dependencies excluded from the lint scope, like nogo's `-facts_only`), or
  `module` (go.mod-scoped linters such as `gomoddirectives`; run once per module).
- Findings are results, not failures: they are recorded in the SARIF output and never affect the
  exit code. Exit codes: `0` analysis completed, `2` bad flags, `3` unusable inputs or internal
  error, `7` invalid `.golangci` config.
- Every declared output is written on every success — including packages that fail to
  type-check, which surface as `typecheck` findings with an empty fact set.
- All caching is the build system's concern: unit mode reads and writes no plaid-lint caches.

## Cache Configuration

By default, all cache tiers use the local filesystem under the platform cache directory with a `plaid-lint` suffix.

Override the cache location with `PLAID_CACHE_DIR=<path>`. The path is used verbatim with no suffix appended.

Override the cache backend with:

| Variable | Purpose |
| --- | --- |
| `PLAID_CACHE_BACKEND` | Global backend default. Values: `local`, `gocacheprog`. |
| `PLAID_L0_CACHE_BACKEND` | Per-tier override for diagnostic and facts streams. |
| `PLAID_L1_CACHE_BACKEND` | Per-tier override for per-analyzer package results. |
| `PLAID_L2_CACHE_BACKEND` | Per-tier override for export data and package facts. |
| `PLAID_L0_GOCACHEPROG` | Helper command for the L0 cache tier. |
| `PLAID_L1_GOCACHEPROG` | Helper command for the L1 cache tier. |
| `PLAID_L2_GOCACHEPROG` | Helper command for the L2 cache tier. |

When the global backend is `gocacheprog`, L0 and L2 route through the helper while L1 stays local unless `PLAID_L1_CACHE_BACKEND=gocacheprog` is set explicitly.

When a tier selects the `gocacheprog` backend, its `PLAID_<TIER>_GOCACHEPROG` helper command takes precedence over `GOCACHEPROG`; tiers without their own helper continue to use `GOCACHEPROG`. The helper variable does not select a backend by itself. For example, a separate L1 helper requires both `PLAID_L1_CACHE_BACKEND=gocacheprog` and `PLAID_L1_GOCACHEPROG=...`.

Location resolution order is:

```text
PLAID_CACHE_DIR
GOLANGCI_LINT_CACHE
$XDG_CACHE_HOME/plaid-lint
os.UserCacheDir()/plaid-lint
$TMPDIR/plaid-lint-cache
```

If any tier resolves to `gocacheprog`, its `PLAID_<TIER>_GOCACHEPROG` or `GOCACHEPROG` value must point at a helper implementing the Go cache program protocol.

## Shared Cache Trust Model

A shared cache is a performance layer, not a security boundary. `plaid-lint` verifies helper-returned bodies against their content digest to catch corruption, but a writer that can control both the action record and the body can still make them match.

Use separate writable namespaces, bucket prefixes, helper configuration, or IAM policies for jobs with different trust levels. Protected branch CI should not read shared-cache entries that untrusted fork jobs, lower-trust repositories, or unrelated tenants can write.

## Runtime Memory Ceiling

On Linux, `plaid-lint` auto-configures `GOMEMLIMIT` from the cgroup memory limit and sets the Go runtime soft ceiling to 75% of that value.

The auto ceiling is skipped when `GOMEMLIMIT` is already set, when `PLAID_DISABLE_AUTO_GOMEMLIMIT=1`, or when no finite cgroup limit can be detected.

## Development

Run the local validation gates from the repository root:

```sh
go build ./...
go vet ./...
go test -p 1 $(go list ./... | grep -Ev '/internal/gopls/internal/(expect|gcimporter|imports)$')
```

The repository has no Makefile and no vendored dependencies. The excluded test
packages are copied upstream gopls tests whose fixture/proxy data is not present
in this fork.

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the local development workflow.

## License

Apache License 2.0.
