// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestOpenSinkConfinesOutputPath verifies that openSink refuses output
// destinations that escape the working tree — the path-traversal /
// arbitrary-file-write vector reachable from an untrusted .golangci.yml.
func TestOpenSinkConfinesOutputPath(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()

	chdir(t, work)

	// A sentinel file outside the tree that must never be touched.
	guarded := filepath.Join(outside, "authorized_keys")
	if err := os.WriteFile(guarded, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("relative path within tree is allowed", func(t *testing.T) {
		_, closer, err := openSink(os.Stdout, "reports/out.json", outputPathConfined)
		if err != nil {
			t.Fatalf("unexpected error for in-tree path: %v", err)
		}
		if closer != nil {
			_ = closer.Close()
		}
		if _, err := os.Stat(filepath.Join(work, "reports", "out.json")); err != nil {
			t.Fatalf("expected in-tree file to be created: %v", err)
		}
	})

	t.Run("absolute path outside tree is rejected", func(t *testing.T) {
		_, _, err := openSink(os.Stdout, guarded, outputPathConfined)
		if err == nil {
			t.Fatal("expected error for absolute path outside working tree")
		}
		assertGuardedIntact(t, guarded)
	})

	t.Run("dot-dot traversal is rejected", func(t *testing.T) {
		rel, err := filepath.Rel(work, guarded)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rel, "..") {
			t.Skipf("relative path %q has no traversal component", rel)
		}
		_, _, err = openSink(os.Stdout, rel, outputPathConfined)
		if err == nil {
			t.Fatal("expected error for ../ traversal")
		}
		assertGuardedIntact(t, guarded)
	})

	t.Run("symlinked leaf is not followed", func(t *testing.T) {
		link := filepath.Join(work, "evil.json")
		if err := os.Symlink(guarded, link); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		_, closer, err := openSink(os.Stdout, "evil.json", outputPathConfined)
		if closer != nil {
			_ = closer.Close()
		}
		if err == nil {
			t.Fatal("expected error opening a symlinked leaf pointing outside the tree")
		}
		assertGuardedIntact(t, guarded)
	})

	t.Run("symlinked parent directory is rejected", func(t *testing.T) {
		linkDir := filepath.Join(work, "evildir")
		if err := os.Symlink(outside, linkDir); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		_, closer, err := openSink(os.Stdout, "evildir/authorized_keys", outputPathConfined)
		if closer != nil {
			_ = closer.Close()
		}
		if err == nil {
			t.Fatal("expected error for write through a symlinked directory")
		}
		assertGuardedIntact(t, guarded)
	})
}

func TestOpenSinkAllowsOperatorOutputOutsideWorkingTree(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	chdir(t, work)

	out := filepath.Join(outside, "reports", "issues.json")
	w, closer, err := openSink(os.Stdout, out, outputPathOperator)
	if err != nil {
		t.Fatalf("open operator sink: %v", err)
	}
	if _, err := w.Write([]byte("operator\n")); err != nil {
		t.Fatalf("write operator sink: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close operator sink: %v", err)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read operator sink: %v", err)
	}
	if string(body) != "operator\n" {
		t.Fatalf("operator sink body = %q", body)
	}
}

func TestEmitDiagnosticsUsesOutputPathAuthority(t *testing.T) {
	work := t.TempDir()
	outside := t.TempDir()
	chdir(t, work)

	t.Run("config path is confined", func(t *testing.T) {
		out := filepath.Join(outside, "from-config.json")
		cfg := configWithJSONPath(out)
		var stdout strings.Builder
		err := emitDiagnostics(&stdout, cfg, nil, &runFlags{setFlags: map[string]bool{}})
		if err == nil {
			t.Fatal("expected config-origin output path outside working tree to fail")
		}
		if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
			t.Fatalf("config-origin path was created or stat failed unexpectedly: %v", statErr)
		}
	})

	t.Run("cli path keeps operator authority", func(t *testing.T) {
		out := filepath.Join(outside, "from-cli.json")
		cfg := configWithJSONPath(out)
		var stdout strings.Builder
		err := emitDiagnostics(&stdout, cfg, nil, &runFlags{
			setFlags: map[string]bool{"output.json.path": true},
		})
		if err != nil {
			t.Fatalf("emit cli-origin output path: %v", err)
		}
		body, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("read cli-origin output: %v", err)
		}
		if !strings.Contains(string(body), "[") {
			t.Fatalf("cli-origin JSON output looks malformed: %q", body)
		}
	})
}

func configWithJSONPath(path string) *config.Config {
	cfg := config.NewDefault()
	cfg.Output.Formats.JSON.Path = path
	return cfg
}

func assertGuardedIntact(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("guarded file disappeared: %v", err)
	}
	if string(b) != "original\n" {
		t.Fatalf("guarded file was modified: %q", string(b))
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}
