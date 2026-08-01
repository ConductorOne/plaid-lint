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

// unitUnusedFixtureFiles is a one-package module with a seeded U1000
// violation (the unreferenced unexported func). Any package exercises
// upstream honnef.co/go/tools' SerializedGraph.Merge — whose trace()
// writes "new node, remapping X -> Y" / "deduplicating ..." to the
// real os.Stderr for every node — but the seeded finding lets the
// tests prove the unused analyzer actually ran.
var unitUnusedFixtureFiles = map[string]string{
	"go.mod": "module example.com/unitquiet\n\ngo 1.26\n",
	"scratch/scratch.go": `package scratch

// Touch is exported (used under ExportedIsUsed).
func Touch() {}

// helper is deliberately unreferenced (U1000).
func helper() {}
`,
}

// writeUnusedGolangci writes an unused-only .golangci config into dir
// and returns its path.
func writeUnusedGolangci(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "golangci.unit.yml")
	body := `version: "2"
linters:
  default: none
  enable:
    - unused
`
	if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	return p
}

// buildUnusedUnitAction assembles the standard unused-enabled unit
// fixture: a compiled one-package module and a complete unit.json.
func buildUnusedUnitAction(t *testing.T) (dir, cfgPath, sarifPath string) {
	t.Helper()
	dir, pkgs := buildUnitFixture(t, unitUnusedFixtureFiles)
	pkg := pkgs["example.com/unitquiet/scratch"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeUnusedGolangci(t, dir)
	cfgPath, sarifPath, _ = writeUnitCfg(t, pkg, golangci, nil)
	return dir, cfgPath, sarifPath
}

// assertNoHonnefNoise fails when either known upstream debug-trace
// prefix leaked to the real stderr.
func assertNoHonnefNoise(t *testing.T, realStderr string) {
	t.Helper()
	if strings.Contains(realStderr, "new node, remapping") {
		t.Errorf("unit leaked 'new node, remapping' to stderr; stderr=%q", realStderr)
	}
	if strings.Contains(realStderr, "deduplicating") {
		t.Errorf("unit leaked 'deduplicating' to stderr; stderr=%q", realStderr)
	}
}

// TestUnitCLI_DefaultQuiet_SuppressesDebugTraces is the unit-mode
// counterpart of TestRun_DefaultQuiet: a plain `plaid-lint unit`
// invocation with the unused analyzer enabled must not leak honnef's
// "new node, remapping" / "deduplicating" trace lines to the real
// os.Stderr — the filter is on by default, no flag required. The
// seeded U1000 finding in the SARIF proves the analyzer actually ran
// (so the assertion is not vacuous) and that suppression does not
// change the findings contract.
func TestUnitCLI_DefaultQuiet_SuppressesDebugTraces(t *testing.T) {
	dir, cfgPath, sarifPath := buildUnusedUnitAction(t)

	code, _, appStderr, realStderr := runAppCapturingRealStderr(t, dir, "unit", "--cfg", cfgPath)
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitSuccess, appStderr)
	}
	assertNoHonnefNoise(t, realStderr)

	var found bool
	for _, r := range readSarifResults(t, sarifPath) {
		if r.RuleID == "unused" {
			found = true
		}
	}
	if !found {
		t.Errorf("seeded U1000 violation missing from SARIF; the unused analyzer did not run, so the suppression assertion is vacuous")
	}
}

// TestUnitCLI_QuietFalse_EmitsDebugTraces pins the escape hatch (the
// same one `run` has, via the shared global --quiet flag): passing
// --quiet=false with no LOG_LEVEL override skips the filter, and the
// honnef trace lines reach the real stderr. This also proves the
// fixture genuinely exercises the noisy upstream path — without it,
// the default-quiet test above could pass without ever suppressing
// anything.
func TestUnitCLI_QuietFalse_EmitsDebugTraces(t *testing.T) {
	t.Setenv("LOG_LEVEL", "")
	dir, cfgPath, _ := buildUnusedUnitAction(t)

	code, _, appStderr, realStderr := runAppCapturingRealStderr(t, dir, "unit", "--quiet=false", "--cfg", cfgPath)
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitSuccess, appStderr)
	}
	if !strings.Contains(realStderr, "new node, remapping") {
		t.Errorf("--quiet=false did not surface 'new node, remapping'; the fixture no longer exercises honnef's trace path (stderr=%q)", realStderr)
	}
}

