// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

func TestPackageExcluder_Nil(t *testing.T) {
	var px *PackageExcluder
	mp := &metadata.Package{LoadDir: "/root/pkg/foo"}
	if px.ShouldExcludePackage(mp, "/root") {
		t.Fatalf("nil excluder should exclude nothing")
	}
	if got := px.Patterns(); got != nil {
		t.Fatalf("nil excluder patterns = %v, want nil", got)
	}
}

func TestPackageExcluder_EmptyInputsReturnsNil(t *testing.T) {
	px, err := NewPackageExcluder(nil, nil)
	if err != nil {
		t.Fatalf("NewPackageExcluder: %v", err)
	}
	if px != nil {
		t.Fatalf("empty inputs should yield nil excluder, got %v", px)
	}
}

func TestPackageExcluder_DirPatterns(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		dir      string
		want     bool
	}{
		{"trailing slash matches prefix", []string{"pkg/pb/"}, "/root/pkg/pb/foo", true},
		{"trailing slash matches exact", []string{"pkg/pb/"}, "/root/pkg/pb", true},
		{"trailing slash does not match sibling", []string{"pkg/pb/"}, "/root/pkg/pbother/foo", false},
		{"glob star matches one segment", []string{"pkg/*/foo"}, "/root/pkg/bar/foo", true},
		{"glob star does not span separators", []string{"pkg/*/foo"}, "/root/pkg/bar/baz/foo", false},
		{"doublestar spans segments", []string{"pkg/**/foo"}, "/root/pkg/a/b/foo", true},
		{"basename glob matches anywhere", []string{"*.gen.go"}, "/root/pkg/foo/bar.gen.go", true},
		{"regex trailing dollar third_party$", []string{"third_party$"}, "/root/third_party", true},
		{"regex trailing dollar mismatch", []string{"third_party$"}, "/root/third_party/foo", false},
		{"unrelated path no match", []string{"pkg/pb/"}, "/root/pkg/services", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			px, err := NewPackageExcluder(nil, tc.patterns)
			if err != nil {
				t.Fatalf("NewPackageExcluder: %v", err)
			}
			mp := &metadata.Package{LoadDir: tc.dir}
			got := px.ShouldExcludePackage(mp, "/root")
			if got != tc.want {
				t.Fatalf("ShouldExcludePackage(dir=%s, patterns=%v) = %v, want %v",
					tc.dir, tc.patterns, got, tc.want)
			}
		})
	}
}

func TestPackageExcluder_GlobPatterns(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		dir      string
		want     bool
	}{
		{"leaf glob hits", []string{"*/leaf*/*"}, "/root/sub/leaf0/foo", true},
		{"leaf glob misses non-leaf", []string{"*/leaf*/*"}, "/root/sub/mid0/foo", false},
		{"basename only matches dir", []string{"leaf0"}, "/root/sub/leaf0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			px, err := NewPackageExcluder(tc.patterns, nil)
			if err != nil {
				t.Fatalf("NewPackageExcluder: %v", err)
			}
			mp := &metadata.Package{LoadDir: tc.dir}
			got := px.ShouldExcludePackage(mp, "/root")
			if got != tc.want {
				t.Fatalf("ShouldExcludePackage(dir=%s, patterns=%v) = %v, want %v",
					tc.dir, tc.patterns, got, tc.want)
			}
		})
	}
}

func TestPackageExcluder_FilePatterns_AllFilesMatch(t *testing.T) {
	// When every Go file matches a file pattern, the package is
	// dropped. Mirrors the "package is entirely generated" case.
	px, err := NewPackageExcluder([]string{"*.pb.go"}, nil)
	if err != nil {
		t.Fatalf("NewPackageExcluder: %v", err)
	}
	mp := &metadata.Package{
		LoadDir: "/root/pkg/pb/foo",
		CompiledGoFiles: []protocol.DocumentURI{
			protocol.URIFromPath("/root/pkg/pb/foo/a.pb.go"),
			protocol.URIFromPath("/root/pkg/pb/foo/b.pb.go"),
		},
	}
	if !px.ShouldExcludePackage(mp, "/root") {
		t.Fatalf("expected package with only *.pb.go files to be excluded")
	}
}

