// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// writeTrapFiles seeds a workspace with the four trap-file shapes
// that surfaced Blocker 1 against c1: a `.git/logs/refs/...` reflog
// whose name ends in `.go` (zero-byte, parser-tripping); a
// `vendor/...` Go file with broken syntax; a `testdata/...` Go file
// with broken syntax; and a `.idea/...` IDE-state Go file. Used by
// every package-main subprocess wrap runner's "ignores trap files"
// test.
//
// The trap files only matter when the upstream binary resolves the
// workspace via filepath-walk. Runners that already pass `./...` to
// go/packages won't see them; runners that enumerate via
// [enumerateGoFiles] won't see them either. Either way, the test
// asserts the run does not error and still produces findings from
// the real source files in the workspace.
func writeTrapFiles(t *testing.T, root string) {
	t.Helper()
	traps := map[string]string{
		filepath.Join(".git", "logs", "refs", "remotes", "origin", "feature-branch-with-dot.go", "HEAD"): "",
		filepath.Join(".git", "logs", "refs", "remotes", "origin", "pkg", "trap.go"):                    "",
		filepath.Join("vendor", "some", "pkg", "foo.go"):                                                "this is not valid go and would trip the parser\n",
		filepath.Join("testdata", "junk.go"):                                                            "package // missing name\n",
		filepath.Join(".idea", "x.go"):                                                                  "garbage\n",
		filepath.Join(".vscode", "y.go"):                                                                "garbage\n",
		filepath.Join("node_modules", "thirdparty", "z.go"):                                             "garbage\n",
	}
	for rel, content := range traps {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func TestEnumerateGoFiles_SkipsTrapDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "also.go"), []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTrapFiles(t, dir)
	got, err := enumerateGoFiles(dir)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	sort.Strings(got)
	want := []string{"good.go", filepath.Join("sub", "also.go")}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestEnumerateGoDirs_SkipsTrapDirs(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{"a/x.go", "b/y.go", "a/inner/z.go"} {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTrapFiles(t, dir)
	got, err := enumerateGoDirs(dir)
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	sort.Strings(got)
	want := []string{"a", filepath.Join("a", "inner"), "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

func TestChunkArgv_Empty(t *testing.T) {
	if got := chunkArgv(nil); got != nil {
		t.Errorf("nil input: got %v want nil", got)
	}
	if got := chunkArgv([]string{}); got != nil {
		t.Errorf("empty input: got %v want nil", got)
	}
}

func TestChunkArgv_UnderBudget(t *testing.T) {
	args := []string{"a.go", "b.go", "c.go"}
	chunks := chunkArgv(args)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks want 1", len(chunks))
	}
	if len(chunks[0]) != 3 {
		t.Errorf("chunk[0] len=%d want 3", len(chunks[0]))
	}
}

func TestChunkArgv_SplitsOverBudget(t *testing.T) {
	// Each arg is 100 bytes; budget is ~96 KiB so 2048 args overflow.
	long := strings.Repeat("x", 99) // 99 chars + NUL = 100 cost
	args := make([]string, 2048)
	for i := range args {
		args[i] = long
	}
	chunks := chunkArgv(args)
	if len(chunks) < 2 {
		t.Fatalf("expected >=2 chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		total := 0
		for _, a := range c {
			total += len(a) + 1
		}
		if total > argvChunkBudgetBytes {
			t.Errorf("chunk %d size %d > budget %d", i, total, argvChunkBudgetBytes)
		}
	}
	count := 0
	for _, c := range chunks {
		count += len(c)
	}
	if count != len(args) {
		t.Errorf("total arg count=%d want %d", count, len(args))
	}
}

func TestChunkArgv_OversizedSingleArg(t *testing.T) {
	// A single arg larger than the budget still gets its own chunk —
	// we can't help a path that's bigger than MAX_ARG_STRLEN by itself,
	// but the chunker mustn't drop it.
	big := strings.Repeat("y", argvChunkBudgetBytes*2)
	args := []string{"small.go", big, "small2.go"}
	chunks := chunkArgv(args)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks want 3", len(chunks))
	}
	if chunks[0][0] != "small.go" || chunks[1][0] != big || chunks[2][0] != "small2.go" {
		t.Errorf("ordering broken: %v", chunks)
	}
}

// fakeEchoBinary writes a /bin/sh script that emits its argv (one
// per line) to stdout and exits 0. Used to exercise chunkedInvoke
// without depending on any upstream linter binary.
func fakeEchoBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fakeEchoBinary uses /bin/sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "echo-argv.sh")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake echo: %v", err)
	}
	return bin
}

