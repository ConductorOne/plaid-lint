# TeamCity printer — divergence from upstream

The plaid-lint `teamcity` printer is **byte-compatible with upstream**
for the service-message wire format, with one minor deviation in
attribute escaping order.

## Service-message format

Both emit, per unique linter:

    ##teamcity[inspectionType id='<linter>' name='<linter>' description='<escaped linter>' category='Golangci-lint reports']

Then per diagnostic:

    ##teamcity[inspection typeId='<linter>' message='<escaped msg>' file='<file>' line='<n>' SEVERITY='<sev>']

## Category string

We retain the literal `Golangci-lint reports` category to match
upstream's category string. Downstream TeamCity dashboards that filter
on category continue to work.

## Severity sanitizer

`{INFO, ERROR, WARNING, WEAK WARNING}` (uppercase) with `ERROR` as the
default. The input `Severity` is uppercased before sanitizing, matching
`pkg/printers.TeamCity.Print`.

## Field-length limits

- inspection type `id`, `name`, `category`: 255 chars (rune-counted)
- `description`, `message`, `file`: 4000 chars

Matches upstream's `smallLimit` / `largeLimit` constants byte-for-byte.

## Escaping

The `|` → `||`, `'` → `|'`, `[` → `|[`, `]` → `|]`, `\n` → `|n`,
`\r` → `|r` replacer is byte-for-byte upstream. The replacement
iteration order in `strings.NewReplacer` is documented as stable.

## Hidden gotcha

TeamCity's service-message parser is **per-line**. A message that
itself contains an unescaped `\n` will split the message across two
lines and confuse the parser. Our escaper handles that. A message that
contains the literal sequence `##teamcity[` could still be confusing if
TeamCity treated it as the start of a new message; this is an
escape-the-payload problem that upstream doesn't solve either.
