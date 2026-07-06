# SARIF printer — divergence from upstream

The plaid-lint `sarif` printer emits SARIF 2.1.0 matching upstream
golangci-lint v2.9's wire shape **with two field-level differences**.

## tool.driver.name

- **Upstream:** `"golangci-lint"`
- **Ours:** `"plaid-lint"`

SARIF consumers (GitHub Code Scanning, Azure DevOps, etc.) key on
`tool.driver.name` to dedupe / route results to the producing tool.
Identifying as plaid-lint is the correct behavior; misrepresenting
the producer would be a security smell.

## startColumn floor

Upstream uses `max(1, issue.Column())` for `region.startColumn`.
We do the same — the SARIF spec says startColumn defaults to 1 when
omitted, and several SARIF validators (including the canonical
`microsoft/sarif-sdk` Multitool) reject `startColumn: 0`.

## Otherwise byte-comparable

Schema URI, version string, JSON field order, sanitized severity values
(`none | note | warning | error`), and the {ruleId, level, message,
locations} per-result shape all match upstream verbatim.

## Hidden gotcha

GitHub Code Scanning specifically requires `runs[].tool.driver.rules`
to be populated if `ruleId` references are used in `results`. Upstream
doesn't emit `rules` either, which means GitHub Code Scanning falls
back to displaying the raw ruleId. We inherit that limitation for now
— populating `rules` is a future enhancement once we have a stable
catalog of linter descriptors (post-Phase 3).
