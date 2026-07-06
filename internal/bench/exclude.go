// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"gopkg.in/yaml.v3"
)

// PackageExcluder decides whether a workspace package should be
// dropped from the benchmark's analysis set before the package map
// is handed to Analyze. The contract is "package-level only": a
// package is either fully analyzed or fully skipped — file-level
// skip is deferred.
//
// A nil *PackageExcluder excludes nothing (zero-value safe).
type PackageExcluder struct {
	// dirPatterns are filepath.Match-style globs applied to a
	// package's source directory, both as-is and after stripping the
	// module root prefix. Patterns containing "/" are matched against
	// the path-relative-to-module-root; patterns with no separator are
	// matched against the basename. This matches the loose substring
	// semantics .golangci.yaml uses for its `paths` list (`pkg/pb/`,
	// `third_party$`, etc.).
	dirPatterns []string

	// filePatterns are filepath.Match-style globs applied against
	// every Go file URI in a package. A package is excluded when
	// ALL of its Go files match (i.e. the package is entirely
	// generated). Mirrors the package-level filtering contract;
	// the per-file Pass.Files trim is engine work and out of scope
	// here.
	filePatterns []string

	// regexes are pre-compiled substring regexes from .golangci.yaml's
	// `linters.exclusions.paths` list. golangci-lint treats those as
	// regex patterns; common idioms like "third_party$" and "pkg/pb/"
	// work as either substring or regex, so we match both ways.
	regexes []*regexp.Regexp
}

// NewPackageExcluder builds an excluder from the supplied glob
// patterns and the optional YAML-loaded paths list. Returns nil
// when no patterns are supplied.
func NewPackageExcluder(globs, yamlPaths []string) (*PackageExcluder, error) {
	if len(globs) == 0 && len(yamlPaths) == 0 {
		return nil, nil
	}
	px := &PackageExcluder{}
	for _, g := range globs {
		// Validate glob syntax up front so a bad pattern fails
		// the run rather than silently matching nothing.
		if _, err := filepath.Match(g, ""); err != nil {
			return nil, fmt.Errorf("invalid --exclude-glob %q: %w", g, err)
		}
		// A glob is always tried as a dir pattern (the bench's
		// primary mechanism: drop matching packages by source dir)
		// and as a file pattern (so `*.pb.go` works for the
		// generated-code idiom). The dir-match check below also
		// inspects the basename for separator-free patterns.
		px.dirPatterns = append(px.dirPatterns, g)
		px.filePatterns = append(px.filePatterns, g)
	}
	for _, p := range yamlPaths {
		if p == "" {
			continue
		}
		px.dirPatterns = append(px.dirPatterns, p)
		re, err := regexp.Compile(p)
		if err != nil {
			// Bad regex from a hand-edited YAML is a soft failure:
			// keep the literal-substring match, drop the regex.
			continue
		}
		px.regexes = append(px.regexes, re)
	}
	return px, nil
}

