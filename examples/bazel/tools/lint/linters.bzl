"""Workspace lint configuration: binds plaid-lint to this repo's config."""

load("@plaid_lint//bazel:defs.bzl", "plaid_lint_aspect")

plaid = plaid_lint_aspect(
    config = Label("@@//:.golangci.yml"),
    module_path = "example.com/plaidexample",
)
