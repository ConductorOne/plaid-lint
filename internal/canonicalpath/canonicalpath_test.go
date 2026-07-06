// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package canonicalpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name    string
		absPath string
		pkgPath string
		want    string
	}{
		{
			name:    "workspace file",
			absPath: "/data/squire/src/c1/pkg/foo/bar.go",
			pkgPath: "github.com/conductorone/c1/pkg/foo",
			want:    "github.com/conductorone/c1/pkg/foo/bar.go",
		},
		{
			name:    "stdlib",
			absPath: "/usr/local/go/src/net/http/server.go",
			pkgPath: "net/http",
			want:    "net/http/server.go",
		},
		{
			name:    "test file",
			absPath: "/data/squire/src/c1/pkg/foo/bar_test.go",
			pkgPath: "github.com/conductorone/c1/pkg/foo",
			want:    "github.com/conductorone/c1/pkg/foo/bar_test.go",
		},
		{
			name:    "external test package",
			absPath: "/data/squire/src/c1/pkg/foo/bar_external_test.go",
			pkgPath: "github.com/conductorone/c1/pkg/foo_test",
			want:    "github.com/conductorone/c1/pkg/foo_test/bar_external_test.go",
		},
		{
			name:    "cgo generated",
			absPath: "/tmp/go-build123/b001/_cgo_gotypes.go",
			pkgPath: "net",
			want:    "net/_cgo_gotypes.go",
		},
		{
			name:    "empty pkgPath returns absPath",
			absPath: "/repo/pkg/x.go",
			pkgPath: "",
			want:    "/repo/pkg/x.go",
		},
		{
			name:    "empty absPath stays empty",
			absPath: "",
			pkgPath: "foo/bar",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Canonicalize(tt.absPath, tt.pkgPath)
			if got != tt.want {
				t.Errorf("Canonicalize(%q, %q) = %q, want %q",
					tt.absPath, tt.pkgPath, got, tt.want)
			}
		})
	}
}

func TestResolver_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "pkg", "foo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(dir, "bar.go")
	if err := os.WriteFile(abs, []byte("package foo"), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgPath := "github.com/example/repo/pkg/foo"
	canon := Canonicalize(abs, pkgPath)
	if want := pkgPath + "/bar.go"; canon != want {
		t.Fatalf("Canonicalize = %q, want %q", canon, want)
	}

	r := NewResolver(map[string]string{pkgPath: dir})
	got := r.Resolve(canon)
	if got != abs {
		t.Errorf("Resolve(%q) = %q, want %q", canon, got, abs)
	}
}

func TestResolver_NilFallback(t *testing.T) {
	canon := "net/http/server.go"
	if got := (*Resolver)(nil).Resolve(canon); got != canon {
		t.Errorf("nil resolver: got %q, want %q", got, canon)
	}
	if got := NewResolver(nil).Resolve(canon); got != canon {
		t.Errorf("empty map resolver: got %q, want %q", got, canon)
	}
}

func TestResolver_UnknownPackage(t *testing.T) {
	// pkgPath not in loaded set → return canonical unchanged. This is
	// the cgo / vendored / external-test fallback path.
	r := NewResolver(map[string]string{
		"github.com/example/known": "/abs/known",
	})
	canon := "github.com/example/unknown/x.go"
	if got := r.Resolve(canon); got != canon {
		t.Errorf("unknown pkg: got %q, want %q", got, canon)
	}
}

func TestResolver_NoSlash(t *testing.T) {
	r := NewResolver(map[string]string{"foo": "/abs"})
	// No slash → not a canonical form → return as-is.
	if got := r.Resolve("bareword"); got != "bareword" {
		t.Errorf("no-slash: got %q, want %q", got, "bareword")
	}
}

func TestResolver_AbsoluteInput(t *testing.T) {
	// An absolute path passed to Resolve should be returned unchanged
	// — it is already in the form we want.
	r := NewResolver(map[string]string{"foo/bar": "/abs/foo/bar"})
	abs := "/some/absolute/path.go"
	// Resolve splits on last "/" and looks up "/some/absolute". That
	// won't match, so we get the input back. Document the behaviour.
	if got := r.Resolve(abs); got != abs {
		t.Errorf("absolute input: got %q, want %q", got, abs)
	}
}

func TestIsCanonical(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"github.com/foo/bar/x.go", true},
		{"net/http/server.go", true},
		{"", false},
		{"single", false},
		{"/abs/path.go", false},
		{"./rel/path.go", true}, // dotted-relative is not absolute
	}
	for _, tc := range cases {
		if got := IsCanonical(tc.in); got != tc.want {
			t.Errorf("IsCanonical(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalize_LongPkgPath(t *testing.T) {
	// Sanity: a pkgPath with many slashes still round-trips through
	// LastIndex-based parsing.
	abs := "/x/y/z/foo.go"
	pkgPath := "a/b/c/d/e/f"
	canon := Canonicalize(abs, pkgPath)
	want := "a/b/c/d/e/f/foo.go"
	if canon != want {
		t.Fatalf("canonicalize: got %q want %q", canon, want)
	}
	r := NewResolver(map[string]string{pkgPath: "/local/x/y"})
	if got := r.Resolve(canon); got != "/local/x/y/foo.go" {
		t.Errorf("resolve: got %q", got)
	}
}
