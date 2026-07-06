# JUnit XML printer — divergence from upstream

The plaid-lint `junit-xml` printer is byte-structurally equivalent to
upstream golangci-lint v2.9's `pkg/printers.JUnitXML` but with one
subtle difference around the empty case.

## Empty input

- **Upstream:** writes `<testsuites></testsuites>` (literal empty element).
- **Ours:** writes the same (`encoding/xml` will emit
  `<testsuites></testsuites>` for an empty `TestSuites` slice).

## Otherwise byte-comparable

- `<testsuites>` root.
- One `<testsuite>` per file, with `name`, `tests`, `errors`, `failures`
  attributes. `errors` is always 0 since we route everything through
  `failures`, matching upstream.
- One `<testcase>` per diagnostic, with `name=linter`, `classname=pos`.
- One `<failure>` per testcase with `message`, `type`, and a CDATA body
  containing the rendered diagnostic detail.
- 2-space indent.

## Hidden gotchas

1. **No standard JUnit XML schema exists.** Different consumers (Jenkins,
   GitLab, Bamboo) accept different flavors. The testmoapp/junitxml
   reference is what upstream targets; we target the same.

2. **Some consumers require `testsuite` to live inside `testsuites`**
   even when there's only one suite. We always emit the wrapper —
   matches upstream.

3. **Extended mode** (`file` + `line` attrs on `<testcase>`) is opt-in
   on the printer struct (`JUnitXML.Extended = true`), again matching
   upstream's `cfg.JUnitXML.Extended`.

4. **CDATA encoding** inherits `encoding/xml`'s behavior: characters
   inside `]]>` sequences in a message would prematurely close the
   CDATA section. We don't currently escape that; it's the same risk
   upstream carries. Filed as follow-up; D-79 captures the deferred
   work.
