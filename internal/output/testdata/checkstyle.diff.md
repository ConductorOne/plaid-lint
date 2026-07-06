# Checkstyle printer — divergence from upstream

The plaid-lint `checkstyle` printer is **structurally compatible** with
upstream but uses Go's stdlib `encoding/xml` indenting rather than
upstream's `go-xmlfmt/xmlfmt` post-processing.

## Indenting

- **Upstream:** `xml.Marshal` → `xmlfmt.FormatXML(s, "", "  ")` (2-space
  indent applied by a string-level post-processor).
- **Ours:** `xml.NewEncoder.Indent("", "  ")` (2-space indent applied by
  the encoder).

In practice the two indenters produce equivalent output for the shapes
this printer emits. We avoid a non-stdlib dependency, which is a
plaid-lint house preference (the project doesn't vendor; minimizing
external deps minimizes proxy churn).

## Self-closing elements

`encoding/xml` does not emit self-closing tags by default. An
`<error column="..." line="..." ...>` element is emitted as
`<error ...></error>` rather than `<error ... />`. Both are valid
Checkstyle XML; downstream consumers parse them identically.

## Severity sanitizer

`{ignore, info, warning, error}` set with `error` as the default for
unrecognized severities. Matches upstream byte-for-byte.

## Diagnostic ordering

Files are sorted lexicographically (matches upstream). Diagnostics
within a file preserve input order. Callers wanting deterministic
within-file order should invoke `output.Sort(diags)` first.
