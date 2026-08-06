// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unitcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/unit"
)

// fixture is a synthetic unit action: real files on disk in the shape
// unit.json declares, with no Go toolchain involved. The key builder
// only ever reads bytes, so a fixture that would not type-check is
// exactly as good a subject as a real package — and far cheaper.
type fixture struct {
	dir     string
	cfgPath string
	cfg     *unit.Config
}

const testToolID = "test-tool v1 sha256:0000"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()

	write := func(rel, body string) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o666); err != nil {
			t.Fatal(err)
		}
		return p
	}

	src := write("pkg/a.go", "package a\n\nfunc A() {}\n")
	ignored := write("pkg/a_windows.go", "//go:build windows\n\npackage a\n")
	exportData := write("dep/dep.a", "export data for dep\n")
	depFacts := write("dep/dep.plaidfacts", "PLF\x01dep facts\n")
	golangci := write(".golangci.yml", "version: \"2\"\n")
	goMod := write("go.mod", "module example.com/fix\n\ngo 1.26\n")
	importcfg := write("pkg/importcfg", "packagefile example.com/fix/dep="+exportData+"\n")
	write("stdlib/linux_arm64/fmt.a", "compiled fmt\n")
	write("stdlib/linux_arm64/os/os.a", "compiled os\n")

	cfg := &unit.Config{
		Schema: unit.SchemaVersion,
		Package: unit.PackageConfig{
			Path:         "example.com/fix/pkg",
			GoFiles:      []string{src},
			IgnoredFiles: []string{ignored},
			GOOS:         "linux",
			GOARCH:       "arm64",
		},
		Deps: unit.DepsConfig{
			Importcfg: importcfg,
			Facts:     map[string]string{"example.com/fix/dep": depFacts},
			StdlibDir: filepath.Join(dir, "stdlib"),
		},
		Module:   unit.ModuleConfig{GoMod: goMod, Path: "example.com/fix"},
		Analysis: unit.AnalysisConfig{Config: golangci},
		Out: unit.OutConfig{
			Sarif: filepath.Join(dir, "out", "out.sarif"),
			Facts: filepath.Join(dir, "out", "out.plaidfacts"),
		},
	}
	f := &fixture{dir: dir, cfgPath: filepath.Join(dir, "unit.json"), cfg: cfg}
	f.writeCfg(t)
	return f
}

// writeCfg re-serializes the fixture's unit.json after a mutation.
func (f *fixture) writeCfg(t *testing.T) {
	t.Helper()
	body, err := json.MarshalIndent(f.cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.cfgPath, body, 0o666); err != nil {
		t.Fatal(err)
	}
}

// key computes the fixture's key, failing the test on any error.
func (f *fixture) key(t *testing.T) Key {
	t.Helper()
	k, err := ComputeKey(f.cfgPath, f.cfg, testToolID)
	if err != nil {
		t.Fatalf("ComputeKey: %v", err)
	}
	return k
}

// appendTo appends a byte to a file, the cheapest possible content
// perturbation.
func appendTo(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
}

