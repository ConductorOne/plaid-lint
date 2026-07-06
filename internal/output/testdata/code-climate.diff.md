# Code Climate printer — divergence from upstream

The plaid-lint `code-climate` printer is **byte-compatible with
upstream** for the issue array shape. Both produce a single line of
JSON (no trailing newline beyond what `json.Encoder` appends) containing
an array of issue objects.

## Per-issue field shape

| Field | Upstream | Ours |
|---|---|---|
| `description` | "<linter>: <text>" | "<linter>: <message>" — same |
| `check_name` | linter | linter — same |
| `severity` | sanitized {info, minor, major, critical, blocker} | same |
| `fingerprint` | md5 of "filename + text + first source line" → uppercase hex | same |
| `location.path` | filename | filename — same |
| `location.lines.begin` | line number | line number — same |

## Severity sanitizer

`{info, minor, major, critical, blocker}` with `critical` as the
default. Matches upstream's
`pkg/printers.CodeClimate.sanitizer.allowedSeverities`.

## Hidden gotchas

1. **GitLab CI Code Quality** expects `severity` to be one of the spec
   set; an upstream-input severity of "error" or "warning" sanitizes to
   "critical", which is louder than callers may expect. This is the
   intended upstream behavior; users mapping plaid-lint severities
   to Code Climate severities should set `Severity` to one of the spec
   values when emitting diagnostics destined for this format.

2. **No `engine_name` field**. Some Code Climate clients require it;
   upstream omits it; we follow upstream.

3. **md5 in `fingerprint`** is intentionally non-crypto; the field
   exists for dedup across runs. We carry `//nolint:gosec` on the call
   site to silence linters.
