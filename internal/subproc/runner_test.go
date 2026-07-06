// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// fakelinterPath compiles the testdata/fakelinter binary on first
// use and returns its absolute path. The compiled binary is cached
// across subtests via a sync.Once and removed at process exit via
// t.Cleanup on the first caller.
var (
	fakelinterOnce sync.Once
	fakelinterAddr string
	fakelinterErr  error
)

func fakelinter(t *testing.T) string {
	t.Helper()
	fakelinterOnce.Do(func() {
		dir, err := os.MkdirTemp("", "fakelinter-bin-*")
		if err != nil {
			fakelinterErr = err
			return
		}
		bin := filepath.Join(dir, "fakelinter")
		src := filepath.Join("testdata", "fakelinter", "main.go")
		cmd := exec.Command("go", "build", "-o", bin, src)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fakelinterErr = err
			return
		}
		fakelinterAddr = bin
	})
	if fakelinterErr != nil {
		t.Fatalf("build fakelinter: %v", fakelinterErr)
	}
	return fakelinterAddr
}

// freshWorkspace lays a minimal Go module under a temp dir and
// returns a WorkspaceRef pointing at it. The returned dir is owned
// by t and cleaned up automatically.
func freshWorkspace(t *testing.T) subproc.WorkspaceRef {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	pkgDir := filepath.Join(dir, "pkg", "foo")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir pkg/foo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "foo.go"), []byte("package foo\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("write foo.go: %v", err)
	}
	return subproc.WorkspaceRef{ModuleRoot: dir}
}

// fakeRunner is an in-test Runner that wires Invoke + Cache +
// Canonicalize together exactly the way a real T3.2/T3.3 wrapper
// will. It exists so the framework can be tested end-to-end without
// any specific linter being wired.
type fakeRunner struct {
	name     string
	bin      string
	cache    subproc.Cache
	version  string
	settings string
	skip     bool

	// spawnCount lets the cache-hit test assert that a second Run
	// does not invoke the subprocess.
	spawnCount atomic.Int64

	// extraEnv is appended to the inherited env for the spawned
	// child (used to flip FAKELINTER_FAIL).
	extraEnv []string

	// timeout, if non-zero, is applied to the Invoke context.
	timeout time.Duration

	// stdin, if non-nil, is fed to the subprocess.
	stdin string
}

func (r *fakeRunner) Name() string { return r.name }

