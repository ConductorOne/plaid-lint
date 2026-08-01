// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCollectSarif writes a minimal single-run SARIF file with one
// finding per (linter, message) pair, stamped with plaidUnit run
// properties the collector reads.
func writeCollectSarif(t *testing.T, path, pkgPath string, findings ...[2]string) string {
	t.Helper()
	var results []string
	for i, f := range findings {
		results = append(results, fmt.Sprintf(`{
			"ruleId": %q,
			"level": "warning",
			"message": {"text": %q},
			"locations": [{"physicalLocation": {
				"artifactLocation": {"uri": "pkg/a.go"},
				"region": {"startLine": %d, "startColumn": 2}
			}}]
		}`, f[0], f[1], 10+i))
	}
	body := fmt.Sprintf(`{
		"version": "2.1.0",
		"runs": [{
			"results": [%s],
			"properties": {"plaidUnit": {"package": %q, "mode": "full", "goFiles": ["pkg/a.go"]}}
		}]
	}`, strings.Join(results, ","), pkgPath)
	if err := os.WriteFile(path, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestCollectCLI pins the `collect` subcommand's exit-code contract
// and its flag surface.
func TestCollectCLI(t *testing.T) {
	t.Run("FailOnFindings", func(t *testing.T) {
		dir := t.TempDir()
		sarif := writeCollectSarif(t, filepath.Join(dir, "a.plaid.sarif"),
			"example.com/p",
			[2]string{"errcheck", "Error return value is not checked"})
		code, stdout, stderr := runApp(t, dir, "collect", "--fail-on-findings", sarif)
		if code != exitIssuesFound {
			t.Fatalf("exit=%d want %d\nstdout=%q\nstderr=%q", code, exitIssuesFound, stdout, stderr)
		}
		if !strings.Contains(stdout, "Error return value is not checked") ||
			!strings.Contains(stdout, "(errcheck)") {
			t.Errorf("finding not printed:\n%q", stdout)
		}
		if !strings.Contains(stderr, "1 finding(s)") {
			t.Errorf("stderr should report the enforced count:\n%q", stderr)
		}
	})

	t.Run("ReportOnlyDefault", func(t *testing.T) {
		// Without --fail-on-findings, findings are printed but the
		// exit code stays 0.
		dir := t.TempDir()
		sarif := writeCollectSarif(t, filepath.Join(dir, "a.plaid.sarif"),
			"example.com/p",
			[2]string{"errcheck", "Error return value is not checked"})
		code, stdout, stderr := runApp(t, dir, "collect", sarif)
		if code != exitSuccess {
			t.Fatalf("exit=%d want %d\nstderr=%q", code, exitSuccess, stderr)
		}
		if !strings.Contains(stdout, "(errcheck)") {
			t.Errorf("finding not printed:\n%q", stdout)
		}
	})

	t.Run("IgnoreLinterWritesMarker", func(t *testing.T) {
		dir := t.TempDir()
		sarif := writeCollectSarif(t, filepath.Join(dir, "a.plaid.sarif"),
			"example.com/p",
			[2]string{"unused", "func helper is unused"})
		marker := filepath.Join(dir, "lint.ok")
		code, stdout, stderr := runApp(t, dir, "collect",
			"--fail-on-findings", "--ignore-linter", "unused", "--out", marker, sarif)
		if code != exitSuccess {
			t.Fatalf("exit=%d want %d\nstdout=%q\nstderr=%q", code, exitSuccess, stdout, stderr)
		}
		// Ignored findings are still printed for visibility.
		if !strings.Contains(stdout, "(unused)") {
			t.Errorf("ignored finding should still print:\n%q", stdout)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("marker not written: %v", err)
		}
	})

	t.Run("NoFiles", func(t *testing.T) {
		code, _, stderr := runApp(t, t.TempDir(), "collect")
		if code != exitCLIError {
			t.Fatalf("exit=%d want %d stderr=%q", code, exitCLIError, stderr)
		}
		if !strings.Contains(stderr, "no SARIF files") {
			t.Errorf("stderr should explain the missing inputs:\n%q", stderr)
		}
	})

	t.Run("MissingFile", func(t *testing.T) {
		dir := t.TempDir()
		code, _, stderr := runApp(t, dir, "collect", filepath.Join(dir, "nope.plaid.sarif"))
		if code != exitInternalError {
			t.Fatalf("exit=%d want %d stderr=%q", code, exitInternalError, stderr)
		}
	})
}
