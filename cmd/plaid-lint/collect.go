// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/conductorone/plaid-lint/internal/unit"
)

// runCollect executes `plaid-lint collect`: aggregate unit-action
// SARIF outputs, print the surviving findings, and optionally fail on
// them. This is what a Bazel validation action (rules_plaid's
// ValidatePlaidLint) and a CI collector run.
//
// Exit codes follow `run` semantics rather than `unit` semantics —
// collect is the consumer that DECIDES, so findings become exit 1
// when --fail-on-findings is set: 0 = clean (or report-only), 1 =
// enforced findings, 2 = bad flags, 3 = unreadable inputs.
func (a *app) runCollect(args []string) int {
	fs := newRunFlagSet("collect", a.stderr)
	g := bindGlobalFlags(fs)
	failOnFindings := fs.Bool("fail-on-findings", false, "exit 1 when findings remain after filtering")
	var ignoreLinters csvSlice
	fs.Var(&ignoreLinters, "ignore-linter", "linter whose findings never cause failure (repeatable / CSV); they are still printed")
	outMarker := fs.String("out", "", "file touched on success (a Bazel validation output)")
	outSarif := fs.String("out-sarif", "", "write the aggregate SARIF report to this path")
	outText := fs.String("out-text", "", "write the aggregate text report to this path")
	outVerdict := fs.String("verdict", "", "write a machine-readable verdict (key=value lines) to this path; findings then never affect the exit code — the consumer of the verdict decides")

	args, aerr := expandArgsFiles(args)
	if aerr != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: collect: %v\n", aerr)
		return exitCLIError
	}
	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		printCollectHelp(a.stdout)
		return exitSuccess
	}
	paths := fs.Args()
	if len(paths) == 0 {
		fmt.Fprintln(a.stderr, "plaid-lint: collect: no SARIF files named")
		return exitCLIError
	}

	res, err := unit.Collect(paths)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitInternalError
	}

	ignored := map[string]bool{}
	for _, l := range ignoreLinters {
		ignored[l] = true
	}
	var text strings.Builder
	enforced := 0
	for _, d := range res.Diagnostics {
		fmt.Fprintf(&text, "%s: %s (%s)\n", d.PosString(), d.Message, d.Linter)
		if !ignored[d.Linter] {
			enforced++
		}
	}
	fmt.Fprint(a.stdout, text.String())
	if res.Superseded > 0 {
		fmt.Fprintf(a.stderr, "plaid-lint: collect: %d unused finding(s) superseded by test-variant runs\n", res.Superseded)
	}

	// Aggregate artifacts. Written before any failure exit so a
	// red verdict still ships its evidence; every writer is
	// deterministic (sorted diagnostics, stable JSON encoding).
	if *outSarif != "" {
		body, serr := unit.RenderAggregateSarif(res.Diagnostics)
		if serr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: collect: render sarif: %v\n", serr)
			return exitInternalError
		}
		if werr := os.WriteFile(*outSarif, body, 0o666); werr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: collect: write sarif: %v\n", werr)
			return exitInternalError
		}
	}
	if *outText != "" {
		if werr := os.WriteFile(*outText, []byte(text.String()), 0o666); werr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: collect: write text: %v\n", werr)
			return exitInternalError
		}
	}
	if *outVerdict != "" {
		verdict := fmt.Sprintf("schema=1\nfindings=%d\nenforced=%d\nignored=%d\nsuperseded=%d\n",
			len(res.Diagnostics), enforced, len(res.Diagnostics)-enforced, res.Superseded)
		if werr := os.WriteFile(*outVerdict, []byte(verdict), 0o666); werr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: collect: write verdict: %v\n", werr)
			return exitInternalError
		}
	}

	// Exit policy: with a verdict output, findings are data — the
	// verdict's consumer (a test runner, a CI gate) decides. Without
	// one, --fail-on-findings gates directly (the per-target
	// validation action's mode).
	if *outVerdict == "" && *failOnFindings && enforced > 0 {
		fmt.Fprintf(a.stderr, "plaid-lint: collect: %d finding(s)\n", enforced)
		return exitIssuesFound
	}
	if *outMarker != "" {
		if err := os.WriteFile(*outMarker, nil, 0o666); err != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: collect: write marker: %v\n", err)
			return exitInternalError
		}
	}
	return exitSuccess
}

// printCollectHelp writes the `collect` help text.
func printCollectHelp(w io.Writer) {
	fmt.Fprintln(w, `plaid-lint collect — aggregate unit-action SARIF outputs.

Usage:
  plaid-lint collect [flags] <file.plaid.sarif>...

Merges results across the named SARIF files, deduplicates by
position, and applies the test-variant supersede rule: when one run
analyzed a strict superset of another's files (an in-package test
archive vs its library), the subset run's 'unused' findings are
dropped — a symbol referenced only from tests is not unused.

Flags:
      --fail-on-findings     exit 1 when findings remain (a Bazel
                             validation action sets this)
      --ignore-linter name   findings from this linter are printed but
                             never cause failure (repeatable / CSV)
      --out path             file touched on success`)
}
