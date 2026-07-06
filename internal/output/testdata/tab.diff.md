# Tab printer — divergence from upstream

The plaid-lint `tab` printer is **byte-compatible with upstream's
uncolored output**.

## Format

    <file>:<line>[:<col>]<TAB>[<linter><TAB>]<message><NL>

Run through `text/tabwriter` with `minwidth=0, tabwidth=0, padding=2,
padchar=' '`. Matches upstream's
`pkg/printers.Tab.Print → tabwriter.NewWriter(p.w, 0, 0, 2, ' ', 0)`.

## colored-tab

Upstream's v2.9 config exposes `formats.tab.colors` as a boolean, which
the same Tab printer reads to decide whether to wrap fields in ANSI
codes. plaid-lint treats color as a writer-level concern (see
text.diff.md for the same rationale). There is no separate
`colored-tab` printer in upstream either, despite earlier documentation
that listed it alongside `tab`.

## Counted as one format

The 9-printer total in this dispatch counts `tab` once, not twice. The
`colored-tab` mode is a flag, not a distinct printer.

## Otherwise byte-comparable

Per-column tab alignment, column-zero omission of `:col`, and the
optional linter-name column all match upstream.
