// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outputsFinding is one seeded SARIF result for the aggregate-output
// tests: (linter, message, line) on the fixed fixture file p/p.go.
type outputsFinding struct {
	linter  string
	message string
	line    int
}

// writeOutputsSarif writes a single-run SARIF file with explicit
// plaidUnit run identity (package + goFiles), so tests can stage the
// test-variant supersede rule and cross-run dedup precisely.
// writeCollectSarif (collect_cli_test.go) can't: it fixes goFiles.
func writeOutputsSarif(t *testing.T, path, pkgPath string, goFiles []string, findings ...outputsFinding) string {
	t.Helper()
	var results []string
	for _, f := range findings {
		results = append(results, fmt.Sprintf(`{
			"ruleId": %q,
			"level": "warning",
			"message": {"text": %q},
			"locations": [{"physicalLocation": {
				"artifactLocation": {"uri": "p/p.go"},
				"region": {"startLine": %d, "startColumn": 2}
			}}]
		}`, f.linter, f.message, f.line))
	}
	filesJSON, err := json.Marshal(goFiles)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{
		"version": "2.1.0",
		"runs": [{
			"results": [%s],
			"properties": {"plaidUnit": {"package": %q, "mode": "full", "goFiles": %s, "compiles": true}}
		}]
	}`, strings.Join(results, ","), pkgPath, filesJSON)
	if err := os.WriteFile(path, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeOutputsFixture stages the canonical two-run fixture:
//
//   - a library run over p/p.go with an `unused` finding (testOnly)
//     and an errcheck finding;
//   - an internal-test run over a strict superset of those files that
//     repeats the errcheck finding (dedup) and adds a forbidigo one.
//
// After collect: the library's unused finding is superseded (the test
// archive proves testOnly has a referent), the duplicated errcheck
// finding collapses, and 2 diagnostics survive (errcheck, forbidigo).
func writeOutputsFixture(t *testing.T, dir string) (libSarif, testSarif string) {
	t.Helper()
	libSarif = writeOutputsSarif(t, filepath.Join(dir, "lib.plaid.sarif"),
		"example.com/parity/p", []string{"p/p.go"},
		outputsFinding{"unused", "func testOnly is unused", 10},
		outputsFinding{"errcheck", "Error return value is not checked", 20},
	)
	testSarif = writeOutputsSarif(t, filepath.Join(dir, "test.plaid.sarif"),
		"example.com/parity/p", []string{"p/p.go", "p/p_test.go"},
		outputsFinding{"errcheck", "Error return value is not checked", 20},
		outputsFinding{"forbidigo", "use of fmt.Println forbidden by pattern fmt\\.Print.*", 30},
	)
	return libSarif, testSarif
}

// TestCollectOutputs pins the aggregate-artifact contract of
// `plaid-lint collect`: --out-sarif / --out-text / --verdict contents,
// the verdict's exit-code neutralization, and byte determinism. This
// is the file-level contract plaid_lint_suite_test's PlaidCollect
// action and test runner depend on.
func TestCollectOutputs(t *testing.T) {
	t.Run("WritesAggregateArtifacts", func(t *testing.T) {
		dir := t.TempDir()
		libSarif, testSarif := writeOutputsFixture(t, dir)
		outSarif := filepath.Join(dir, "aggregate.plaid.sarif")
		outText := filepath.Join(dir, "report.txt")
		outVerdict := filepath.Join(dir, "verdict")

		code, stdout, stderr := runApp(t, dir, "collect",
			"--out-sarif", outSarif, "--out-text", outText, "--verdict", outVerdict,
			libSarif, testSarif)
		if code != exitSuccess {
			t.Fatalf("exit=%d want %d\nstdout=%q\nstderr=%q", code, exitSuccess, stdout, stderr)
		}

		// Verdict: the exact key=value schema the suite test runner
		// parses with sed. 2 findings survive (errcheck deduped,
		// forbidigo), none ignored, 1 unused finding superseded.
		verdict, err := os.ReadFile(outVerdict)
		if err != nil {
			t.Fatalf("verdict not written: %v", err)
		}
		wantVerdict := "schema=1\nfindings=2\nenforced=2\nignored=0\nsuperseded=1\n"
		if string(verdict) != wantVerdict {
			t.Errorf("verdict mismatch:\ngot  %q\nwant %q", verdict, wantVerdict)
		}

		// Text report: byte-for-byte what was printed to stdout.
		text, err := os.ReadFile(outText)
		if err != nil {
			t.Fatalf("text report not written: %v", err)
		}
		if string(text) != stdout {
			t.Errorf("out-text should equal stdout:\nfile  %q\nstdout %q", text, stdout)
		}
		if !strings.Contains(stdout, "(errcheck)") || !strings.Contains(stdout, "(forbidigo)") {
			t.Errorf("surviving findings missing from report:\n%q", stdout)
		}
		if strings.Contains(stdout, "(unused)") {
			t.Errorf("superseded unused finding leaked into report:\n%q", stdout)
		}
		if strings.Count(stdout, "(errcheck)") != 1 {
			t.Errorf("duplicate errcheck finding should dedup to one line:\n%q", stdout)
		}

		// SARIF: parses, and its result count equals the verdict's
		// findings count.
		body, err := os.ReadFile(outSarif)
		if err != nil {
			t.Fatalf("aggregate SARIF not written: %v", err)
		}
		var doc struct {
			Runs []struct {
				Results []json.RawMessage `json:"results"`
			} `json:"runs"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("aggregate SARIF is not valid JSON: %v\n%s", err, body)
		}
		if len(doc.Runs) != 1 {
			t.Fatalf("aggregate SARIF runs=%d want 1", len(doc.Runs))
		}
		if got := len(doc.Runs[0].Results); got != 2 {
			t.Errorf("aggregate SARIF results=%d want 2 (the verdict's findings count)", got)
		}
	})

	t.Run("VerdictNeutralizesExitCode", func(t *testing.T) {
		// With --verdict, findings are data for the verdict's
		// consumer: exit 0 regardless of --fail-on-findings. This is
		// what lets the PlaidCollect action always succeed while the
		// suite test runner enforces.
		for _, extra := range [][]string{nil, {"--fail-on-findings"}} {
			dir := t.TempDir()
			libSarif, testSarif := writeOutputsFixture(t, dir)
			args := append([]string{"collect", "--verdict", filepath.Join(dir, "verdict")}, extra...)
			args = append(args, libSarif, testSarif)
			code, stdout, stderr := runApp(t, dir, args...)
			if code != exitSuccess {
				t.Errorf("extra=%v: exit=%d want %d — findings must not affect the exit code with --verdict\nstdout=%q\nstderr=%q",
					extra, code, exitSuccess, stdout, stderr)
			}
		}
	})

	t.Run("FailOnFindingsWithoutVerdict", func(t *testing.T) {
		// Without a verdict output, --fail-on-findings still gates
		// directly (the per-target validation action's mode).
		dir := t.TempDir()
		libSarif, testSarif := writeOutputsFixture(t, dir)
		code, stdout, stderr := runApp(t, dir, "collect", "--fail-on-findings", libSarif, testSarif)
		if code != exitIssuesFound {
			t.Fatalf("exit=%d want %d\nstdout=%q\nstderr=%q", code, exitIssuesFound, stdout, stderr)
		}
	})

	t.Run("IgnoredLintersCountInVerdict", func(t *testing.T) {
		dir := t.TempDir()
		libSarif, testSarif := writeOutputsFixture(t, dir)
		outVerdict := filepath.Join(dir, "verdict")
		code, stdout, stderr := runApp(t, dir, "collect",
			"--ignore-linter", "forbidigo", "--verdict", outVerdict, libSarif, testSarif)
		if code != exitSuccess {
			t.Fatalf("exit=%d want %d\nstdout=%q\nstderr=%q", code, exitSuccess, stdout, stderr)
		}
		verdict, err := os.ReadFile(outVerdict)
		if err != nil {
			t.Fatal(err)
		}
		wantVerdict := "schema=1\nfindings=2\nenforced=1\nignored=1\nsuperseded=1\n"
		if string(verdict) != wantVerdict {
			t.Errorf("verdict mismatch:\ngot  %q\nwant %q", verdict, wantVerdict)
		}
		// Ignored findings still print for visibility.
		if !strings.Contains(stdout, "(forbidigo)") {
			t.Errorf("ignored finding should still print:\n%q", stdout)
		}
	})

	t.Run("DeterministicSarif", func(t *testing.T) {
		// Two independent collect runs over the same inputs must
		// produce byte-identical aggregate SARIF — the property that
		// makes the PlaidCollect action cacheable and lets worker
		// vs one-shot modes be byte-compared.
		var bodies [][]byte
		for i := range 2 {
			dir := t.TempDir()
			libSarif, testSarif := writeOutputsFixture(t, dir)
			outSarif := filepath.Join(dir, "aggregate.plaid.sarif")
			code, _, stderr := runApp(t, dir, "collect", "--out-sarif", outSarif, libSarif, testSarif)
			if code != exitSuccess {
				t.Fatalf("run %d: exit=%d want %d stderr=%q", i, code, exitSuccess, stderr)
			}
			body, err := os.ReadFile(outSarif)
			if err != nil {
				t.Fatal(err)
			}
			bodies = append(bodies, body)
		}
		if string(bodies[0]) != string(bodies[1]) {
			t.Errorf("aggregate SARIF is not deterministic:\nrun0 %q\nrun1 %q", bodies[0], bodies[1])
		}
	})
}