// TestComputeKey_Deterministic: the same declared inputs produce the
// same key, twice in a row and from a re-parsed config. Without this
// nothing else in this file means anything — a key that varied per
// call would trivially "never serve a stale result" by never serving
// anything.
func TestComputeKey_Deterministic(t *testing.T) {
	f := newFixture(t)
	first := f.key(t)
	if second := f.key(t); first != second {
		t.Fatalf("key is not stable across calls: %s vs %s", first, second)
	}

	reparsed, err := unit.LoadConfig(f.cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	third, err := ComputeKey(f.cfgPath, reparsed, testToolID)
	if err != nil {
		t.Fatalf("ComputeKey: %v", err)
	}
	if third != first {
		t.Errorf("key differs after a config round-trip: %s vs %s", first, third)
	}
}

// TestComputeKey_EveryDeclaredInputClassChangesTheKey is the
// correctness bar: perturb ONE input class and the key must move.
// A class missing from this table is a class whose change could serve
// a stale result — the failure mode that would make the cache worse
// than no cache.
func TestComputeKey_EveryDeclaredInputClassChangesTheKey(t *testing.T) {
	cases := []struct {
		name    string
		perturb func(t *testing.T, f *fixture)
	}{
		{"source byte", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Package.GoFiles[0])
		}},
		{"ignored source byte", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Package.IgnoredFiles[0])
		}},
		{"dependency export data", func(t *testing.T, f *fixture) {
			appendTo(t, filepath.Join(f.dir, "dep", "dep.a"))
		}},
		{"importcfg text", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Deps.Importcfg)
		}},
		{"dependency facts", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Deps.Facts["example.com/fix/dep"])
		}},
		{"golangci config", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Analysis.Config)
		}},
		{"go.mod", func(t *testing.T, f *fixture) {
			appendTo(t, f.cfg.Module.GoMod)
		}},
		{"stdlib tree content", func(t *testing.T, f *fixture) {
			appendTo(t, filepath.Join(f.dir, "stdlib", "linux_arm64", "os", "os.a"))
		}},
		{"stdlib tree membership", func(t *testing.T, f *fixture) {
			p := filepath.Join(f.dir, "stdlib", "linux_arm64", "net.a")
			if err := os.WriteFile(p, []byte("compiled net\n"), 0o666); err != nil {
				t.Fatal(err)
			}
		}},
		{"unit.json field (goarch)", func(t *testing.T, f *fixture) {
			f.cfg.Package.GOARCH = "amd64"
			f.writeCfg(t)
		}},
		{"unit.json field (enable_only)", func(t *testing.T, f *fixture) {
			f.cfg.Analysis.EnableOnly = []string{"errcheck"}
			f.writeCfg(t)
		}},
		{"declared source path", func(t *testing.T, f *fixture) {
			moved := filepath.Join(f.dir, "pkg", "renamed.go")
			if err := os.Rename(f.cfg.Package.GoFiles[0], moved); err != nil {
				t.Fatal(err)
			}
			f.cfg.Package.GoFiles[0] = moved
			f.writeCfg(t)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			before := f.key(t)
			tc.perturb(t, f)
			after := f.key(t)
			if before == after {
				t.Errorf("key unchanged after perturbing %s (%s); a stale entry would be served", tc.name, before)
			}
		})
	}
}

// TestComputeKey_ToolIdentityChangesTheKey: the analyzing binary is
// part of the key. Two builds that disagree about what `unused`
// reports must not share entries, and until a release stamps them both
// report the same version string — which is why the caller derives the
// identity from the executable's own bytes.
func TestComputeKey_ToolIdentityChangesTheKey(t *testing.T) {
	f := newFixture(t)
	before := f.key(t)
	after, err := ComputeKey(f.cfgPath, f.cfg, testToolID+" (rebuilt)")
	if err != nil {
		t.Fatalf("ComputeKey: %v", err)
	}
	if before == after {
		t.Errorf("key unchanged across tool identities (%s)", before)
	}
}

// TestComputeKey_RejectsEmptyToolIdentity: an entry that cannot be
// attributed to a specific binary must never be written or read.
func TestComputeKey_RejectsEmptyToolIdentity(t *testing.T) {
	f := newFixture(t)
	if _, err := ComputeKey(f.cfgPath, f.cfg, ""); err == nil {
		t.Fatal("ComputeKey accepted an empty tool identity")
	}
}

// TestComputeKey_MissingDeclaredInputIsAnError: a declared input the
// key builder cannot read means the key cannot be proven to cover the
// action. The caller turns that into an uncached run, never into an
// entry keyed on what it managed to read.
func TestComputeKey_MissingDeclaredInputIsAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		rm   func(f *fixture) string
	}{
		{"source", func(f *fixture) string { return f.cfg.Package.GoFiles[0] }},
		{"export data", func(f *fixture) string { return filepath.Join(f.dir, "dep", "dep.a") }},
		{"dep facts", func(f *fixture) string { return f.cfg.Deps.Facts["example.com/fix/dep"] }},
		{"golangci config", func(f *fixture) string { return f.cfg.Analysis.Config }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.Remove(tc.rm(f)); err != nil {
				t.Fatal(err)
			}
			if _, err := ComputeKey(f.cfgPath, f.cfg, testToolID); err == nil {
				t.Errorf("ComputeKey succeeded with the %s input removed", tc.name)
			}
		})
	}
}

// TestComputeKey_IgnoresAmbientEnvironment is the hermeticity property
// stated negatively: the variables that steer every OTHER cache in
// this repo (and the ones a build system's environment differs on)
// must not reach this key. If one ever does, an action's result starts
// depending on host state the build system cannot see — the exact
// property `plaid-lint unit` exists to provide.
func TestComputeKey_IgnoresAmbientEnvironment(t *testing.T) {
	f := newFixture(t)
	before := f.key(t)

	for _, kv := range [][2]string{
		{"GOCACHEPROG", "/nonexistent/cacheprog --flag"},
		{"XDG_CACHE_HOME", "/nonexistent/xdg"},
		{"HOME", "/nonexistent/home"},
		{"GOCACHE", "/nonexistent/gocache"},
		{"GOROOT", "/nonexistent/goroot"},
		{"GOFLAGS", "-mod=vendor"},
		{"PLAID_CACHE_BACKEND", "gocacheprog"},
		{"PLAID_L1_CACHE_BACKEND", "gocacheprog"},
		{"PLAID_CACHE_DIR", "/nonexistent/plaid"},
		{"PLAID_METRICS_TRACE", "1"},
	} {
		t.Setenv(kv[0], kv[1])
	}

	if after := f.key(t); after != before {
		t.Errorf("key changed with the ambient environment: %s vs %s", before, after)
	}
}

