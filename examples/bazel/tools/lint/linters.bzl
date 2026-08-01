"""Workspace lint configuration: binds plaid-lint to this repo's config."""

load("@plaid_lint//bazel:defs.bzl", "plaid_lint_aspect")

plaid = plaid_lint_aspect(
    config = Label("@@//:.golangci.yml"),
    module_path = "example.com/plaidexample",
)

# Worker variant: same configuration, PlaidLint runs as a persistent
# JSON-protocol worker. The distinct output_suffix keeps its declared
# outputs from colliding with %plaid when both apply in one build.
plaid_worker = plaid_lint_aspect(
    config = Label("@@//:.golangci.yml"),
    module_path = "example.com/plaidexample",
    use_worker = True,
    output_suffix = ".worker",
)