// Patterns returns the user-supplied patterns for surface in the
// JSON output. The slice is a defensive copy; callers may mutate.
func (px *PackageExcluder) Patterns() []string {
	if px == nil {
		return nil
	}
	out := make([]string, 0, len(px.dirPatterns)+len(px.filePatterns))
	seen := map[string]bool{}
	for _, p := range px.dirPatterns {
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	for _, p := range px.filePatterns {
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out
}

// ShouldExcludePackage reports whether the given package should be
// dropped from the analysis set. The check considers both directory
// patterns (matched against the package's source directory relative
// to moduleRoot) and file patterns (a package is dropped when every
// Go file matches).
//
// The source directory is derived from CompiledGoFiles[0], falling
// back to GoFiles[0], with mp.LoadDir as a last resort for ad-hoc
// packages where neither file list is populated. mp.LoadDir alone is
// not a usable directory candidate for `go list ./...`-shaped
// workspaces: every package shares the loader cwd as its LoadDir, so
// patterns like "pkg/pb/" can never match individual packages
// (LEARN-FGL-003).
//
// moduleRoot is the absolute fixture path; pass cfg.Fixture.
func (px *PackageExcluder) ShouldExcludePackage(mp *metadata.Package, moduleRoot string) bool {
	if px == nil || mp == nil {
		return false
	}
	if px.dirMatches(packageSourceDir(mp), moduleRoot) {
		return true
	}
	if len(px.filePatterns) == 0 {
		return false
	}
	files := mp.CompiledGoFiles
	if len(files) == 0 {
		files = mp.GoFiles
	}
	if len(files) == 0 {
		return false
	}
	for _, uri := range files {
		path := uri.Path()
		if !px.fileMatches(path, moduleRoot) {
			return false
		}
	}
	return true
}

// packageSourceDir returns the package's source directory: the
// directory containing its first compiled Go file (or GoFile), with
// mp.LoadDir as a final fallback. Returns "" only when mp has no
// usable directory information.
func packageSourceDir(mp *metadata.Package) string {
	if len(mp.CompiledGoFiles) > 0 {
		return filepath.Dir(mp.CompiledGoFiles[0].Path())
	}
	if len(mp.GoFiles) > 0 {
		return filepath.Dir(mp.GoFiles[0].Path())
	}
	return mp.LoadDir
}

// dirMatches reports whether dir matches any directory pattern.
// The dir is checked both as the raw absolute path and as the
// module-root-relative form so callers can write `pkg/pb/` style
// patterns and have them work against any fixture root. dir should
// be the package's source directory (see packageSourceDir).
func (px *PackageExcluder) dirMatches(dir, moduleRoot string) bool {
	candidates := candidatePaths(dir, moduleRoot)
	base := filepath.Base(dir)
	for _, c := range candidates {
		for _, p := range px.dirPatterns {
			if matchGlob(p, c) {
				return true
			}
			// Trailing-slash convention from golangci-lint:
			// "pkg/pb/" should hit "pkg/pb" via prefix.
			trimmed := strings.TrimSuffix(p, "/")
			if trimmed != p && (c == trimmed || strings.HasPrefix(c, trimmed+"/")) {
				return true
			}
			// Separator-free patterns also match the basename.
			if !strings.Contains(p, "/") {
				if ok, _ := filepath.Match(p, base); ok {
					return true
				}
			}
		}
		for _, re := range px.regexes {
			if re.MatchString(c) {
				return true
			}
		}
	}
	return false
}

// fileMatches reports whether path matches any file pattern.
func (px *PackageExcluder) fileMatches(path, moduleRoot string) bool {
	candidates := candidatePaths(path, moduleRoot)
	base := filepath.Base(path)
	for _, p := range px.filePatterns {
		if matchGlob(p, base) {
			return true
		}
		for _, c := range candidates {
			if matchGlob(p, c) {
				return true
			}
		}
	}
	return false
}

// candidatePaths returns the absolute path and the module-root-relative
// path (with forward slashes) for matching. Returns just the absolute
// path when the path is not under moduleRoot.
func candidatePaths(path, moduleRoot string) []string {
	abs := path
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	out := []string{filepath.ToSlash(abs)}
	if moduleRoot != "" {
		if rel, err := filepath.Rel(moduleRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// matchGlob is a wrapper around filepath.Match that adds a "**"
// extension for matching across path separators. Implementation is
// intentionally simple: "**" is rewritten to a literal-segment
// matcher by reducing to per-segment Match calls. Good enough for
// the bench's exclude needs (e.g. "**/*.gen.go", "*/leaf*/*").
func matchGlob(pattern, name string) bool {
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)
	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(pattern, name)
		if err != nil {
			return false
		}
		if ok {
			return true
		}
		// Tolerate the common case "pattern matches a suffix":
		// users writing "*.pb.go" expect that to match
		// "pkg/pb/foo/bar.pb.go". filepath.Match doesn't span
		// separators, so we also try the basename + the last
		// path segment of the pattern against suffix slices.
		if !strings.Contains(pattern, "/") {
			if ok, _ := filepath.Match(pattern, filepath.Base(name)); ok {
				return true
			}
		}
		return false
	}
	return matchDoubleStar(pattern, name)
}

// matchDoubleStar is a tiny implementation of "**" globbing: "**"
// matches zero or more path segments. Other meta-characters are
// passed through to filepath.Match per-segment.
func matchDoubleStar(pattern, name string) bool {
	patSegs := strings.Split(pattern, "/")
	nameSegs := strings.Split(name, "/")
	return matchSegs(patSegs, nameSegs)
}

func matchSegs(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// "**" matches any number of segments (including zero).
			if len(pat) == 1 {
				return true
			}
			// Try every split point.
			for i := 0; i <= len(name); i++ {
				if matchSegs(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := filepath.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	return len(name) == 0
}

// LoadExcludePathsFromYAML reads a .golangci.yaml file and returns
// the union of path-pattern lists declared at:
//
//   - linters.exclusions.paths (v2 schema, current c1)
//   - formatters.exclusions.paths (v2 schema, current c1)
//   - run.skip-dirs (v1 schema, legacy)
//   - run.skip-files (v1 schema, legacy)
//
// This is the minimum viable subset called out in the Phase 1.7
// brief. Other golangci-lint configuration knobs (per-linter
// path-except rules, per-preset toggles, regex-vs-glob semantics)
// are NOT consumed — the bench is making a coarse "skip generated
// code" decision, not faithfully reproducing the linter's
// per-issue rule engine.
//
// What this function does NOT support (callers who need them
// should preprocess their YAML or supply --exclude-glob):
//
//   - linters.exclusions.rules[].path / path-except (per-linter
//     scoped paths; the bench drops the package wholesale)
//   - linters.exclusions.presets (built-in golangci-lint preset
//     rules like "comments" / "common-false-positives")
//   - generated:lax / generated:strict semantics
//   - issues.exclude-rules (deprecated v1 sibling of rules)
//
// Returns (patterns, nil) on success; (nil, err) when the file is
// unreadable or unparseable. An empty paths list is not an error
// — the caller decides whether to surface that.
func LoadExcludePathsFromYAML(path string) ([]string, error) {
	if path == "" {
		return nil, errors.New("empty path")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc golangciConfig
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range doc.Linters.Exclusions.Paths {
		add(p)
	}
	for _, p := range doc.Formatters.Exclusions.Paths {
		add(p)
	}
	for _, p := range doc.Run.SkipDirs {
		add(p)
	}
	for _, p := range doc.Run.SkipFiles {
		add(p)
	}
	return out, nil
}

// golangciConfig is a minimal YAML schema covering only the path
// lists the bench cares about. Everything else in .golangci.yaml is
// silently ignored by the yaml decoder.
type golangciConfig struct {
	Linters struct {
		Exclusions struct {
			Paths []string `yaml:"paths"`
		} `yaml:"exclusions"`
	} `yaml:"linters"`
	Formatters struct {
		Exclusions struct {
			Paths []string `yaml:"paths"`
		} `yaml:"exclusions"`
	} `yaml:"formatters"`
	Run struct {
		SkipDirs  []string `yaml:"skip-dirs"`
		SkipFiles []string `yaml:"skip-files"`
	} `yaml:"run"`
}