func (r *fakeRunner) Run(ctx context.Context, cfg *config.Config, ws subproc.WorkspaceRef) ([]output.Diagnostic, error) {
	if r.skip {
		return subproc.SkipResult()
	}

	key, err := subproc.CacheKey(r.name, r.version, r.settings, ws)
	if err != nil {
		return nil, err
	}
	if r.cache != nil {
		if diags, ok, lookupErr := r.cache.Lookup(key); lookupErr == nil && ok {
			return diags, nil
		}
	}

	r.spawnCount.Add(1)

	runCtx := ctx
	if r.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}

	args := []string{"--module-root=" + ws.ModuleRoot}
	// Propagate env via the os/exec interface; tests poke r.extraEnv.
	prevEnv := os.Environ()
	for _, kv := range r.extraEnv {
		key, val, _ := strings.Cut(kv, "=")
		_ = os.Setenv(key, val)
	}
	defer func() {
		for _, kv := range r.extraEnv {
			key, _, _ := strings.Cut(kv, "=")
			_ = os.Unsetenv(key)
		}
		// Restore unrelated mutations no-op; tests don't touch
		// the same vars they read.
		_ = prevEnv
	}()

	var stdin *strings.Reader
	if r.stdin != "" {
		stdin = strings.NewReader(r.stdin)
	}
	stdout, _, _, invokeErr := subproc.Invoke(runCtx, r.bin, args, readerOrNil(stdin))
	if invokeErr != nil {
		return nil, invokeErr
	}

	if strings.HasPrefix(string(stdout), "stdin:") {
		// Non-JSON stdout path used only by the stdin-echo fail
		// mode. Fabricate a single diagnostic so the caller can
		// assert end-to-end.
		diag := output.Diagnostic{
			Linter:   r.name,
			Message:  string(stdout),
			Severity: output.SeverityInfo,
		}
		out := []output.Diagnostic{diag}
		if r.cache != nil {
			_ = r.cache.Store(key, out)
		}
		return out, nil
	}

	var raw []struct {
		File     string `json:"file"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, &subproc.ParseError{Name: r.bin, Detail: "decode fakelinter stdout", Err: err}
	}
	subprocs := make([]subproc.SubprocDiagnostic, 0, len(raw))
	for _, r := range raw {
		subprocs = append(subprocs, subproc.SubprocDiagnostic{
			Message:  r.Message,
			Severity: r.Severity,
			File:     r.File,
			Line:     r.Line,
			Column:   r.Column,
		})
	}
	diags := subproc.Canonicalize(r.name, ws, subprocs)
	if r.cache != nil {
		if err := r.cache.Store(key, diags); err != nil {
			return nil, err
		}
	}
	return diags, nil
}

// readerOrNil avoids passing a typed-nil *strings.Reader through the
// io.Reader interface (which would defeat the nil check in Invoke).
func readerOrNil(r *strings.Reader) interface {
	Read(p []byte) (int, error)
} {
	if r == nil {
		return nil
	}
	return r
}

func TestRunner_HappyPath(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	cacheRoot := t.TempDir()
	cache, err := subproc.OpenCache(cacheRoot)
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := &fakeRunner{name: "fakelinter", bin: bin, cache: cache, version: "v1", settings: "s1"}

	diags, err := r.Run(t.Context(), nil, ws)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("want 1 diag, got %d", len(diags))
	}
	d := diags[0]
	if d.Linter != "fakelinter" {
		t.Errorf("Linter=%q want %q", d.Linter, "fakelinter")
	}
	if d.Severity != output.SeverityError {
		t.Errorf("Severity=%q want %q", d.Severity, output.SeverityError)
	}
	if !filepath.IsAbs(d.Pos.Filename) {
		t.Errorf("Pos.Filename=%q is not absolute", d.Pos.Filename)
	}
	if !strings.HasPrefix(d.Pos.Filename, ws.ModuleRoot) {
		t.Errorf("Pos.Filename=%q not anchored at ModuleRoot=%q", d.Pos.Filename, ws.ModuleRoot)
	}
	if d.Pos.Line != 10 {
		t.Errorf("Pos.Line=%d want 10", d.Pos.Line)
	}
	if r.spawnCount.Load() != 1 {
		t.Errorf("spawnCount=%d want 1", r.spawnCount.Load())
	}
}

func TestRunner_CacheHit(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	cache, err := subproc.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := &fakeRunner{name: "fakelinter", bin: bin, cache: cache, version: "v1", settings: "s1"}

	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := r.spawnCount.Load(); got != 1 {
		t.Errorf("spawnCount=%d want 1 (second Run should hit cache)", got)
	}
}

func TestRunner_CacheInvalidatedByEdit(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	cache, err := subproc.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := &fakeRunner{name: "fakelinter", bin: bin, cache: cache, version: "v1", settings: "s1"}

	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Mutate a source file under the workspace.
	if err := os.WriteFile(filepath.Join(ws.ModuleRoot, "pkg", "foo", "foo.go"), []byte("package foo\n\nfunc Foo() { _ = 1 }\n"), 0o644); err != nil {
		t.Fatalf("mutate source: %v", err)
	}

	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := r.spawnCount.Load(); got != 2 {
		t.Errorf("spawnCount=%d want 2 (source edit should invalidate cache)", got)
	}
}

func TestRunner_CacheInvalidatedByVersion(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	cache, err := subproc.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := &fakeRunner{name: "fakelinter", bin: bin, cache: cache, version: "v1", settings: "s1"}

	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	r.version = "v2"
	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := r.spawnCount.Load(); got != 2 {
		t.Errorf("spawnCount=%d want 2 (version change should invalidate)", got)
	}
}

func TestRunner_CacheInvalidatedBySettings(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	cache, err := subproc.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	r := &fakeRunner{name: "fakelinter", bin: bin, cache: cache, version: "v1", settings: "s1"}

	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	r.settings = "s2"
	if _, err := r.Run(t.Context(), nil, ws); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := r.spawnCount.Load(); got != 2 {
		t.Errorf("spawnCount=%d want 2 (settings change should invalidate)", got)
	}
}

func TestRunner_OptOut(t *testing.T) {
	r := &fakeRunner{name: "fakelinter", skip: true}
	diags, err := r.Run(t.Context(), nil, subproc.WorkspaceRef{ModuleRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if diags != nil {
		t.Errorf("diags=%v want nil", diags)
	}
	if r.spawnCount.Load() != 0 {
		t.Errorf("spawnCount=%d want 0 (opt-out must not spawn)", r.spawnCount.Load())
	}
}

func TestRunner_TimeoutTrips(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	r := &fakeRunner{
		name:     "fakelinter",
		bin:      bin,
		version:  "v1",
		settings: "s1",
		extraEnv: []string{"FAKELINTER_FAIL=hang"},
		timeout:  150 * time.Millisecond,
	}

	start := time.Now()
	_, err := r.Run(t.Context(), nil, ws)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want timeout error, got nil")
	}
	if !subproc.IsTimeout(err) {
		t.Errorf("err=%v does not satisfy IsTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed=%v — ctx didn't kill the subprocess promptly", elapsed)
	}
	var ie *subproc.InvokeError
	if !errors.As(err, &ie) {
		t.Fatalf("err=%v does not unwrap to *InvokeError", err)
	}
	if ie.ExitCode != -1 {
		t.Errorf("ExitCode=%d want -1 (no clean exit on timeout)", ie.ExitCode)
	}
}

func TestRunner_NonZeroExit(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	r := &fakeRunner{
		name:     "fakelinter",
		bin:      bin,
		version:  "v1",
		settings: "s1",
		extraEnv: []string{"FAKELINTER_FAIL=crash"},
	}
	_, err := r.Run(t.Context(), nil, ws)
	if err == nil {
		t.Fatalf("want non-zero exit error, got nil")
	}
	var ie *subproc.InvokeError
	if !errors.As(err, &ie) {
		t.Fatalf("err=%v does not unwrap to *InvokeError", err)
	}
	if ie.ExitCode != 7 {
		t.Errorf("ExitCode=%d want 7", ie.ExitCode)
	}
	if !strings.Contains(string(ie.Stderr), "boom") {
		t.Errorf("Stderr=%q want to contain 'boom'", string(ie.Stderr))
	}
}

func TestRunner_MalformedOutput(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	r := &fakeRunner{
		name:     "fakelinter",
		bin:      bin,
		version:  "v1",
		settings: "s1",
		extraEnv: []string{"FAKELINTER_FAIL=malformed"},
	}
	_, err := r.Run(t.Context(), nil, ws)
	if err == nil {
		t.Fatalf("want parse error, got nil")
	}
	var pe *subproc.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("err=%v does not unwrap to *ParseError", err)
	}
}

func TestInvoke_Stdin(t *testing.T) {
	bin := fakelinter(t)
	ws := freshWorkspace(t)
	r := &fakeRunner{
		name:     "fakelinter",
		bin:      bin,
		version:  "v1",
		settings: "s1",
		extraEnv: []string{"FAKELINTER_FAIL=stdin"},
		stdin:    "hello world",
	}
	diags, err := r.Run(t.Context(), nil, ws)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("want 1 diag, got %d", len(diags))
	}
	if !strings.HasPrefix(diags[0].Message, "stdin:") {
		t.Errorf("Message=%q want stdin:* prefix", diags[0].Message)
	}
}

func TestCanonicalize_Mapping(t *testing.T) {
	ws := subproc.WorkspaceRef{ModuleRoot: "/abs/root"}
	subprocs := []subproc.SubprocDiagnostic{
		{
			Message:  "trailing newline\n",
			Severity: "WARN",
			File:     "pkg/a/x.go",
			Line:     1,
			Column:   2,
			Related: []subproc.SubprocRelated{
				{Message: "see also", File: "pkg/b/y.go", Line: 9, Column: 0},
			},
		},
		{
			Message:  "absolute path",
			Severity: "fatal",
			File:     "/elsewhere/abs.go",
			Line:     5,
			Column:   0,
		},
		{
			Message:  "unknown severity",
			Severity: "moot",
			File:     "rel.go",
			Line:     2,
		},
	}
	diags := subproc.Canonicalize("linterX", ws, subprocs)
	if len(diags) != 3 {
		t.Fatalf("len(diags)=%d want 3", len(diags))
	}

	// Trim, severity map, attribution, related canonicalized.
	if diags[0].Linter != "linterX" {
		t.Errorf("Linter=%q want linterX", diags[0].Linter)
	}
	if diags[0].Message != "trailing newline" {
		t.Errorf("Message=%q want trimmed", diags[0].Message)
	}
	if diags[0].Severity != output.SeverityWarning {
		t.Errorf("Severity=%q want warning", diags[0].Severity)
	}
	if diags[0].Pos.Filename != filepath.FromSlash("/abs/root/pkg/a/x.go") {
		t.Errorf("Pos.Filename=%q want /abs/root/pkg/a/x.go", diags[0].Pos.Filename)
	}
	if len(diags[0].Related) != 1 {
		t.Fatalf("Related len=%d want 1", len(diags[0].Related))
	}
	if diags[0].Related[0].Position.Filename != filepath.FromSlash("/abs/root/pkg/b/y.go") {
		t.Errorf("Related[0].Position.Filename=%q want /abs/root/pkg/b/y.go", diags[0].Related[0].Position.Filename)
	}

	// Absolute paths preserved.
	if diags[1].Pos.Filename != filepath.FromSlash("/elsewhere/abs.go") {
		t.Errorf("Pos.Filename=%q want /elsewhere/abs.go", diags[1].Pos.Filename)
	}
	if diags[1].Severity != output.SeverityError {
		t.Errorf("Severity=%q want error (fatal mapped)", diags[1].Severity)
	}

	// Unknown severity collapses to error.
	if diags[2].Severity != output.SeverityError {
		t.Errorf("Severity=%q want error (unknown→error)", diags[2].Severity)
	}
}

func TestCacheKey_StableAndSensitive(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	ws := subproc.WorkspaceRef{ModuleRoot: dir, BuildTags: []string{"a", "b"}, Env: []string{"E=1"}}

	k1, err := subproc.CacheKey("L", "v1", "s1", ws)
	if err != nil {
		t.Fatalf("CacheKey: %v", err)
	}
	k2, err := subproc.CacheKey("L", "v1", "s1", ws)
	if err != nil {
		t.Fatalf("CacheKey: %v", err)
	}
	if k1 != k2 {
		t.Errorf("repeat call produced different keys: %s vs %s", k1, k2)
	}

	// Tag order shouldn't matter.
	ws2 := ws
	ws2.BuildTags = []string{"b", "a"}
	k3, err := subproc.CacheKey("L", "v1", "s1", ws2)
	if err != nil {
		t.Fatalf("CacheKey: %v", err)
	}
	if k1 != k3 {
		t.Errorf("tag order altered key: %s vs %s", k1, k3)
	}

	// Different version flips the key.
	k4, err := subproc.CacheKey("L", "v2", "s1", ws)
	if err != nil {
		t.Fatalf("CacheKey: %v", err)
	}
	if k1 == k4 {
		t.Errorf("version change did not alter key")
	}
}

func TestCacheKey_RequiresAbsRoot(t *testing.T) {
	ws := subproc.WorkspaceRef{ModuleRoot: "relative/path"}
	_, err := subproc.CacheKey("L", "v1", "s1", ws)
	if err == nil {
		t.Errorf("want error for relative ModuleRoot, got nil")
	}
}

func TestCache_LookupMissAndStore(t *testing.T) {
	c, err := subproc.OpenCache(t.TempDir())
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	const key = "abcdef0123456789"
	if _, ok, err := c.Lookup(key); err != nil || ok {
		t.Fatalf("Lookup miss expected, got ok=%v err=%v", ok, err)
	}
	diags := []output.Diagnostic{{Linter: "L", Message: "hi"}}
	if err := c.Store(key, diags); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok, err := c.Lookup(key)
	if err != nil || !ok {
		t.Fatalf("Lookup hit expected, got ok=%v err=%v", ok, err)
	}
	if len(got) != 1 || got[0].Message != "hi" {
		t.Errorf("got=%v want [{Message:hi}]", got)
	}
}

// Use config.Config to make sure the runner signature compiles
// against the real type even though the tests don't depend on a
// concrete Config value.
var _ = config.NewDefault
