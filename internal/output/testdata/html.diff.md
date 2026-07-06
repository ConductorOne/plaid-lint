# HTML printer — divergence from upstream

plaid-lint's `html` printer emits a **simplified, self-contained**
HTML page rather than upstream golangci-lint's React/Bulma page.

## Why

Upstream's HTML printer pulls in:

- Bulma CSS via cdnjs (`bulma/0.9.2/css/bulma.min.css`)
- highlight.js + the Go language module via cdnjs
- React 17 + ReactDOM 17 + babel-standalone via cdnjs
- A `<script type="text/babel">` block of JSX rendering Issue components

This is fine for a one-off report opened in a browser online, but it:

1. Doesn't render offline (CI artifacts behind a firewall).
2. Pins SRI hashes to specific cdnjs versions that drift.
3. Makes byte-stable golden testing impossible (the embedded JSX is
   small, but the cumulative dependency surface is large).

## What we emit instead

- One `<table>` with rows: position, linter, severity, message, code.
- An inline `<style>` block with ~10 selectors.
- A `<div class="summary">` count.
- The "No diagnostics" empty state matches upstream's "No issues found!"
  in intent.

The structure (one table of diagnostics + a summary) matches the
dispatch brief's stated requirement for HTML.

## Consumer impact

Browser-rendered reports look plain rather than styled-with-Bulma.
Anyone scraping the HTML programmatically should target the `<table>`
rather than upstream's React components (which never existed in the
DOM until the React bundle ran anyway).
