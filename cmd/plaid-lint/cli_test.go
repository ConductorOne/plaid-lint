// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runApp executes the CLI with the supplied args against a temp
// stdout/stderr pair and returns (exitCode, stdout, stderr). Runs in
// the supplied workdir so config discovery walks the right tree.
func runApp(t *testing.T, workdir string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workdir); err != nil {
		t.Fatalf("chdir %s: %v", workdir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	a := &app{
		args:   args,
		stdout: &stdout,
		stderr: &stderr,
	}
	code := a.run()
	return code, stdout.String(), stderr.String()
}

// fixtureRepo creates a minimal repo with a .golangci.yml whose
// content the caller supplies. Returns the absolute repo path.
//
// The fixture is a valid Go module rooted at the returned path so
// the production engine path (packages.Load → Snapshot.Analyze)
// can drive it without needing an external module. A trivial
// `package main` source file is included so the workspace has at
// least one analyzable package; tests that want a finding install
// their own source.
func fixtureRepo(t *testing.T, configBody string) string {
	t.Helper()
	dir := t.TempDir()
	writeGoModule(t, dir)
	if configBody != "" {
		if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte(configBody), 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}
	return dir
}

// fixtureModule creates an empty Go module directory with no
// .golangci.yml. Tests that drive `run --no-config` against an
// arbitrary cwd use this so the engine path can load packages.
func fixtureModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeGoModule(t, dir)
	return dir
}

// writeGoModule stamps a go.mod + trivial main.go into dir so the
// engine's packages.Load step succeeds.
func writeGoModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module plaid-lint-cli-test\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
}

func TestVersion_Short(t *testing.T) {
	code, stdout, stderr := runApp(t, t.TempDir(), "version", "--short")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "v0-dev") {
		t.Fatalf("stdout=%q does not contain version", stdout)
	}
}

func TestVersion_JSON(t *testing.T) {
	code, stdout, _ := runApp(t, t.TempDir(), "version", "--json")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var v versionInfo
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("decode json: %v\nstdout=%q", err, stdout)
	}
	if v.Version == "" {
		t.Fatalf("empty version")
	}
}

func TestTopLevelHelp(t *testing.T) {
	code, stdout, _ := runApp(t, t.TempDir(), "--help")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	for _, want := range []string{
		"plaid-lint",
		"Available Commands:",
		"run",
		"linters",
		"version",
		"cache",
		"config",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help output missing %q\n%s", want, stdout)
		}
	}
}

func TestUnknownSubcommand(t *testing.T) {
	code, _, stderr := runApp(t, t.TempDir(), "totally-not-a-real-subcommand")
	if code != exitCLIError {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitCLIError, stderr)
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("stderr missing diagnostic: %q", stderr)
	}
}

func TestRun_NoConfigSucceeds(t *testing.T) {
	dir := fixtureModule(t)
	code, stdout, stderr := runApp(t, dir, "run", "--no-config")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRun_AutoDiscoversConfig(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - govet
`)
	code, _, stderr := runApp(t, dir, "run")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// Verbose mode should announce the loaded path.
	code, _, stderr = runApp(t, dir, "run", "-v")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, ".golangci.yml") {
		t.Errorf("verbose stderr missing config path announcement: %q", stderr)
	}
}

func TestRun_ConfigErrorExitCode(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
run:
  modules-download-mode: not-a-mode
`)
	code, _, stderr := runApp(t, dir, "run")
	if code != exitConfigError {
		t.Fatalf("exit=%d want %d stderr=%q", code, exitConfigError, stderr)
	}
	if !strings.Contains(stderr, "modules-download-mode") {
		t.Errorf("stderr missing field path: %q", stderr)
	}
}

