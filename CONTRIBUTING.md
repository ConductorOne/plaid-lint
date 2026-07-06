# Contributing

Thanks for improving `plaid-lint`.

## Local Workflow

Run the core gates from the repository root:

```sh
go build ./...
go vet ./...
go test -p 1 $(go list ./... | grep -Ev '/internal/gopls/internal/(expect|gcimporter|imports)$')
```

There is no Makefile, no vendored dependency tree, and no repository-level `golangci-lint` configuration.
The excluded packages are copied upstream gopls tests whose fixture/proxy data is not present in this fork.

Format only files you change:

```sh
gofmt -w <files>
```

The `internal/gopls/` tree is a narrow fork of upstream `gopls` internals. Keep edits there focused on the intended integration point and avoid applying project-wide formatting or style churn.

## Pull Requests

Keep pull requests scoped to one behavior or cleanup theme. Include tests when changing command behavior, cache selection, analyzer wiring, output formats, or workspace loading semantics.

For shared-cache changes, document the trust boundary explicitly: cache entries can be integrity-checked by digest, but shared writable storage does not authenticate who produced an entry.
