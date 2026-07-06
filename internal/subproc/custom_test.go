// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// customPluginPath compiles internal/subproc/testdata/customplugin
// on first use and returns its absolute path. The compiled binary
// lives in a per-process temp dir and persists across subtests via
// sync.Once.
var (
	customPluginOnce sync.Once
	customPluginAddr string
	customPluginErr  error
)

func customPlugin(t *testing.T) string {
	t.Helper()
	customPluginOnce.Do(func() {
		dir, err := os.MkdirTemp("", "customplugin-bin-*")
		if err != nil {
			customPluginErr = err
			return
		}
		bin := filepath.Join(dir, "customplugin")
		src := filepath.Join("testdata", "customplugin")
		cmd := exec.Command("go", "build", "-o", bin, "./"+src)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			customPluginErr = err
			return
		}
		customPluginAddr = bin
	})
	if customPluginErr != nil {
		t.Fatalf("build customplugin: %v", customPluginErr)
	}
	return customPluginAddr
}

// freshCustomWorkspace lays down a minimal Go module with one
// package and a go.sum so the linterVersion hash has a real input.
func freshCustomWorkspace(t *testing.T) WorkspaceRef {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), []byte("// stub go.sum\n"), 0o644); err != nil {
		t.Fatalf("write go.sum: %v", err)
	}
	pkgDir := filepath.Join(dir, "pkg", "foo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkg/foo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "foo.go"), []byte("package foo\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("write foo.go: %v", err)
	}
	return WorkspaceRef{ModuleRoot: dir}
}

// extraEnv runs fn with the given KEY=VALUE pairs applied to the
// process environment and restored on return. The synthetic plugin
// inherits the parent env via invokeInDir, so this is the cleanest
// way to flip its behavior flags from a test.
func extraEnv(t *testing.T, kvs []string, fn func()) {
	t.Helper()
	saved := make(map[string]string, len(kvs))
	cleared := make(map[string]bool, len(kvs))
	for _, kv := range kvs {
		k, v, _ := strings.Cut(kv, "=")
		if prev, ok := os.LookupEnv(k); ok {
			saved[k] = prev
		} else {
			cleared[k] = true
		}
		if err := os.Setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
	}
	defer func() {
		for k := range cleared {
			_ = os.Unsetenv(k)
		}
		for k, v := range saved {
			_ = os.Setenv(k, v)
		}
	}()
	fn()
}

func TestCustomRunner_Name(t *testing.T) {
	r := NewCustomRunner("myplugin", config.CustomLinterSettings{Path: "/tmp/x"}, nil)
	if got := r.Name(); got != "myplugin" {
		t.Errorf("Name()=%q want myplugin", got)
	}
}

func TestCustomRunner_HappyPath(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	cache, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := NewCustomRunner("myplugin", config.CustomLinterSettings{Path: bin}, cache)

	var diags []output.Diagnostic
	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=3"}, func() {
		diags, err = r.Run(t.Context(), nil, ws)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(diags) != 3 {
		t.Fatalf("len(diags)=%d want 3", len(diags))
	}

	// Attribution forced to runner name; severity mapped; path
	// re-rooted to absolute.
	for i, d := range diags {
		if d.Linter != "myplugin" {
			t.Errorf("[%d].Linter=%q want myplugin", i, d.Linter)
		}
		if d.Severity != output.SeverityWarning {
			t.Errorf("[%d].Severity=%q want warning", i, d.Severity)
		}
		if !filepath.IsAbs(d.Pos.Filename) {
			t.Errorf("[%d].Pos.Filename=%q not absolute", i, d.Pos.Filename)
		}
		if !strings.HasPrefix(d.Pos.Filename, ws.ModuleRoot) {
			t.Errorf("[%d].Pos.Filename=%q not under ModuleRoot", i, d.Pos.Filename)
		}
		if d.Message == "" {
			t.Errorf("[%d].Message empty", i)
		}
	}
}