func TestRun_ExplicitConfigFlag(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "ci.yml")
	if err := os.WriteFile(cfgPath, []byte(`version: "2"
linters:
  default: none
  enable: [govet]
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Run from an unrelated working dir (with go.mod so the
	// engine's packages.Load step can succeed).
	code, _, stderr := runApp(t, fixtureModule(t), "run", "--config", cfgPath, "-v")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "ci.yml") {
		t.Errorf("verbose stderr missing config path: %q", stderr)
	}
}

func TestRun_V1MigrationWarning(t *testing.T) {
	dir := fixtureRepo(t, `linters:
  enable-all: true
`)
	// --issues-exit-code=0 because enable-all enables revive whose
	// package-comments rule fires on the trivial fixture. This test
	// is about the v1→v2 warning, not the issue count.
	code, _, stderr := runApp(t, dir, "run", "--issues-exit-code=0")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("expected migration warnings; got stderr=%q", stderr)
	}
}

func TestRun_PositionalArgsPreserved(t *testing.T) {
	dir := fixtureModule(t)
	// Just sanity-check that positional args don't break parsing.
	code, _, stderr := runApp(t, dir, "run", "--no-config", "./...", "./pkg/foo/...")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

func TestLinters_PrintsEnabled(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - govet
`)
	code, stdout, stderr := runApp(t, dir, "linters")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "errcheck") || !strings.Contains(stdout, "govet") {
		t.Errorf("linters listing missing enabled names: %q", stdout)
	}
}

func TestLinters_JSONOutput(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable: [errcheck]
`)
	code, stdout, _ := runApp(t, dir, "linters", "--json")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var entries []linterListEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("decode json: %v\nstdout=%q", err, stdout)
	}
	found := false
	for _, e := range entries {
		if e.Name == "errcheck" && e.Enabled {
			found = true
		}
	}
	if !found {
		t.Errorf("errcheck not enabled in JSON output: %+v", entries)
	}
}

func TestLinters_CLIOverlayWins(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: standard
`)
	// --default=none plus --enable should produce just the enabled set.
	code, stdout, _ := runApp(t, dir, "linters", "--default=none", "--enable=govet", "--json")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var entries []linterListEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	enabled := map[string]bool{}
	for _, e := range entries {
		if e.Enabled {
			enabled[e.Name] = true
		}
	}
	if !enabled["govet"] {
		t.Errorf("--enable=govet not honored: %+v", enabled)
	}
	if enabled["errcheck"] {
		t.Errorf("--default=none didn't clear the standard set: %+v", enabled)
	}
}

func TestCache_StatusOnEmpty(t *testing.T) {
	// Point GOLANGCI_LINT_CACHE at a tmp dir so we don't touch the
	// real cache.
	t.Setenv("GOLANGCI_LINT_CACHE", filepath.Join(t.TempDir(), "cache"))
	code, stdout, _ := runApp(t, t.TempDir(), "cache", "status")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout, "Dir:") {
		t.Errorf("cache status output missing Dir: %q", stdout)
	}
}

func TestCache_StatusPopulated(t *testing.T) {
	// Stage a fake cache with a few files of known size and verify
	// the status command emits the humanized format ("Size: <h> on
	// disk (<n> files)") instead of the raw-bytes form.
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(filepath.Join(cacheDir, "l1", "analyzer", "fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		p := filepath.Join(cacheDir, "l1", "analyzer", "fake", fmt.Sprintf("entry-%d", i))
		if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PLAID_CACHE_DIR", cacheDir)
	code, stdout, _ := runApp(t, t.TempDir(), "cache", "status")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, "on disk") {
		t.Errorf("status output missing 'on disk' suffix (regression: raw-bytes format?): %q", stdout)
	}
	if !strings.Contains(stdout, "(3 files)") {
		t.Errorf("status output missing file count: %q", stdout)
	}
}

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{1024 * 1024, "1.0M"},
		{1024*1024*1024 + 1024*1024*800, "1.8G"}, // ~1.8 GiB, matches du -h
		{int64(1024) * 1024 * 1024 * 1024, "1.0T"},
	}
	for _, tc := range cases {
		if got := humanizeBytes(tc.in); got != tc.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCache_CleanIdempotent(t *testing.T) {
	t.Setenv("GOLANGCI_LINT_CACHE", filepath.Join(t.TempDir(), "cache"))
	for i := 0; i < 2; i++ {
		code, _, stderr := runApp(t, t.TempDir(), "cache", "clean")
		if code != 0 {
			t.Fatalf("iter %d exit=%d stderr=%q", i, code, stderr)
		}
	}
}

func TestConfig_PathWithFile(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"`)
	code, stdout, _ := runApp(t, dir, "config", "path")
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stdout, ".golangci.yml") {
		t.Errorf("path output missing config name: %q", stdout)
	}
}

