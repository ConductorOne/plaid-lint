// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

import (
	"os"
	"path/filepath"
	"testing"

	ineffassign "github.com/gordonklaus/ineffassign/pkg/ineffassign"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
)

// TestCascadeIneffassignSyntaxOnly is the focused regression gate for
// the first SyntaxOnly classification: ineffassign.
//
// Two halves:
//
//  1. CORRECTNESS — running ineffassign over the cascade fixture
//     produces the same diagnostic stream before and after a
//     comment-only edit to the dep package. If this fails, ineffassign
//     either grew a TypesInfo / Pkg dependency (the SyntaxOnly
//     classification is unsafe and must drop back to FullTypeGraph),
//     or the narrowing introduced a stale-hit class.
//
//  2. WIN — warm L1 hits = numRoots; the importer packages hit L1
//     on the dep-internals-only edit. This is the analytical
//     justification for the SyntaxOnly classification: if hits don't
//     materialize the classification is harmless but useless.
//
// Uses the actual ineffassign analyzer (not the synthetic
// cascadesyntaxonly one) so a future ineffassign upgrade that adds a
// dep-type read breaks this test rather than silently producing
// stale L1 hits in production.
func TestCascadeIneffassignSyntaxOnly(t *testing.T) {
	requireGo(t)

	a := ineffassign.Analyzer
	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{0x1e} },
		AnalyzerVersion: "cascade-ineffassign-v1",
		CacheVersion:    1,
		TypeUseScope:    analyzers.TypeUseSyntaxOnly,
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(a)}

	modDir := t.TempDir()
	const numRoots = 5
	cascadeFixture(t, modDir, numRoots)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	cold := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry)
	if cold.l1.Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0; metrics=%+v", cold.l1)
	}

	// Edit only a comment in app/app.go. ineffassign reads pass.Files
	// for the importer packages, none of which include app/'s source
	// — so the importers' diagnostic outputs are invariant.
	appFile := filepath.Join(modDir, "app", "app.go")
	body, err := os.ReadFile(appFile)
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	body = append(body, []byte("\n// ineffassign-cascade trailer\n")...)
	if err := os.WriteFile(appFile, body, 0o644); err != nil {
		t.Fatalf("rewrite app.go: %v", err)
	}

	warm := runCascadeOnce(t, modDir, l1Dir, l2Dir, registry)

	// Correctness half: diagnostic-stream digest equivalence across
	// the dep-internals-only edit.
	if warm.digest != cold.digest {
		t.Errorf("ineffassign diagnostic stream changed across a comment-only dep edit:\n  cold=%s\n  warm=%s",
			cold.digest, warm.digest)
	}

	// Win half: importers must hit L1 on warm.
	if warm.l1.SyntaxOnlyHits < numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want >= %d (ineffassign should hit on cascade-affected importers). metrics=%+v",
			warm.l1.SyntaxOnlyHits, numRoots, warm.l1)
	}
	if warm.l1.SyntaxOnlyHits > numRoots {
		t.Errorf("warm: SyntaxOnly L1 hits = %d, want <= %d (over-hit; edited app/ must miss). metrics=%+v",
			warm.l1.SyntaxOnlyHits, numRoots, warm.l1)
	}
	t.Logf("ineffassign SyntaxOnly: %d/%d importers hit L1; diagnostic equivalence preserved. cold=%+v warm=%+v",
		warm.l1.SyntaxOnlyHits, numRoots, cold.l1, warm.l1)
}