func TestCustomRunner_CacheHit(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	cache, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, cache)

	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=2"}, func() {
		if _, err := r.Run(t.Context(), nil, ws); err != nil {
			t.Fatalf("first Run: %v", err)
		}
	})

	// Second run with the plugin set to emit a different count: if
	// the cache is honored, the result should still have 2 diags
	// (cached from the first call), proving the plugin did not spawn.
	var diags []output.Diagnostic
	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=99"}, func() {
		diags, err = r.Run(t.Context(), nil, ws)
	})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(diags) != 2 {
		t.Errorf("len(diags)=%d want 2 (cache hit should not re-spawn)", len(diags))
	}
}

func TestCustomRunner_CacheInvalidatedByEdit(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	cache, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, cache)

	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=2"}, func() {
		if _, err := r.Run(t.Context(), nil, ws); err != nil {
			t.Fatalf("first Run: %v", err)
		}
	})

	// Mutate a source file → WorkspaceContentHash changes → cache miss.
	if err := os.WriteFile(filepath.Join(ws.ModuleRoot, "pkg", "foo", "foo.go"), []byte("package foo\n\nfunc Foo() { _ = 1 }\n"), 0o644); err != nil {
		t.Fatalf("mutate source: %v", err)
	}

	var diags []output.Diagnostic
	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=5"}, func() {
		diags, err = r.Run(t.Context(), nil, ws)
	})
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(diags) != 5 {
		t.Errorf("len(diags)=%d want 5 (source edit should re-spawn)", len(diags))
	}
}

func TestCustomRunner_CacheInvalidatedByBinaryRebuild(t *testing.T) {
	ws := freshCustomWorkspace(t)
	cache, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}

	// First plugin "build".
	bin1 := fakeExecutable(t, "payload-A")
	r1 := NewCustomRunner("p", config.CustomLinterSettings{Path: bin1}, cache)
	v1, err := r1.linterVersion(bin1, ws)
	if err != nil {
		t.Fatalf("linterVersion(bin1): %v", err)
	}

	// Second plugin "build" (different bytes).
	bin2 := fakeExecutable(t, "payload-B")
	r2 := NewCustomRunner("p", config.CustomLinterSettings{Path: bin2}, cache)
	v2, err := r2.linterVersion(bin2, ws)
	if err != nil {
		t.Fatalf("linterVersion(bin2): %v", err)
	}
	if v1 == v2 {
		t.Errorf("expected different versions for different plugin builds: %s == %s", v1, v2)
	}

	// go.sum mutation alone must also flip the version even with the
	// same binary, mirroring T3.2 / T3.3.
	if err := os.WriteFile(filepath.Join(ws.ModuleRoot, "go.sum"), []byte("// different go.sum\n"), 0o644); err != nil {
		t.Fatalf("rewrite go.sum: %v", err)
	}
	v3, err := r1.linterVersion(bin1, ws)
	if err != nil {
		t.Fatalf("linterVersion after go.sum edit: %v", err)
	}
	if v1 == v3 {
		t.Errorf("expected go.sum change to flip version: %s == %s", v1, v3)
	}
}

func TestCustomRunner_CacheInvalidatedBySettings(t *testing.T) {
	r1 := NewCustomRunner("p", config.CustomLinterSettings{
		Path:     "/x",
		Settings: map[string]any{"severity": "info"},
	}, nil)
	r2 := NewCustomRunner("p", config.CustomLinterSettings{
		Path:     "/x",
		Settings: map[string]any{"severity": "warning"},
	}, nil)

	h1, err := r1.settingsHash()
	if err != nil {
		t.Fatalf("settingsHash r1: %v", err)
	}
	h2, err := r2.settingsHash()
	if err != nil {
		t.Fatalf("settingsHash r2: %v", err)
	}
	if h1 == h2 {
		t.Errorf("settings change did not flip hash: %s == %s", h1, h2)
	}

	// And the same settings must produce the same hash.
	h1b, _ := r1.settingsHash()
	if h1 != h1b {
		t.Errorf("settings hash unstable: %s vs %s", h1, h1b)
	}
}

