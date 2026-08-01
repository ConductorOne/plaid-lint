"""Workspace lint configuration: binds plaid-lint to this repo's config.

The standalone aspects below (applied via --aspects) use the factory
API with per-target validation. The aggregate suite path
(//:lint, //:lint_clean) uses @plaid_lint//bazel:defs.bzl%
plaid_lint_suite_test instead, configured through the
@plaid_lint//bazel:* build settings in .bazelrc.
"""

load("@plaid_lint//bazel:defs.bzl", "plaid_lint_aspect")

plaid = plaid_lint_aspect(
    config = Label("@@//:.golangci.yml"),
    module_path = "example.com/plaidexample",
)

plaid_worker = plaid_lint_aspect(
    config = Label("@@//:.golangci.yml"),
    module_path = "example.com/plaidexample",
    use_worker = True,
    output_suffix = ".worker",
)
