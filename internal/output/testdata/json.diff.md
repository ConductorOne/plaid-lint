# JSON printer — divergence from upstream

plaid-lint's `json` printer is **not byte-compatible** with golangci-lint
v2.9's `pkg/printers.JSON`. The deviations are intentional.

## Wire shape

- **Upstream:** `{"Issues": [...], "Report": {...}}`
- **Ours:** `{"issues": [...]}`

The capitalized field names are a leftover of upstream encoding
`result.Issue` directly with the field names as written in Go. Our
diagnostic model uses lowercase + snake_case json tags so the format
reads as JSON-native rather than Go-native.

The `Report` sibling carries build-tool metadata (enabled linters, build
config warnings) that doesn't fit cleanly into a pure-printer model.
Wiring that in is a T2.4 concern; for now we omit it.

## Per-issue field shape

| Field | Upstream | Ours |
|---|---|---|
| linter name | `FromLinter` | `linter` |
| message | `Text` | `message` |
| severity | `Severity` | `severity` (omitempty) |
| position | `Pos` (token.Position incl. Offset) | `pos` (no offset) |
| source | `SourceLines` | `source_lines` (omitempty) |
| fixes | `SuggestedFixes` (analysis-shaped) | `suggested_fixes` (TextEdit-shaped, omitempty) |
| -- | `ExpectNoLint` / `ExpectedNoLintLinter` (nolintlint state) | (omitted) |
| -- | `LineRange` | (omitted; future) |
| -- | `HunkPos` (diff mode) | (omitted; not implemented) |
| -- | `Pkg` (build pkg ptr) | (omitted; not serialized upstream either via `json:"-"`) |

## Consumer impact

Any downstream consumer that parses upstream's JSON output (typically
`reviewdog`, GitHub Action wrappers, custom CI scripts) will need a
small adapter layer for plaid-lint. The field set is a strict subset
plus the cleaner naming, so the adapter is mechanical.