// TestUnitCLI_Quiet_CompileWarningStillSurfaces: the default-on filter
// drops ONLY the known noise prefixes — plaid-lint's own warnings keep
// flowing. A facts_only action over a package that does not compile
// must still print its "does not compile" warning while the honnef
// noise stays suppressed, and the exit code stays 0 per the unit
// contract.
func TestUnitCLI_Quiet_CompileWarningStillSurfaces(t *testing.T) {
	dir, pkgs := buildUnitFixture(t, map[string]string{
		"go.mod":           "module example.com/unitquiet\n\ngo 1.26\n",
		"broken/broken.go": "package broken\n\nfunc use() { undefinedSymbol() }\n",
	})
	pkg := pkgs["example.com/unitquiet/broken"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeUnusedGolangci(t, dir)
	cfgPath, _, _ := writeUnitCfg(t, pkg, golangci, nil)
	setUnitCfgMode(t, cfgPath, "facts_only")

	code, _, appStderr, realStderr := runAppCapturingRealStderr(t, dir, "unit", "--cfg", cfgPath)
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitSuccess, appStderr)
	}
	if !strings.Contains(appStderr, "does not compile") {
		t.Errorf("does-not-compile warning missing under default quiet; stderr=%q", appStderr)
	}
	assertNoHonnefNoise(t, realStderr)
}

// TestUnitWorker_DefaultQuiet_SuppressesDebugTraces covers the
// persistent-worker mode end-to-end through the real dispatch path
// (`unit --worker`), where the filter is installed before the worker
// loop starts: one WorkRequest over stdin runs the unused-enabled
// action, answers exitCode 0 on stdout, and leaks no honnef trace
// lines to the real stderr.
func TestUnitWorker_DefaultQuiet_SuppressesDebugTraces(t *testing.T) {
	dir, cfgPath, sarifPath := buildUnusedUnitAction(t)

	origStdin := os.Stdin
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdin = pr
	t.Cleanup(func() { os.Stdin = origStdin })
	if _, err := fmt.Fprintf(pw, `{"arguments":["--cfg",%q],"requestId":1}`+"\n", cfgPath); err != nil {
		t.Fatalf("write work request: %v", err)
	}
	_ = pw.Close()

	code, stdout, appStderr, realStderr := runAppCapturingRealStderr(t, dir, "unit", "--worker")
	if code != exitSuccess {
		t.Fatalf("worker exit=%d want %d stderr=%q", code, exitSuccess, appStderr)
	}
	assertNoHonnefNoise(t, realStderr)

	var resp workResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &resp); err != nil {
		t.Fatalf("decode work response %q: %v", stdout, err)
	}
	if resp.ExitCode != 0 || resp.RequestID != 1 {
		t.Errorf("response exitCode=%d requestId=%d output=%q; want 0/1", resp.ExitCode, resp.RequestID, resp.Output)
	}
	if len(readSarifResults(t, sarifPath)) == 0 {
		t.Errorf("worker action produced no findings; expected the seeded U1000 violation")
	}
}

// setUnitCfgMode patches an on-disk unit.json's analysis.mode.
func setUnitCfgMode(t *testing.T, cfgPath, mode string) {
	t.Helper()
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	analysis, _ := m["analysis"].(map[string]any)
	if analysis == nil {
		analysis = map[string]any{}
		m["analysis"] = analysis
	}
	analysis["mode"] = mode
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, out, 0o666); err != nil {
		t.Fatal(err)
	}
}
