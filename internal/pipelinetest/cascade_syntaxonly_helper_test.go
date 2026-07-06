// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// runCascadeSyntaxOnlyDepInternalsEdit is the shared body of every
// per-analyzer regression test. It pulls the named linter's
// production AnalyzerFn out of the registry catalog, sanity-checks
// that the wire registered a SyntaxOnly descriptor in
// BundledRegistry, then drives the cascade fixture with a
// dep-internals (non-exported helper-add) edit and asserts:
//
//  1. CORRECTNESS — importers' diagnostic stream is byte-equal
//     across the edit.
//  2. WIN — warm SyntaxOnly L1 hits == numRoots (the importers all
//     hit, the edited app/ correctly misses).
//
// linterName is the catalog name (e.g. "whitespace"); configSalt is
// the test-local registry salt (one byte distinct from sibling tests
// to prevent any cross-pollination via the AnalyzerVersion).
// analyzerVersion likewise distinguishes the test's analyzer-version
// header from siblings.
func runCascadeSyntaxOnlyDepInternalsEdit(
	t *testing.T,
	linterName string,
	configSalt byte,
	analyzerVersion string,
) {
	t.Helper()
	requireGo(t)

	a := newSyntaxOnlyAnalyzerFromCatalog(t, linterName)

	reg := analyzers.NewRegistry()
	reg.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{configSalt} },
		AnalyzerVersion: analyzerVersion,
		CacheVersion:    1,
		TypeUseScope:    analyzers.TypeUseSyntaxOnly,
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(a)}

	// Use leakyTempDir (not t.TempDir) for the workspace and L1/L2
	// dirs. clcache.Open launches a background GC goroutine whose
	// lifetime escapes the *Cache; t.TempDir's RemoveAll cleanup
	// races it with sporadic "directory not empty" errors.
	modDir := leakyTempDir(t, "plaid-cascade-mod-")
	const numRoots = 5
	cascadeFixture(t, modDir, numRoots)
	l1Dir := leakyTempDir(t, "plaid-cascade-l1-")
	l2Dir := leakyTempDir(t, "plaid-cascade-l2-")
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	cold := runCascadeOnce(t, modDir, l1Dir, l2Dir, reg)
	if cold.l1.Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0; metrics=%+v", cold.l1)
	}

	// Edit ONLY a non-exported helper in app/app.go (the
	// dep-internals-changed gate, stronger than a comment-only
	// edit). Exported API unchanged.
	appFile := filepath.Join(modDir, "app", "app.go")
	editedBody := []byte(`package app

// T is the cascade-mid type touched (only transitively) by every
// root. The dep-internals edit appends a non-exported helper below.
type T struct {
	Name string
}

// New is the constructor every root imports.
func New(name string) *T { return &T{Name: name} }

// internalHelper is a non-exported addition introduced by the
// cascade-syntaxonly tests — exported API is unchanged so importers'
// L1 entries must hit.
func internalHelper() int { return 42 }
`)
	if err := os.WriteFile(appFile, editedBody, 0o644); err != nil {
		t.Fatalf("rewrite app.go: %v", err)
	}

	warm := runCascadeOnce(t, modDir, l1Dir, l2Dir, reg)

	if warm.digest != cold.digest {
		t.Errorf("%s diagnostic stream changed across a non-exported dep edit:\n  cold=%s\n  warm=%s",
			linterName, cold.digest, warm.digest)
	}
	if warm.l1.SyntaxOnlyHits < numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want >= %d (%s should hit on cascade-affected importers). metrics=%+v",
			warm.l1.SyntaxOnlyHits, numRoots, linterName, warm.l1)
	}
	if warm.l1.SyntaxOnlyHits > numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want <= %d (over-hit; edited app/ must miss). metrics=%+v",
			warm.l1.SyntaxOnlyHits, numRoots, warm.l1)
	}
	t.Logf("%s SyntaxOnly: %d/%d importers hit L1; diagnostic equivalence preserved. cold=%+v warm=%+v",
		linterName, warm.l1.SyntaxOnlyHits, numRoots, cold.l1, warm.l1)
}

// newSyntaxOnlyAnalyzerFromCatalog pulls a freshly-built analyzer of
// the named linter from the production registry catalog and verifies
// that calling its AnalyzerFn registered a SyntaxOnly descriptor in
// BundledRegistry. Used by every per-analyzer test so the
// production wire is exercised end-to-end.
func newSyntaxOnlyAnalyzerFromCatalog(t *testing.T, name string) *analysis.Analyzer {
	t.Helper()
	fn := registry.AnalyzerFnForTest(name)
	if fn == nil {
		t.Fatalf("%s AnalyzerFn not registered in default catalog", name)
	}
	as := fn(nil)
	if len(as) != 1 || as[0] == nil {
		t.Fatalf("%s AnalyzerFn returned %d analyzers, want 1", name, len(as))
	}
	d := analyzers.BundledRegistry.Lookup(as[0])
	if d == nil {
		t.Fatalf("%s descriptor not registered in BundledRegistry", name)
	}
	if d.TypeUseScope != analyzers.TypeUseSyntaxOnly {
		t.Fatalf("%s TypeUseScope = %v, want SyntaxOnly", name, d.TypeUseScope)
	}
	return as[0]
}
