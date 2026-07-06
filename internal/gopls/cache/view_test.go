// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

import "testing"

// TestNormalizePackagePattern pins the directory-vs-import-path
// discrimination that scopesForPatterns relies on. The bug this
// guards against: bare patterns like "pkg/foo" without a `./`
// prefix are interpreted by `go list` as import-path patterns
// (in c1's case `gitlab.com/ductone/c1/pkg/foo`), which fail to
// resolve in single-module repos whose import root doesn't match
// the working directory. packages.Load then returns a synthetic
// placeholder with no GoFiles, the loader drops it, and the
// engine sees zero workspace packages.
func TestNormalizePackagePattern(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// Bare relative paths get the `./` prefix.
		{"bare-segment", "pkg", "./pkg"},
		{"bare-nested", "pkg/foo", "./pkg/foo"},
		{"bare-recursive", "pkg/foo/...", "./pkg/foo/..."},
		{"bare-deep", "cmd/dev-util/wipe", "./cmd/dev-util/wipe"},

		// Already-prefixed paths pass through unchanged.
		{"dot-current", ".", "."},
		{"dot-slash", "./pkg/foo", "./pkg/foo"},
		{"dot-slash-recursive", "./...", "./..."},
		{"parent-dir", "../sibling", "../sibling"},
		{"absolute", "/abs/path", "/abs/path"},

		// Import paths (dot in first segment) pass through.
		{"import-github", "github.com/foo/bar", "github.com/foo/bar"},
		{"import-gitlab", "gitlab.com/ductone/c1/pkg/foo", "gitlab.com/ductone/c1/pkg/foo"},
		{"import-gopkg", "gopkg.in/yaml.v3", "gopkg.in/yaml.v3"},
		{"import-recursive", "github.com/foo/bar/...", "github.com/foo/bar/..."},

		// Dotted directory components after the first segment are
		// not import paths — the dot has to be in the first
		// segment to count.
		{"dotted-subdir", "pkg/foo.bar/baz", "./pkg/foo.bar/baz"},

		// Empty / degenerate inputs pass through; scopesForPatterns
		// filters empty patterns separately.
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePackagePattern(tt.in)
			if got != tt.want {
				t.Errorf("normalizePackagePattern(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