// TestComputeKey_WorkingDirectory pins the one piece of process state
// that CAN reach a result: the exclusion filter anchors path-relative
// rules at the working directory, but applies it only to absolute
// diagnostic paths. So a config declaring absolute paths must key on
// the working directory, and one declaring relative paths — the shape
// a build system emits, and the shape that makes entries portable —
// must not.
func TestComputeKey_WorkingDirectory(t *testing.T) {
	chdir := func(t *testing.T, dir string) {
		t.Helper()
		old, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chdir(old) })
	}

	t.Run("absolute paths key on it", func(t *testing.T) {
		f := newFixture(t)
		chdir(t, f.dir)
		before := f.key(t)
		sub := filepath.Join(f.dir, "pkg")
		chdir(t, sub)
		if after := f.key(t); after == before {
			t.Errorf("key unchanged across working directories with absolute declared paths (%s)", before)
		}
	})

	t.Run("relative paths do not", func(t *testing.T) {
		f := newFixture(t)
		// Respell every declared path relative to the fixture root,
		// then compute from two different working directories. The
		// files themselves are read relative to the cwd, so both runs
		// must run from a directory where they resolve: the same one.
		// What differs is only what os.Getwd would report, which is
		// simulated by computing the key from a nested directory with
		// a symlinked view of the tree.
		rel := func(p string) string {
			r, err := filepath.Rel(f.dir, p)
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		f.cfg.Package.GoFiles = []string{rel(f.cfg.Package.GoFiles[0])}
		f.cfg.Package.IgnoredFiles = []string{rel(f.cfg.Package.IgnoredFiles[0])}
		f.cfg.Deps.Importcfg = rel(f.cfg.Deps.Importcfg)
		f.cfg.Deps.Facts = map[string]string{"example.com/fix/dep": rel(f.cfg.Deps.Facts["example.com/fix/dep"])}
		f.cfg.Deps.StdlibDir = rel(f.cfg.Deps.StdlibDir)
		f.cfg.Analysis.Config = rel(f.cfg.Analysis.Config)
		f.cfg.Module.GoMod = rel(f.cfg.Module.GoMod)
		f.cfg.Out.Sarif = "out/out.sarif"
		f.cfg.Out.Facts = "out/out.plaidfacts"
		// The importcfg names its export data absolutely; respell it
		// too, or the tree is not actually path-portable.
		if err := os.WriteFile(filepath.Join(f.dir, f.cfg.Deps.Importcfg),
			[]byte("packagefile example.com/fix/dep=dep/dep.a\n"), 0o666); err != nil {
			t.Fatal(err)
		}
		f.writeCfg(t)

		chdir(t, f.dir)
		before := f.key(t)

		// A second checkout of the same tree at a different absolute
		// path must produce the same key — that is what makes an entry
		// shareable between workspaces.
		clone := t.TempDir()
		copyTree(t, f.dir, clone)
		chdir(t, clone)
		after, err := ComputeKey("unit.json", f.cfg, testToolID)
		if err != nil {
			t.Fatalf("ComputeKey: %v", err)
		}
		if after != before {
			t.Errorf("key differs between two checkouts of identical relative inputs: %s vs %s", before, after)
		}
	})
}

// copyTree copies a directory tree, used to prove path portability.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o777)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o666)
	})
	if err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}

// TestComputeKey_StdlibSymlinkRetarget: a declared directory is
// commonly a tree of links into a build system's output base, so a
// retargeted link must move the key even when both targets happen to
// hold the same bytes today.
func TestComputeKey_StdlibSymlinkRetarget(t *testing.T) {
	f := newFixture(t)
	real1 := filepath.Join(f.dir, "real1.a")
	real2 := filepath.Join(f.dir, "real2.a")
	for _, p := range []string{real1, real2} {
		if err := os.WriteFile(p, []byte("same bytes\n"), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(f.dir, "stdlib", "linux_arm64", "linked.a")
	if err := os.Symlink(real1, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before := f.key(t)

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real2, link); err != nil {
		t.Fatal(err)
	}
	if after := f.key(t); after == before {
		t.Errorf("key unchanged after retargeting a declared symlink (%s)", before)
	}
}
