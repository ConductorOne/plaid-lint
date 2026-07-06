// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import "testing"

// TestScopesForPatterns_EmptyFallsBackToViewScope locks in the
// backward-compat invariant: when InitializeWorkspaceWithPatterns is
// called with no patterns, the load scope set is exactly the
// pre-narrowing behavior (one viewLoadScope, which expands to
// `./...` under the module root for module views). This keeps the
// bench harness's W6 cold↔warm digest equivalence reachable — the
// bench never threads patterns through, so the load shape must be
// unchanged.
func TestScopesForPatterns_EmptyFallsBackToViewScope(t *testing.T) {
	for _, name := range []string{"empty", "all-blanks"} {
		t.Run(name, func(t *testing.T) {
			var patterns []string
			if name == "all-blanks" {
				patterns = []string{"", ""}
			}
			scopes := scopesForPatterns(GoModView, patterns)
			if len(scopes) != 1 {
				t.Fatalf("len(scopes) = %d, want 1; got %#v", len(scopes), scopes)
			}
			if _, ok := scopes[0].(viewLoadScope); !ok {
				t.Errorf("scopes[0] is %T, want viewLoadScope", scopes[0])
			}
		})
	}
}

// TestScopesForPatterns_NarrowsToPackageScopes asserts the narrow
// path: each non-empty pattern becomes a packageLoadScope passed
// through to packages.Load as a query string verbatim.
func TestScopesForPatterns_NarrowsToPackageScopes(t *testing.T) {
	patterns := []string{
		"./pkg/foo/...",
		"/abs/path/...",
		"github.com/example/bar",
	}
	scopes := scopesForPatterns(GoModView, patterns)
	if len(scopes) != len(patterns) {
		t.Fatalf("len(scopes) = %d, want %d; got %#v", len(scopes), len(patterns), scopes)
	}
	for i, s := range scopes {
		got, ok := s.(packageLoadScope)
		if !ok {
			t.Errorf("scopes[%d] is %T, want packageLoadScope", i, s)
			continue
		}
		if string(got) != patterns[i] {
			t.Errorf("scopes[%d] = %q, want %q", i, string(got), patterns[i])
		}
	}
}

// TestScopesForPatterns_AdHocViewIgnoresPatterns matches the
// reloadWorkspace fallback in snapshot.go: an AdHocView cannot be
// reloaded by package path because go/packages's ad-hoc handling
// keys on the view's single directory. Patterns are silently
// dropped and the view scope is loaded instead.
func TestScopesForPatterns_AdHocViewIgnoresPatterns(t *testing.T) {
	patterns := []string{"./pkg/foo/...", "./pkg/bar/..."}
	scopes := scopesForPatterns(AdHocView, patterns)
	if len(scopes) != 1 {
		t.Fatalf("len(scopes) = %d, want 1; got %#v", len(scopes), scopes)
	}
	if _, ok := scopes[0].(viewLoadScope); !ok {
		t.Errorf("scopes[0] is %T, want viewLoadScope", scopes[0])
	}
}

// TestScopesForPatterns_SkipsBlanksMixed asserts that a mix of
// empty and non-empty patterns produces only the non-empty scopes
// (rather than producing an empty scope query, which packages.Load
// would interpret as ./).
func TestScopesForPatterns_SkipsBlanksMixed(t *testing.T) {
	scopes := scopesForPatterns(GoModView, []string{"", "./pkg/foo/...", ""})
	if len(scopes) != 1 {
		t.Fatalf("len(scopes) = %d, want 1; got %#v", len(scopes), scopes)
	}
	got, ok := scopes[0].(packageLoadScope)
	if !ok {
		t.Fatalf("scopes[0] is %T, want packageLoadScope", scopes[0])
	}
	if string(got) != "./pkg/foo/..." {
		t.Errorf("scopes[0] = %q, want ./pkg/foo/...", string(got))
	}
}