func TestCustomRunner_Timeout(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()

	var err error
	start := time.Now()
	extraEnv(t, []string{"CUSTOMPLUGIN_FAIL=hang"}, func() {
		_, err = r.Run(ctx, nil, ws)
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("want timeout error, got nil")
	}
	if !IsTimeout(err) {
		t.Errorf("err=%v does not satisfy IsTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed=%v — ctx didn't kill subprocess promptly", elapsed)
	}
	var ie *InvokeError
	if !errors.As(err, &ie) {
		t.Fatalf("err=%v does not unwrap to *InvokeError", err)
	}
	if ie.ExitCode != -1 {
		t.Errorf("ExitCode=%d want -1 on timeout", ie.ExitCode)
	}
}

func TestCustomRunner_ExitCodeGreaterThanOneIsError(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	var err error
	extraEnv(t, []string{"CUSTOMPLUGIN_FAIL=exit2", "CUSTOMPLUGIN_DIAGS=1"}, func() {
		_, err = r.Run(t.Context(), nil, ws)
	})
	if err == nil {
		t.Fatalf("want InvokeError for exit 2, got nil")
	}
	var ie *InvokeError
	if !errors.As(err, &ie) {
		t.Fatalf("err=%v does not unwrap to *InvokeError", err)
	}
	if ie.ExitCode != 2 {
		t.Errorf("ExitCode=%d want 2", ie.ExitCode)
	}
}

func TestCustomRunner_MalformedNDJSONIsParseError(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	var err error
	extraEnv(t, []string{"CUSTOMPLUGIN_FAIL=malformed"}, func() {
		_, err = r.Run(t.Context(), nil, ws)
	})
	if err == nil {
		t.Fatalf("want ParseError, got nil")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err=%v does not unwrap to *ParseError", err)
	}
}

func TestCustomRunner_StderrCapturedOnError(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	var err error
	extraEnv(t, []string{"CUSTOMPLUGIN_FAIL=exit2", "CUSTOMPLUGIN_DIAGS=1"}, func() {
		_, err = r.Run(t.Context(), nil, ws)
	})
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	var ie *InvokeError
	if !errors.As(err, &ie) {
		t.Fatalf("err=%v does not unwrap to *InvokeError", err)
	}
	if !strings.Contains(string(ie.Stderr), "customplugin: emitted") {
		t.Errorf("Stderr=%q want to contain plugin debug output", string(ie.Stderr))
	}
}

func TestCustomRunner_RelatedRoundtrips(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	var diags []output.Diagnostic
	var err error
	extraEnv(t, []string{"CUSTOMPLUGIN_DIAGS=1", "CUSTOMPLUGIN_RELATED=true"}, func() {
		diags, err = r.Run(t.Context(), nil, ws)
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len(diags)=%d want 1", len(diags))
	}
	if len(diags[0].Related) != 1 {
		t.Fatalf("Related len=%d want 1", len(diags[0].Related))
	}
	rel := diags[0].Related[0]
	if rel.Message != "see also" {
		t.Errorf("Related[0].Message=%q want \"see also\"", rel.Message)
	}
	if !filepath.IsAbs(rel.Position.Filename) {
		t.Errorf("Related[0].Filename=%q not absolute", rel.Position.Filename)
	}
}

func TestCustomRunner_MissingRequiredField(t *testing.T) {
	bin := customPlugin(t)
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: bin}, nil)

	var err error
	extraEnv(t, []string{"CUSTOMPLUGIN_FAIL=missing-fields"}, func() {
		_, err = r.Run(t.Context(), nil, ws)
	})
	if err == nil {
		t.Fatalf("want ParseError for missing required field, got nil")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err=%v does not unwrap to *ParseError", err)
	}
	if !strings.Contains(pe.Detail, "missing required field") {
		t.Errorf("Detail=%q want to mention missing required field", pe.Detail)
	}
}

func TestCustomRunner_BinaryNotFound(t *testing.T) {
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: "/no/such/customplugin"}, nil)
	_, err := r.Run(t.Context(), nil, ws)
	if err == nil {
		t.Fatalf("want error for missing binary, got nil")
	}
}

