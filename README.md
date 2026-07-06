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

When the global backend is `gocacheprog`, L0 and L2 route through the helper while L1 stays local unless `PLAID_L1_CACHE_BACKEND=gocacheprog` is set explicitly.

Location resolution order is:

```text
PLAID_CACHE_DIR
GOLANGCI_LINT_CACHE
$XDG_CACHE_HOME/plaid-lint
os.UserCacheDir()/plaid-lint
$TMPDIR/plaid-lint-cache
```

If any tier resolves to `gocacheprog`, `GOCACHEPROG` must point at a helper implementing the Go cache program protocol.

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
