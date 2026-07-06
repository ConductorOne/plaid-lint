# plaid-lint — conventions for agents

This file is read automatically by Claude Code. Other agent tooling (Codex,
etc.) that looks for `AGENTS.md` should read this file too — `AGENTS.md` in
this repo is a symlink to `CLAUDE.md`.

The repo has no Makefile, no `.golangci.yml`, and no vendor directory. There
is nothing to wrap locally; invoke the Go toolchain directly. GitHub workflows
run the same Go gates for pull requests and releases.

## Commands (verified 2026-05-18)

Run from the repo root. No env vars, no flags, no build tags needed.

```
go build ./...               # ~1s — builds everything including cmd/plaid-lint-bench
go vet ./...                 # ~2s
go test -p 1 $(go list ./... | grep -Ev '/internal/gopls/internal/(expect|gcimporter|imports)$')
go test ./internal/bench/... # scoped; ~4s. Prefer when iterating.
```

The CI-equivalent test gate is the sequential package-list command above.
Plain `go test ./...` also enters three copied upstream gopls test packages
whose testdata/proxy fixtures are not present in this fork:
`internal/gopls/internal/expect`, `internal/gopls/internal/gcimporter`, and
`internal/gopls/internal/imports`.

`TestSchedulerIntegration_SABatchEquivalence` in `./internal/pipelinetest/`
is load-sensitive (it asserts the scheduler throttles under a tight RSS
budget) and can fail spuriously on a busy box. Re-run the single test in
isolation before treating a failure as real.

### Lint

`golangci-lint` v2.9 is installed at `/usr/local/bin/golangci-lint`. There
is no project config; it runs with defaults. As of 2026-05-18 the repo
ships with 121 pre-existing issues (50 errcheck, 31 staticcheck,
37 unused, 3 ineffassign) across non-gopls code. Do not try to fix all
of them in an unrelated change. Lint the files you touched:

```
golangci-lint run ./internal/<package>/...
```

### Format

`gofmt -l .` reports ~98 files (75 inside `internal/gopls/` from the
upstream fork, ~23 in plaid-lint's own code — mostly tab-vs-space in
godoc code blocks). Do not run a repo-wide `gofmt -w .` as a drive-by;
format only the files you edited.

## Repo layout

- `cmd/plaid-lint/` — the production CLI.
- `cmd/plaid-lint-bench/` — the benchmark CLI. Entry point for calibration
  runs and gate decisions.
- `internal/bench/` — benchmark harness library backing the CLI.
- `internal/cache/` — L1 (per-(package, analyzer) disk) and L2
  (gcexportdata) caches.
- `internal/l3/` — in-RAM SSA/IR cache, process-lifetime only.
- `internal/scheduler/` — RSS-budget-aware action scheduler.
- `internal/analyzers/` — analyzer bundle (descriptors + staticcheck wrap).
- `internal/workspace/` — CLI-shaped `WorkspaceState` replacing gopls's
  LSP session/folder/view layer.
- `internal/pipelinetest/` — end-to-end pipeline tests with fixture modules
  under `testdata/`.
- `internal/test/{golden,safety,parallel,l3,harness}/` — focused test
  suites, each with its own `testdata/` of fixture modules.
- `internal/gopls/` — narrow fork of `golang.org/x/tools/gopls/internal/cache`
  and its support packages. Inherits upstream's own conventions; do not
  apply plaid-lint house style there.
- `bench/` — checked-in benchmark result JSON.

## Module + build conventions

- Module path: `github.com/conductorone/plaid-lint`.
- Go 1.26. Non-vendored module; `-mod=mod` defaults apply.
- Build tags used by plaid-lint code (excluding the gopls fork):
  - `//go:build h3` — `internal/bench/h3_investigation_test.go`, an
    opt-in investigation test not run by default.
  - The `buildtag_flip` safety fixture exercises tag handling but is
    test data, not a tag you'd ever pass.
- No CGO. No special GOFLAGS.

## Workflow

- Worktrees live as siblings of the main checkout, named
  `/data/squire/src/plaid-lint-<short-branch-name>` (drop the `phase`
  prefix, e.g. `phase1.7-track1` -> `plaid-lint-1.7-track1`).
- Branches merge fast-forward into `main` after review. Never push to
  `main` directly; never force-push `main`.
- Branch names follow the phase the work belongs to: `phase1.7-<topic>`,
  `phase1.6-<topic>`. Soft convention — nothing enforces it.

## What this repo is NOT

Conventions from sibling repos that do **not** apply here:

- No Makefile. Do not run `make dev`, `make test`, `make lint`, or
  any other make target — they don't exist.
- No `CGO_ENABLED=0` requirement. Default Go toolchain settings work.
- No `-mod=vendor`. There is no vendor directory; the module resolves
  from the proxy.
- No `-buildvcs=false` requirement.
- No `-tags=squire` or any Squire-specific build tag. `SQUIRE_ENV_ID`
  being set in the shell does not change how this repo builds.
- No proto generation step, no frontend, no Tilt.