func TestChunkedInvoke_SinglePass(t *testing.T) {
	bin := fakeEchoBinary(t)
	files := []string{"a.go", "b.go", "c.go"}
	res := chunkedInvoke(context.Background(), bin, "", nil, []string{"--flag"}, files)
	if res.err != nil {
		t.Fatalf("err=%v", res.err)
	}
	lines := strings.Split(strings.TrimRight(string(res.stdout), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines want 4: %q", len(lines), res.stdout)
	}
}

func TestChunkedInvoke_MultiChunkPreservesOrder(t *testing.T) {
	bin := fakeEchoBinary(t)
	const n = 5000
	args := make([]string, n)
	for i := range args {
		// 40-byte path to force multiple chunks (~200 KiB total at n=5000).
		args[i] = fmt.Sprintf("pkg/very/long/path/file_%05d.go", i)
	}
	res := chunkedInvoke(context.Background(), bin, "", nil, nil, args)
	if res.err != nil {
		t.Fatalf("err=%v", res.err)
	}
	lines := strings.Split(strings.TrimRight(string(res.stdout), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("got %d lines want %d (chunks lost args?)", len(lines), n)
	}
	for i, l := range lines {
		if l != args[i] {
			t.Fatalf("line %d=%q want %q (chunks reordered output)", i, l, args[i])
		}
	}
}

func TestChunkedInvoke_DoesNotHitE2BIG(t *testing.T) {
	// note: synthetic; real c1 reproduces at ~10K files. This
	// fixture's 20K args at ~30 bytes each = ~600 KiB total argv —
	// past the ARG_MAX threshold on small-EnvVars hosts (CI). Without
	// chunkedInvoke, exec returns E2BIG; with it, each invocation
	// stays well under 96 KiB.
	bin := fakeEchoBinary(t)
	const n = 20000
	args := make([]string, n)
	for i := range args {
		args[i] = fmt.Sprintf("a/b/c/d/e/f/g/file_%07d.go", i)
	}
	res := chunkedInvoke(context.Background(), bin, "", nil, nil, args)
	if res.err != nil {
		t.Fatalf("chunkedInvoke errored on c1-scale input (E2BIG?): %v", res.err)
	}
	lines := strings.Split(strings.TrimRight(string(res.stdout), "\n"), "\n")
	if len(lines) != n {
		t.Errorf("got %d lines want %d", len(lines), n)
	}
}

func TestChunkedInvoke_PropagatesNonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "exit1.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := chunkedInvoke(context.Background(), bin, "", nil, nil, []string{"x"})
	if res.exitCode != 1 {
		t.Errorf("exitCode=%d want 1", res.exitCode)
	}
	if res.err == nil {
		t.Errorf("err nil, want invoke error")
	}
}

// seedLargeWorkspace adds n synthetic `.go` files under root, each
// at a deep path so the joined argv would exceed ARG_MAX without
// chunking / stdin. note: synthetic; real c1 reproduces at ~10K
// files. The default n=1500 at ~120 byte paths totals ~180 KiB —
// past the 96 KiB chunk budget so the chunked runners must split.
func seedLargeWorkspace(t *testing.T, root string, n int, contents string) {
	t.Helper()
	for i := 0; i < n; i++ {
		rel := filepath.Join(
			"deep", "nested", "directory", "with", "long", "path", "prefix",
			"pkgname", "subpkg",
			fmt.Sprintf("file_%s_%06d.go", strings.Repeat("x", 20), i),
		)
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInvokeWithStdinFiles_ReadsStdin(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("cat not on PATH")
	}
	files := []string{"foo.go", "bar.go", "baz.go"}
	stdout, _, _, err := invokeWithStdinFiles(context.Background(), "/bin/cat", "", nil, nil, files)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	want := "foo.go\nbar.go\nbaz.go\n"
	if string(stdout) != want {
		t.Errorf("stdout=%q want %q", stdout, want)
	}
}