func TestCustomRunner_EmptyPath(t *testing.T) {
	ws := freshCustomWorkspace(t)
	r := NewCustomRunner("p", config.CustomLinterSettings{}, nil)
	_, err := r.Run(t.Context(), nil, ws)
	if err == nil {
		t.Fatalf("want error for empty path, got nil")
	}
}

func TestCustomRunner_BuildArgs_FlagOrderDeterministic(t *testing.T) {
	r := NewCustomRunner("p", config.CustomLinterSettings{
		Path: "/x",
		Settings: map[string]any{
			"zeta":  "z-val",
			"alpha": "a-val",
			"mid":   true,
		},
	}, nil)
	got := r.buildArgs()
	// Keys sorted lexicographically, trailing `./...`.
	want := []string{"--alpha=a-val", "--mid=true", "--zeta=z-val", "./..."}
	if len(got) != len(want) {
		t.Fatalf("len(args)=%d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestCustomRunner_BuildArgs_NoSettings(t *testing.T) {
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: "/x"}, nil)
	got := r.buildArgs()
	if len(got) != 1 || got[0] != "./..." {
		t.Errorf("args=%v want [./...]", got)
	}
}

func TestCustomRunner_BuildEnv_BuildTags(t *testing.T) {
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: "/x"}, nil)
	ws := WorkspaceRef{ModuleRoot: "/abs", BuildTags: []string{"b", "a", "b"}, Env: []string{"FOO=1"}}
	env := r.buildEnv(ws)
	// FOO=1 first (from ws.Env), then GOFLAGS with sorted deduped tags.
	if len(env) != 2 {
		t.Fatalf("len(env)=%d want 2 (%v)", len(env), env)
	}
	if env[0] != "FOO=1" {
		t.Errorf("env[0]=%q want FOO=1", env[0])
	}
	if env[1] != "GOFLAGS=-tags=a,b" {
		t.Errorf("env[1]=%q want GOFLAGS=-tags=a,b", env[1])
	}
}

func TestCustomRunner_BuildEnv_NoTags(t *testing.T) {
	r := NewCustomRunner("p", config.CustomLinterSettings{Path: "/x"}, nil)
	env := r.buildEnv(WorkspaceRef{ModuleRoot: "/abs"})
	if len(env) != 0 {
		t.Errorf("env=%v want empty", env)
	}
}

func TestParseCustomNDJSON_HappyPath(t *testing.T) {
	input := []byte(`{"file":"a.go","line":1,"column":2,"severity":"error","message":"m1"}
{"file":"b.go","line":3,"column":4,"severity":"warning","message":"m2","code":"X"}
`)
	diags, err := parseCustomNDJSON("p", input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(diags) != 2 {
		t.Fatalf("len=%d want 2", len(diags))
	}
	if diags[0].File != "a.go" || diags[0].Line != 1 || diags[0].Column != 2 {
		t.Errorf("[0]=%+v", diags[0])
	}
	if diags[1].Severity != "warning" {
		t.Errorf("[1].Severity=%q", diags[1].Severity)
	}
}

func TestParseCustomNDJSON_EmptyAndBlank(t *testing.T) {
	for _, s := range [][]byte{nil, []byte(""), []byte("   \n  \n")} {
		diags, err := parseCustomNDJSON("p", s)
		if err != nil {
			t.Errorf("parse(%q) err=%v", s, err)
		}
		if diags != nil {
			t.Errorf("parse(%q) diags=%v want nil", s, diags)
		}
	}
}

func TestParseCustomNDJSON_ToleratesUnknownFields(t *testing.T) {
	input := []byte(`{"file":"a.go","line":1,"column":2,"severity":"error","message":"m","future_field":42}
`)
	diags, err := parseCustomNDJSON("p", input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("len=%d want 1", len(diags))
	}
}

// fakeExecutable writes a non-executable file with the given contents
// and returns its absolute path. linterVersion only needs to sha256
// the bytes; the file doesn't have to run.
func fakeExecutable(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fake-customplugin-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(contents); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return f.Name()
}