func TestConfig_PathNoFile(t *testing.T) {
	// Run from a tmp dir with no config anywhere up the chain. (May
	// pick up $HOME global if present; assert at most that the
	// command succeeds.)
	dir := t.TempDir()
	t.Setenv("HOME", dir) // override $HOME so the walk-up doesn't find a global
	code, _, _ := runApp(t, dir, "config", "path")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
}

func TestHelp_RunSubcommand(t *testing.T) {
	code, stdout, _ := runApp(t, t.TempDir(), "help", "run")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout, "--enable") {
		t.Errorf("help run missing --enable: %q", stdout)
	}
}

func TestRun_OutputJSONToFile(t *testing.T) {
	dir := fixtureModule(t)
	outFile := filepath.Join(dir, "issues.json")
	code, _, stderr := runApp(t, dir, "run",
		"--no-config",
		"--output.json.path="+outFile,
	)
	if code != 0 && code != exitIssuesFound {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// JSON file must be at least an array, regardless of whether
	// the fixture surfaces diagnostics.
	body, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read %s: %v", outFile, err)
	}
	if !strings.Contains(string(body), "[") {
		t.Errorf("json output looks malformed: %q", body)
	}
}

// TestRun_EngineSurfacesDiagnostics is the Phase 3.5 smoke test: a
// known-bad source file should surface at least one diagnostic and
// produce a non-zero exit code. Confirms the engine wiring (config
// → registry → engine.Run → printer) is end-to-end functional.
func TestRun_EngineSurfacesDiagnostics(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	// Overwrite main.go with a file that has a clear ineffassign
	// finding (assignment to x is never read).
	src := `package main

func main() {
	x := 1
	x = 2
	_ = x
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	code, stdout, stderr := runApp(t, dir, "run")
	if code != exitIssuesFound {
		t.Fatalf("exit=%d want %d stdout=%q stderr=%q", code, exitIssuesFound, stdout, stderr)
	}
	if !strings.Contains(stdout, "ineffassign") && !strings.Contains(stdout, "ineffectual") {
		t.Errorf("expected ineffassign diagnostic in stdout, got: %q (stderr=%q)", stdout, stderr)
	}
}

// TestRun_Determinism is the Phase 3.5 cold↔warm preservation
// check: invoking `plaid-lint run` twice in a row against the
// same source must produce byte-identical diagnostic output. This
// is the production analogue of bench's W6 cold↔warm digest
// equivalence assertion.
func TestRun_Determinism(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	src := `package main

func main() {
	x := 1
	x = 2
	_ = x
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	// Pin XDG_CACHE_HOME so both runs share the same L1/L2 dirs.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	_, out1, _ := runApp(t, dir, "run")
	_, out2, _ := runApp(t, dir, "run")
	if out1 != out2 {
		t.Errorf("non-deterministic diagnostic output across consecutive runs:\nrun1=%q\nrun2=%q", out1, out2)
	}
}

func TestRun_DefaultsToExitZeroOnNoDiagnostics(t *testing.T) {
	// Use linters.default: none so the in-process analyzer set is
	// empty and the trivial fixture cannot surface a diagnostic.
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
`)
	code, _, _ := runApp(t, dir, "run")
	if code != exitSuccess {
		t.Fatalf("exit=%d want %d (no analyzers wired → zero diags)", code, exitSuccess)
	}
}

// runAppCapturingRealStderr is like runApp but also captures bytes
// written to the process's real os.Stderr (where upstream third-party
// debug-trace lines land). The honnef.co/go/tools `unused` analyzer
// writes "new node, remapping ..." and "deduplicating ..." directly to
// os.Stderr; those bypass the app's stderr buffer and require this
// pipe-based capture.
func runAppCapturingRealStderr(t *testing.T, workdir string, args ...string) (code int, stdout, appStderr, realStderr string) {
	t.Helper()
	orig := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = pw
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 8192)
		for {
			n, rerr := pr.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- sb.String()
	}()

	code, stdout, appStderr = runApp(t, workdir, args...)
	_ = pw.Close()
	realStderr = <-done
	_ = pr.Close()
	return code, stdout, appStderr, realStderr
}

// forceColdLocalAnalyzerCaches isolates tests that assert on honnef's
// real-stderr debug traces. Those traces are emitted only when the unused
// analyzer actually runs. A shared gocacheprog/L0 hit is valid production
// behavior, but it would make --quiet=false look like it suppressed stderr
// and would let the suppression tests pass without exercising the filter.
func forceColdLocalAnalyzerCaches(t *testing.T) {
	t.Helper()
	t.Setenv("GOCACHEPROG", "")
	t.Setenv("GOCACHE", filepath.Join(t.TempDir(), "go-build-cache"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("PLAID_DISABLE_AUTO_CACHE_BACKEND", "1")
	t.Setenv("PLAID_CACHE_BACKEND", "local")
	t.Setenv("PLAID_L0_CACHE_BACKEND", "local")
	t.Setenv("PLAID_L1_CACHE_BACKEND", "local")
	t.Setenv("PLAID_L2_CACHE_BACKEND", "local")
}

// TestRun_Quiet_SuppressesDebugTraces is the headline spec test: the
// honnef.co/go/tools unused pass emits "new node, remapping X -> Y"
// and "deduplicating X -> Y based on path Z" / "... based on position
// Z" to the real os.Stderr (unconditionally — no debug flag gates it).
// --quiet installs an os.Stderr filter that drops those prefixes.
func TestRun_Quiet_SuppressesDebugTraces(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - unused
`)
	src := `package main

func unusedHelper() int { return 42 }

func main() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	forceColdLocalAnalyzerCaches(t)
	_, _, _, realStderr := runAppCapturingRealStderr(t, dir, "run", "--quiet")
	if strings.Contains(realStderr, "new node, remapping") {
		t.Errorf("--quiet did not suppress 'new node, remapping'; stderr=%q", realStderr)
	}
	if strings.Contains(realStderr, "deduplicating") {
		t.Errorf("--quiet did not suppress 'deduplicating'; stderr=%q", realStderr)
	}
}

// TestRun_Quiet_DiagnosticsStillPrinted asserts the --quiet filter
// affects only the noise prefixes, not real diagnostics — those go to
// stdout via output.NewPrinter and never touch the filter at all.
func TestRun_Quiet_DiagnosticsStillPrinted(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	src := `package main

func main() {
	x := 1
	x = 2
	_ = x
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	code, stdout, _, _ := runAppCapturingRealStderr(t, dir, "run", "--quiet")
	if code != exitIssuesFound {
		t.Fatalf("exit=%d want %d stdout=%q", code, exitIssuesFound, stdout)
	}
	if !strings.Contains(stdout, "ineffassign") && !strings.Contains(stdout, "ineffectual") {
		t.Errorf("expected ineffassign diagnostic in stdout under --quiet, got: %q", stdout)
	}
}

// TestRun_Default_SuppressesDebugTraces locks in the default behavior:
// with no flags and no LOG_LEVEL, the filter is on and honnef's upstream
// trace lines are dropped. Guards against an accidental "filter
// off-by-default" regression.
func TestRun_Default_SuppressesDebugTraces(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - unused
`)
	src := `package main

func unusedHelper() int { return 42 }

func main() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	forceColdLocalAnalyzerCaches(t)
	t.Setenv("LOG_LEVEL", "")
	_, _, _, realStderr := runAppCapturingRealStderr(t, dir, "run")
	if strings.Contains(realStderr, "new node, remapping") {
		t.Errorf("default run did not suppress 'new node, remapping' (regression: filter off-by-default?); stderr=%q", realStderr)
	}
	if strings.Contains(realStderr, "deduplicating") {
		t.Errorf("default run did not suppress 'deduplicating'; stderr=%q", realStderr)
	}
}

// TestRun_QuietFalse_EmitsDebugTraces pins the escape hatch: passing
// --quiet=false (with no LOG_LEVEL=warn env) skips the filter so users
// who actually need to see honnef's trace output can get it back.
func TestRun_QuietFalse_EmitsDebugTraces(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - unused
`)
	src := `package main

func unusedHelper() int { return 42 }

func main() {}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	forceColdLocalAnalyzerCaches(t)
	t.Setenv("LOG_LEVEL", "")
	_, _, _, realStderr := runAppCapturingRealStderr(t, dir, "run", "--quiet=false")
	if !strings.Contains(realStderr, "new node, remapping") {
		t.Errorf("--quiet=false unexpectedly suppressed 'new node, remapping' (regression: --quiet=false didn't disable the filter?); stderr=%q", realStderr)
	}
}
