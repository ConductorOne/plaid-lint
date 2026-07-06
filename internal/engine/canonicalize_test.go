// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// canonicalFixture builds a tiny module whose default-analyzer run
// will produce at least one diagnostic. The package path is fixed at
// "canontest" so the canonical form is predictable.
func canonicalFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("go.mod", "module canontest\n\ngo 1.21\n")
	// ineffassign-friendly content: x=1; x=2; _=x triggers ineffassign.
	mustWrite("main.go", `package main

func Hello(name string) string {
	x := 1
	x = 2
	_ = x
	return "hello " + name
}

func main() {
	_ = Hello("world")
}
`)
	return dir
}

// canonRunInput is a minimal RunInput pointed at a canonicalFixture
// with no exclusion filter (we want diagnostics to flow through).
func canonRunInput(t *testing.T, fixture string, l0c *l0.Cache) RunInput {
	t.Helper()
	l1, err := clcache.Open(filepath.Join(t.TempDir(), "l1"))
	if err != nil {
		t.Fatalf("open L1: %v", err)
	}
	l2, err := clcache.Open(filepath.Join(t.TempDir(), "l2"))
	if err != nil {
		t.Fatalf("open L2: %v", err)
	}
	cfg := config.NewDefault()
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("registry.Build: %v", err)
	}
	return RunInput{
		Config:    cfg,
		Registry:  reg,
		Workspace: subproc.WorkspaceRef{ModuleRoot: fixture},
		L1:        l1,
		L2:        l2,
		L0:        l0c,
	}
}

// TestCanonicalize_EngineEmitsCanonical drives the engine and asserts
// every returned Pos.Filename is in canonical form (no absolute
// path prefix matching the workspace fixture). This is the headline
// behavioural assertion that L0 / cache layers see canonical bytes.
func TestCanonicalize_EngineEmitsCanonical(t *testing.T) {
	dir := canonicalFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	in := canonRunInput(t, dir, l0c)
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Diagnostics) == 0 {
		t.Skip("fixture produced no diagnostics; engine cannot exercise canonical assertion")
	}
	for _, d := range res.Diagnostics {
		if strings.HasPrefix(d.Pos.Filename, dir) {
			t.Errorf("Pos.Filename = %q starts with workspace prefix %q (engine emitted absolute path)",
				d.Pos.Filename, dir)
		}
		if !strings.HasPrefix(d.Pos.Filename, "canontest/") {
			t.Errorf("Pos.Filename = %q; want canonical form starting with \"canontest/\"", d.Pos.Filename)
		}
	}
	// PkgDirs should be populated so the CLI can reverse.
	if len(res.PkgDirs) == 0 {
		t.Errorf("RunOutput.PkgDirs is empty; CLI cannot reverse-map")
	}
	gotDir, ok := res.PkgDirs["canontest"]
	if !ok {
		t.Fatalf("PkgDirs missing 'canontest' entry; got %v", res.PkgDirs)
	}
	if gotDir != dir {
		t.Errorf("PkgDirs[\"canontest\"] = %q, want %q", gotDir, dir)
	}
}

// TestCanonicalize_L0WriteFormat reads the raw bytes of an L0 entry
// after a cold run and asserts no absolute paths appear in the
// serialized form. This is the cross-machine portability precondition.
func TestCanonicalize_L0WriteFormat(t *testing.T) {
	dir := canonicalFixture(t)
	l0Root := t.TempDir()
	l0c, err := l0.Open(l0Root)
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	in := canonRunInput(t, dir, l0c)
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Diagnostics) == 0 {
		t.Skip("fixture produced no diagnostics; cannot scan L0 bytes")
	}

	// Walk every .gob file written under l0Root and assert no byte
	// substring matches the absolute workspace path.
	absBytes := []byte(dir)
	found := false
	err = filepath.Walk(l0c.Path(), func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(p, ".gob") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(data, absBytes) {
			t.Errorf("L0 entry %s contains absolute workspace path %q (leaks dev-box paths cross-machine)", p, dir)
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk L0: %v", err)
	}
	_ = found
}

// TestCanonicalize_CLIRender simulates the CLI render path: run
// engine, apply the CLI's Resolver wiring, assert the resulting
// diagnostics' Pos.Filename is absolute on the local machine. This
// pins the rendering invariant: users see absolute paths, the cache
// stores canonical paths.
func TestCanonicalize_CLIRender(t *testing.T) {
	dir := canonicalFixture(t)
	l0c, err := l0.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open L0: %v", err)
	}
	in := canonRunInput(t, dir, l0c)
	res, err := Run(context.Background(), in)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Diagnostics) == 0 {
		t.Skip("fixture produced no diagnostics")
	}

	// Apply the CLI's reverse step.
	output.ResolveDiagnostics(canonicalpath.NewResolver(res.PkgDirs), res.Diagnostics)

	for _, d := range res.Diagnostics {
		if !filepath.IsAbs(d.Pos.Filename) {
			t.Errorf("post-Resolve Pos.Filename = %q; want absolute path", d.Pos.Filename)
		}
		if !strings.HasPrefix(d.Pos.Filename, dir) {
			t.Errorf("post-Resolve Pos.Filename = %q; want prefix %q (the workspace dir)",
				d.Pos.Filename, dir)
		}
	}
}

// TestCanonicalize_GeneratedCode pins the cgo / synthetic-file
// fallback behaviour. We can't easily run real cgo in a unit test,
// so we exercise the same code path with a synthetic uriPkg that
// omits a file's owning package — emulating the cgo case where the
// file's URI is the build cache, not the package's source tree.
func TestCanonicalize_GeneratedCode(t *testing.T) {
	// uriPkg only knows about workspace files. A synthetic / cgo
	// file's URI won't appear in the map.
	uriPkg := map[protocol.DocumentURI]string{
		protocol.URIFromPath("/repo/pkg/foo/a.go"): "example.com/foo",
	}
	// Build a diagnostic whose Pos.Filename points at a synthetic
	// path no loaded package owns.
	cgoPath := "/tmp/go-build-cache/b001/_cgo_gotypes.go"
	diags := []output.Diagnostic{{
		Pos: output.Position{Filename: cgoPath, Line: 12},
	}}
	canonicalizeDiagnostics(diags, uriPkg)
	if got := diags[0].Pos.Filename; got != cgoPath {
		t.Errorf("unowned file canonicalised to %q; want unchanged absolute path", got)
	}

	// Verify the reverse path: a Resolver returns the input unchanged
	// when the (absolute) string doesn't parse into a known pkgPath.
	r := canonicalpath.NewResolver(map[string]string{"some/pkg": "/abs/dir"})
	if got := r.Resolve(cgoPath); got != cgoPath {
		t.Errorf("Resolve on cgo path = %q; want unchanged", got)
	}
}
