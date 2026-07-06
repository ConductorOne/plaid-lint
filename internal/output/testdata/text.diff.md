# Text printer — divergence from upstream

The plaid-lint `text` printer is **byte-compatible with upstream's
uncolored output** modulo one deliberate omission.

## Line shape

    <file>:<line>[:<col>]: <message>[ (<linter>)]

Then optionally the source line, then optionally a `^` underline at
the column. Matches upstream's `pkg/printers.Text.printIssue`.

## Color

Upstream's text printer accepts a `Colors` config flag and uses
`github.com/fatih/color` to wrap the position and message in ANSI codes
when enabled. The plaid-lint port omits color entirely: it is a
caller concern (e.g., wrap `os.Stdout` in `colorable.NewColorableStdout`).
Keeping the printer pure lets us reuse it in test code, capture-to-file
flows, and `--no-color` CLI modes without conditional logic.

If the upstream-equivalent of `colored-line-number` is desired, it is
trivially recovered by wrapping the writer; the printer doesn't need
to change.

## Column-zero behavior

When `Pos.Column == 0` the column is unknown; both upstream and
plaid-lint omit the `:col` suffix and skip the underline pointer.
Matches byte-for-byte.

## Source-line indenting

Upstream and ours render tabs in the source-line prefix as tabs, and
all other prefix characters as spaces, so the `^` underline aligns
visually with the offending column. Byte-equivalent.