func TestPackageExcluder_FilePatterns_SomeFilesMatch(t *testing.T) {
	// When some files don't match, the package is NOT dropped.
	// File-level skip is out of scope for the bench's package-only
	// contract.
	px, err := NewPackageExcluder([]string{"*.pb.go"}, nil)
	if err != nil {
		t.Fatalf("NewPackageExcluder: %v", err)
	}
	mp := &metadata.Package{
		LoadDir: "/root/pkg/mixed",
		CompiledGoFiles: []protocol.DocumentURI{
			protocol.URIFromPath("/root/pkg/mixed/a.pb.go"),
			protocol.URIFromPath("/root/pkg/mixed/handwritten.go"),
		},
	}
	if px.ShouldExcludePackage(mp, "/root") {
		t.Fatalf("expected package with mixed files to NOT be excluded")
	}
}

func TestLoadExcludePathsFromYAML_V2Schema(t *testing.T) {
	// Minimal v2-schema doc mirroring c1's .golangci.yml shape.
	body := []byte(`version: "2"
linters:
  default: none
  exclusions:
    paths:
      - third_party$
      - builtin$
      - pkg/pb/
      - pkg/mockpb/
formatters:
  exclusions:
    paths:
      - pkg/pb/
      - tests/k8s
`)
	dir := t.TempDir()
	path := filepath.Join(dir, ".golangci.yml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got, err := LoadExcludePathsFromYAML(path)
	if err != nil {
		t.Fatalf("LoadExcludePathsFromYAML: %v", err)
	}
	want := map[string]bool{
		"third_party$": true,
		"builtin$":     true,
		"pkg/pb/":      true,
		"pkg/mockpb/":  true,
		"tests/k8s":    true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d unique patterns", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected pattern %q in result %v", p, got)
		}
	}
}

func TestLoadExcludePathsFromYAML_V1SkipDirs(t *testing.T) {
	// Legacy v1-schema run.skip-dirs / run.skip-files. Documented
	// in the brief as the minimum viable subset.
	body := []byte(`run:
  skip-dirs:
    - vendor
    - generated
  skip-files:
    - '.*\.pb\.go$'
`)
	dir := t.TempDir()
	path := filepath.Join(dir, ".golangci.yml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got, err := LoadExcludePathsFromYAML(path)
	if err != nil {
		t.Fatalf("LoadExcludePathsFromYAML: %v", err)
	}
	want := map[string]bool{
		"vendor":      true,
		"generated":   true,
		`.*\.pb\.go$`: true,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %d unique patterns", got, len(want))
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("unexpected pattern %q in result %v", p, got)
		}
	}
}

func TestLoadExcludePathsFromYAML_C1Subset(t *testing.T) {
	// Load c1's actual .golangci.yml shape (subset). This is the
	// "does the YAML reader work on the real file" check the brief
	// asks for. We use a verbatim slice of the relevant sections so
	// the test is hermetic — see the dispatcher report for the
	// full-file run against /data/squire/src/c1/.golangci.yml.
	body := []byte(`version: "2"
linters:
  default: none
  enable:
    - asasalint
  exclusions:
    generated: lax
    paths:
      - third_party$
      - builtin$
      - examples$
      - tests/k8s # No go files in here
      - pkg/pb/ # Generated proto code
      - pkg/mockpb/ # Generated mocks
formatters:
  enable:
    - goimports
  exclusions:
    generated: lax
    paths:
      - third_party$
      - builtin$
      - examples$
      - tests/k8s # No go files in here
      - pkg/pb/ # Generated proto code
      - pkg/mockpb/ # Generated mocks
`)
	dir := t.TempDir()
	path := filepath.Join(dir, ".golangci.yml")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	got, err := LoadExcludePathsFromYAML(path)
	if err != nil {
		t.Fatalf("LoadExcludePathsFromYAML: %v", err)
	}
	wantContains := []string{
		"pkg/pb/",
		"pkg/mockpb/",
		"third_party$",
		"builtin$",
		"examples$",
		"tests/k8s",
	}
	for _, w := range wantContains {
		var found bool
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("pattern %q missing from %v", w, got)
		}
	}
	// Patterns should be deduped across linters/formatters lists.
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	for p, n := range seen {
		if n != 1 {
			t.Errorf("pattern %q appeared %d times, want 1", p, n)
		}
	}
}

func TestLoadExcludePathsFromYAML_MissingFile(t *testing.T) {
	_, err := LoadExcludePathsFromYAML("/no/such/file")
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}

func TestMatchGlob_DoubleStar(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.gen.go", "pkg/foo/bar.gen.go", true},
		{"**/*.gen.go", "bar.gen.go", true},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/b/c", true},
		{"a/**/c", "a/b/x/c", true},
		{"a/**/c", "a/b/x/d", false},
	}
	for _, tc := range cases {
		got := matchGlob(tc.pattern, tc.name)
		if got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}
