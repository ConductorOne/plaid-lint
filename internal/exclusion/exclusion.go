// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package exclusion implements the post-analysis diagnostic filter that
// applies golangci-lint v2's `linters.exclusions:` semantics:
//
//   - `paths`         drop diagnostics whose path matches any pattern
//   - `paths-except`  keep only diagnostics whose path matches any pattern
//   - `presets`       built-in v2 preset rules (comments, legacy, ...)
//   - `rules`         user-supplied rules: linters + path[-except] + text/source
//   - `generated`     drop diagnostics in generated files (lax/strict/disable)
//
// Inputs are paired with the engine: diagnostics come back from engine.Run
// already in []output.Diagnostic form, and the filter rewrites the slice
// before the printer pipeline. The base path for relative-path matching is
// the workspace module root (Run already resolves this).
package exclusion

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
)

// Filter holds the compiled exclusion state for one Run.
type Filter struct {
	basePath string

	pathPatterns       []*regexp.Regexp
	pathExceptPatterns []*regexp.Regexp

	rules []compiledRule

	generatedMode    string
	generatedCache   map[string]bool
	generatedCacheMu sync.Mutex

	// staticcheckDefaultDisabled lists honnef check IDs that golangci-lint
	// disables by default for the staticcheck linter (set when the user
	// does not supply an explicit `staticcheck.checks` selector). Without
	// honoring this list, ST1000/ST1003/ST1016/ST1020/ST1021/ST1022 fire
	// in plaid but not in golangci-lint.
	staticcheckDefaultDisabled map[string]struct{}

	// targetDirs is the resolved set of relative directories the user
	// asked to lint (CLI positional args interpreted as go/packages
	// patterns). When non-empty, diagnostics for files outside these
	// directories are filtered out.
	//
	// This mirrors golangci-lint's behavior: `packages.Load(./pkg/foo/...)`
	// returns the matched packages, but plaid's gopls-rooted
	// workspace loader also retains every transitively-imported in-module
	// package as a "workspace package" (LSP semantics), so those get
	// analyzed and emit diagnostics. Without target-path filtering, a
	// `plaid-lint run ./pkg/c1semconv/...` reports issues in
	// `pkg/pb/...`, `pkg/builtin_connectors/...`, etc. — anything reachable
	// via imports. Filtering by target directory restores parity.
	//
	// Empty means "no scope filter" (load-everything behavior).
	targetDirs []targetDir

	// uniqByLine: when true (the upstream default), collapse multiple
	// diagnostics that share a (file, line) tuple down to the first.
	// Without this, an analyzer fan-out (e.g. a test-variant of a
	// package re-running the same gosec check) produces visible
	// duplicates.
	uniqByLine bool

	// nolint applies the upstream `nolint_filter` processor — drops
	// diagnostics whose (file, line, linter) falls inside a
	// `//nolint[:list]` directive's range. Set unconditionally because
	// golangci-lint v2 always applies this filter; there is no config
	// knob to disable it.
	nolint *nolintFilter
}

// targetDir is one resolved CLI target. dir is forward-slashed and
// relative to basePath; recursive is true for "/..." patterns.
type targetDir struct {
	dir       string
	recursive bool
}

// compiledRule is one fully-compiled rule (user-supplied or preset).
type compiledRule struct {
	linters    map[string]struct{}
	path       *regexp.Regexp
	pathExcept *regexp.Regexp
	text       *regexp.Regexp
	source     *regexp.Regexp
}

// NewFilter builds a Filter from the parsed Config. basePath should be
// the absolute module root used to compute the relative path each
// rule's `path` regex is matched against. targetPatterns is the
// user-supplied positional argument list from the CLI (e.g.
// `./pkg/c1semconv/...`); when non-empty, diagnostics emitted on files
// outside those targets are filtered out for golangci-lint parity. An
// empty targetPatterns slice means "no target-path filter."
//
// Returns an error only on regex compile failures (which
// Config.Validate should have already caught).
func NewFilter(cfg *config.Config, basePath string, targetPatterns []string) (*Filter, error) {
	if cfg == nil {
		return &Filter{
			basePath:       basePath,
			generatedMode:  config.GeneratedModeStrict,
			generatedCache: map[string]bool{},
			targetDirs:     resolveTargetDirs(targetPatterns),
			nolint:         newNolintFilter(),
		}, nil
	}
	exc := &cfg.Linters.Exclusions
	f := &Filter{
		basePath:                   basePath,
		generatedMode:              exc.Generated,
		generatedCache:             map[string]bool{},
		staticcheckDefaultDisabled: staticcheckDefaultDisabledSet(&cfg.Linters.Settings.Staticcheck),
		targetDirs:                 resolveTargetDirs(targetPatterns),
		// golangci-lint defaults uniq-by-line to true; we honor an
		// explicit yaml setting of `issues.uniq-by-line` but otherwise
		// match upstream's default.
		uniqByLine: true,
		nolint:     newNolintFilter(),
	}
	// The struct decodes `uniq-by-line` into a bool that defaults to
	// false. Distinguishing "user wrote `false`" from "key absent"
	// would require a *bool. For now, only respect an explicit YAML
	// `false` via the issues block's other knobs (none today touch
	// uniqByLine reading), and keep the upstream-matching default of
	// true. If a follow-up adds a *bool migration we can refine.
	_ = cfg.Issues.UniqByLine
	if f.generatedMode == "" {
		f.generatedMode = config.GeneratedModeStrict
	}

	for _, p := range exc.Paths {
		re, err := compilePathRegex(p)
		if err != nil {
			return nil, fmt.Errorf("linters.exclusions.paths: %w", err)
		}
		f.pathPatterns = append(f.pathPatterns, re)
	}
	for _, p := range exc.PathsExcept {
		re, err := compilePathRegex(p)
		if err != nil {
			return nil, fmt.Errorf("linters.exclusions.paths-except: %w", err)
		}
		f.pathExceptPatterns = append(f.pathExceptPatterns, re)
	}

	for i, r := range exc.Rules {
		cr, err := compileExcludeRule(&r)
		if err != nil {
			return nil, fmt.Errorf("linters.exclusions.rules[%d]: %w", i, err)
		}
		f.rules = append(f.rules, cr)
	}

	for _, p := range exc.Presets {
		for _, r := range presetRules(p) {
			cr, err := compileExcludeRule(&r)
			if err != nil {
				// Presets are compiled-in; a failure here is a bug.
				return nil, fmt.Errorf("linters.exclusions.presets[%s]: %w", p, err)
			}
			f.rules = append(f.rules, cr)
		}
	}

	return f, nil
}

// ConfigDigest returns a deterministic sha256 over the effective
// suppression configuration this Filter encodes: exclusion paths,
// paths-except, rules (linters + path/path-except/text/source regexes),
// generated-file mode, the staticcheck default-disabled set, the
// resolved target directories, the uniq-by-line flag, and whether the
// nolint filter is active.
//
// It exists to be folded into the L0 diagnostic cache key. The L0 cache
// stores the POST-filter per-package diagnostic stream, so a change to
// any suppression rule MUST invalidate those cached entries. Without
// this digest in the key, removing (or adding) an exclude-rule while
// leaving the package's source byte-identical replays diagnostics
// filtered under the OLD configuration — silently dropping findings
// that should now surface (e.g. a security-linter finding that resurfaces
// after a suspicious exclusion is deleted).
//
// basePath is intentionally excluded: it is a machine-local absolute
// path, and folding it in would break the cross-machine portability the
// rest of the L0 key is careful to preserve. Rule matching
// runs against module-relative paths, which are captured by targetDirs
// and the regex sources themselves.
//
// A nil receiver (no filter configured) returns the zero digest.
func (f *Filter) ConfigDigest() [32]byte {
	if f == nil {
		return [32]byte{}
	}
	h := sha256.New()
	var lenBuf [8]byte
	writeStr := func(s string) {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(s)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(s))
	}
	// regexp.Regexp.String() round-trips the source pattern, so two
	// filters compiled from the same config produce the same bytes.
	writeRe := func(re *regexp.Regexp) {
		if re == nil {
			writeStr("\x00<nil>")
			return
		}
		writeStr(re.String())
	}

	writeStr("paths")
	for _, re := range f.pathPatterns {
		writeRe(re)
	}
	writeStr("paths-except")
	for _, re := range f.pathExceptPatterns {
		writeRe(re)
	}
	writeStr("rules")
	for _, r := range f.rules {
		// linters is a set; sort for a stable digest.
		ls := make([]string, 0, len(r.linters))
		for l := range r.linters {
			ls = append(ls, l)
		}
		sort.Strings(ls)
		writeStr("linters")
		for _, l := range ls {
			writeStr(l)
		}
		writeStr("path")
		writeRe(r.path)
		writeStr("path-except")
		writeRe(r.pathExcept)
		writeStr("text")
		writeRe(r.text)
		writeStr("source")
		writeRe(r.source)
	}
	writeStr("generated")
	writeStr(f.generatedMode)
	writeStr("staticcheck-default-disabled")
	scKeys := make([]string, 0, len(f.staticcheckDefaultDisabled))
	for k := range f.staticcheckDefaultDisabled {
		scKeys = append(scKeys, k)
	}
	sort.Strings(scKeys)
	for _, k := range scKeys {
		writeStr(k)
	}
	writeStr("target-dirs")
	for _, td := range f.targetDirs {
		writeStr(td.dir)
		if td.recursive {
			writeStr("recursive")
		} else {
			writeStr("flat")
		}
	}
	writeStr("uniq-by-line")
	if f.uniqByLine {
		writeStr("true")
	} else {
		writeStr("false")
	}
	writeStr("nolint")
	if f.nolint != nil {
		writeStr("on")
	} else {
		writeStr("off")
	}

	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Apply rewrites diags in place, returning a new slice with excluded
// diagnostics removed. A nil receiver is a no-op pass-through (matches
// the contract callers want when no config-side exclusions are set).
func (f *Filter) Apply(diags []output.Diagnostic) []output.Diagnostic {
	if f == nil {
		return diags
	}
	if len(diags) == 0 {
		return diags
	}
	if len(f.pathPatterns) == 0 && len(f.pathExceptPatterns) == 0 &&
		len(f.rules) == 0 && f.generatedMode == config.GeneratedModeDisable &&
		len(f.staticcheckDefaultDisabled) == 0 && len(f.targetDirs) == 0 &&
		!f.uniqByLine && f.nolint == nil {
		return diags
	}

	out := make([]output.Diagnostic, 0, len(diags))
	// fileLineSeen tracks (file, line) tuples for uniq-by-line. Only
	// populated when uniqByLine is true.
	type fileLine struct {
		file string
		line int
	}
	seen := make(map[fileLine]struct{})
	for _, d := range diags {
		if f.dropDiagnostic(d) {
			continue
		}
		if f.uniqByLine {
			key := fileLine{file: d.Pos.Filename, line: d.Pos.Line}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, d)
	}
	return out
}

// ExcludesAllFiles reports whether every compiled file in a package is
// suppressed by a file-wide exclusion. It deliberately considers only target
// scope, global path rules, and generated-file detection. Per-issue rules,
// nolint directives, and diagnostic-specific filters require analyzer output
// and therefore cannot safely exclude an analysis root in advance.
func (f *Filter) ExcludesAllFiles(paths []string) bool {
	if f == nil || len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if !f.excludesFile(path) {
			return false
		}
	}
	return true
}

func (f *Filter) excludesFile(path string) bool {
	if path == "" {
		return false
	}
	rel := f.relativePath(path)
	if len(f.targetDirs) > 0 && !f.matchesTarget(rel) {
		return true
	}
	for _, re := range f.pathPatterns {
		if re.MatchString(rel) {
			return true
		}
	}
	if len(f.pathExceptPatterns) > 0 {
		matched := false
		for _, re := range f.pathExceptPatterns {
			if re.MatchString(rel) {
				matched = true
				break
			}
		}
		if !matched {
			return true
		}
	}
	return f.generatedMode != config.GeneratedModeDisable && f.isGenerated(path)
}

// dropDiagnostic reports whether d should be removed.
func (f *Filter) dropDiagnostic(d output.Diagnostic) bool {
	rel := f.relativePath(d.Pos.Filename)

	// -1. Target-path scope. When the CLI was invoked with positional
	// patterns, restrict diagnostics to files under those targets so
	// transitive in-module deps that plaid's workspace loader picked
	// up don't leak through as analysis-surface noise.
	if len(f.targetDirs) > 0 && !f.matchesTarget(rel) {
		return true
	}

	// -0.5. //nolint directives. Mirrors upstream's `nolint_filter`
	// processor — drops the diagnostic when its (file, line) falls
	// inside a //nolint[:list] range. Applied before generated-file
	// detection so a file marked generated still surfaces nolint-suppressed
	// diagnostics consistently with golangci-lint's pipeline ordering.
	if f.nolint != nil && f.nolint.suppresses(d) {
		return true
	}

	// 0. Staticcheck default-disabled checks. When the user does not
	// supply `linters.settings.staticcheck.checks`, golangci-lint
	// turns off ST1000/ST1003/ST1016/ST1020/ST1021/ST1022. Plaid
	// runs every analyzer regardless of selector, so we filter the
	// resulting diagnostics here.
	if _, ok := f.staticcheckDefaultDisabled[d.Linter]; ok {
		return true
	}

	// 0.1. Library-version-skew: drop diagnostics whose (linter,
	// message-substring) matches a known rule plaid emits because its
	// pinned upstream library version is newer than golangci-lint v2.9's.
	// Same shape as the gosec analyzerFilters (G124/G407/G702/G703) but
	// for libraries without a public include/exclude API. Currently
	// covers noctx's net/http/httptest.NewRequest rule (added in
	// v0.5.0; golangci v2.9 pins v0.4.0).
	if dropLibraryVersionSkew(d) {
		return true
	}

	// 1. Paths (drop if any matches)
	for _, re := range f.pathPatterns {
		if re.MatchString(rel) {
			return true
		}
	}
	// 2. Paths-except (keep only if at least one matches; when configured)
	if len(f.pathExceptPatterns) > 0 {
		any := false
		for _, re := range f.pathExceptPatterns {
			if re.MatchString(rel) {
				any = true
				break
			}
		}
		if !any {
			return true
		}
	}
	// 3. Generated file detection
	if f.generatedMode != config.GeneratedModeDisable && f.isGenerated(d.Pos.Filename) {
		return true
	}
	// 4. Per-issue rules (drop if any rule matches)
	for _, r := range f.rules {
		if r.match(d, rel) {
			return true
		}
	}
	return false
}

// relativePath returns d's filename relative to the filter's basePath, or
// the absolute path when filepath.Rel fails. Always forward-slashed for
// regex stability (mirrors fsutils.NormalizePathInRegex on unix).
func (f *Filter) relativePath(name string) string {
	if f.basePath == "" {
		return filepath.ToSlash(name)
	}
	if !filepath.IsAbs(name) {
		// Already relative — assume relative to basePath.
		return filepath.ToSlash(name)
	}
	rel, err := filepath.Rel(f.basePath, name)
	if err != nil {
		return filepath.ToSlash(name)
	}
	return filepath.ToSlash(rel)
}

// isGenerated reports whether the file at path is a generated source.
// Results are cached per Filter; the cache is concurrency-safe.
func (f *Filter) isGenerated(path string) bool {
	if path == "" {
		return false
	}
	if filepath.Base(path) == "go.mod" {
		return false
	}
	f.generatedCacheMu.Lock()
	v, ok := f.generatedCache[path]
	f.generatedCacheMu.Unlock()
	if ok {
		return v
	}
	gen := detectGenerated(path, f.generatedMode)
	f.generatedCacheMu.Lock()
	f.generatedCache[path] = gen
	f.generatedCacheMu.Unlock()
	return gen
}

// detectGenerated parses path's package clause + comments and applies
// the strict or lax marker rules. Errors degrade to "not generated"
// (a failed parse means the lint already errored elsewhere; we don't
// want to silently drop diagnostics).
func detectGenerated(path, mode string) bool {
	if mode == config.GeneratedModeDisable {
		return false
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false
	}
	if mode == config.GeneratedModeStrict {
		return file != nil && ast.IsGenerated(file)
	}
	return isGeneratedLax(file)
}

// Lax markers from golangci-lint v2: any of these substrings (lowercased)
// in a top-of-file comment marks the file as generated.
var laxMarkers = []string{
	"code generated",
	"do not edit",
	"autogenerated file",
	"* generated by: swagger codegen ",
}

func isGeneratedLax(file *ast.File) bool {
	if file == nil || len(file.Comments) == 0 {
		return false
	}
	var lines []string
	for _, c := range file.Comments {
		lines = append(lines, strings.TrimSpace(c.Text()))
	}
	doc := strings.ToLower(strings.Join(lines, "\n"))
	for _, m := range laxMarkers {
		if strings.Contains(doc, m) {
			return true
		}
	}
	return false
}

// compilePathRegex normalizes a YAML path pattern into a regex.
// On unix, fsutils.NormalizePathInRegex is a no-op; we mirror that by
// using the pattern verbatim. Patterns are matched as regular expressions
// against the relative-path slash-form, exactly like golangci-lint v2.
func compilePathRegex(p string) (*regexp.Regexp, error) {
	if p == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	return regexp.Compile(p)
}

// compileExcludeRule builds a compiledRule from a config.ExcludeRule.
func compileExcludeRule(r *config.ExcludeRule) (compiledRule, error) {
	var c compiledRule
	if len(r.Linters) > 0 {
		c.linters = make(map[string]struct{}, len(r.Linters))
		for _, l := range r.Linters {
			c.linters[l] = struct{}{}
		}
	}
	if r.Path != "" {
		re, err := compilePathRegex(r.Path)
		if err != nil {
			return c, fmt.Errorf("path: %w", err)
		}
		c.path = re
	}
	if r.PathExcept != "" {
		re, err := compilePathRegex(r.PathExcept)
		if err != nil {
			return c, fmt.Errorf("path-except: %w", err)
		}
		c.pathExcept = re
	}
	if r.Text != "" {
		re, err := regexp.Compile(r.Text)
		if err != nil {
			return c, fmt.Errorf("text: %w", err)
		}
		c.text = re
	}
	if r.Source != "" {
		re, err := regexp.Compile(r.Source)
		if err != nil {
			return c, fmt.Errorf("source: %w", err)
		}
		c.source = re
	}
	return c, nil
}

// match reports whether the rule applies to d. All non-nil fields must
// match (AND semantics, matching upstream).
//
// Note: source-line matching is not implemented; rules that set only
// `source:` will never fire. c1's config does not use source rules, so
// this is acceptable for parity on the c1 corpus.
func (r compiledRule) match(d output.Diagnostic, rel string) bool {
	if r.empty() {
		return false
	}
	if len(r.linters) > 0 {
		if !matchLinterName(r.linters, d.Linter) {
			return false
		}
	}
	if r.path != nil && !r.path.MatchString(rel) {
		return false
	}
	if r.pathExcept != nil && r.pathExcept.MatchString(rel) {
		return false
	}
	if r.text != nil && !r.text.MatchString(d.Message) {
		return false
	}
	if r.source != nil {
		// source-line matching requires reading the source file at
		// diag's line; not wired today. Conservative behavior: a rule
		// that *only* has source set never fires; a rule with source
		// plus other conditions fires when the others match (matching
		// the "AND" intent, modulo the source check).
		// To keep parity strict, treat a non-nil source as "does not
		// match" so the diag is not silently dropped on a stale assumption.
		return false
	}
	return true
}

func (r compiledRule) empty() bool {
	return len(r.linters) == 0 && r.path == nil && r.pathExcept == nil && r.text == nil && r.source == nil
}

// matchLinterName reports whether the diagnostic's emitting analyzer
// name matches any of the rule's linter selectors. Direct equality is
// the common case; the family alias map handles native fan-outs — both
// the staticcheck family (ST/SA/QF/S → "staticcheck") and the govet
// sub-analyzer set (copylocks/printf/... → "govet"). Without these
// aliases the `comments` preset wouldn't drop ST1000 / ST1020, and
// c1's `_test\.go` rule scoping `govet` wouldn't cover sub-analyzer
// diagnostics like copylocks.
func matchLinterName(selectors map[string]struct{}, diagLinter string) bool {
	if _, ok := selectors[diagLinter]; ok {
		return true
	}
	if family, ok := familyByPrefix(diagLinter); ok {
		if _, ok := selectors[family]; ok {
			return true
		}
	}
	return false
}

// matchesTarget reports whether the diagnostic's relative path is
// under one of the resolved CLI target directories. Forward-slashed
// rel is required (see relativePath).
func (f *Filter) matchesTarget(rel string) bool {
	for _, t := range f.targetDirs {
		if t.dir == "" || t.dir == "." {
			return true
		}
		if t.recursive {
			if rel == t.dir || strings.HasPrefix(rel, t.dir+"/") {
				return true
			}
		} else {
			// Non-recursive: only files directly in t.dir.
			if filepath.ToSlash(filepath.Dir(rel)) == t.dir {
				return true
			}
		}
	}
	return false
}

// resolveTargetDirs maps go/packages query patterns to filter-scope
// entries. Only the relative-path patterns supported by the CLI are
// handled (`./pkg/foo`, `./pkg/foo/...`, `pkg/foo/...`, bare directory
// names); import-path patterns and the special `./...` are recognized
// but produce a "match everything" entry.
//
// The empty result intentionally means "no scope filter": some tests
// and entrypoints invoke Run with nil patterns, and we don't want to
// flip the filter into "drop everything" mode for those.
func resolveTargetDirs(patterns []string) []targetDir {
	if len(patterns) == 0 {
		return nil
	}
	out := make([]targetDir, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// "./..." or "..." or "all" means everything under module root —
		// no scope restriction.
		if p == "./..." || p == "..." || p == "all" {
			return nil
		}
		// Strip a leading "./".
		p = strings.TrimPrefix(p, "./")
		recursive := strings.HasSuffix(p, "/...")
		if recursive {
			p = strings.TrimSuffix(p, "/...")
		} else if p == "..." {
			return nil
		}
		// Forward-slash normalization (Windows tolerance — comment-only
		// on Unix where it's a no-op).
		p = filepath.ToSlash(p)
		out = append(out, targetDir{dir: p, recursive: recursive})
	}
	return out
}

// staticcheckDefaultDisabledChecks is the set golangci-lint v2 turns
// off by default for the staticcheck linter when the user does not
// provide an explicit `staticcheck.checks` selector. Sourced from
// `pkg/golinters/staticcheck/staticcheck.go::createConfig.defaultChecks`.
//
// Each entry is a check ID (analyzer Name), not a regex.
var staticcheckDefaultDisabledChecks = []string{
	"ST1000", "ST1003", "ST1016", "ST1020", "ST1021", "ST1022",
}

// staticcheckDefaultDisabledSet returns the set of check IDs to drop.
// When the user has supplied `linters.settings.staticcheck.checks`,
// they have taken responsibility for the selector and we honor it
// (return nil); the registry path runs every analyzer because honnef's
// per-check selector evaluation happens internally, so the explicit
// list is informational here. The empty / unset case applies the
// default-disabled list to match golangci-lint.
func staticcheckDefaultDisabledSet(s *config.StaticCheckSettings) map[string]struct{} {
	if s != nil && len(s.Checks) > 0 {
		return nil
	}
	out := make(map[string]struct{}, len(staticcheckDefaultDisabledChecks))
	for _, c := range staticcheckDefaultDisabledChecks {
		out[c] = struct{}{}
	}
	return out
}

// familyByPrefix derives the umbrella linter name a per-check analyzer
// belongs to. Two families are fanned out as individual analyzers:
// the honnef.co/go/tools family (staticcheck / gosimple / stylecheck /
// quickfix) emits per-check names like SA####/ST####/S1###/QF####, and
// the x/tools govet sub-analyzers (copylocks, printf, ...) emit under
// their own short names. Everything else matches by direct equality.
//
// Diagnostics arrive with the analyzer's own name (e.g. "ST1000",
// "copylocks"), but `linters.exclusions.rules[*].linters`, v2 presets,
// and user `//nolint:govet` directives name the umbrella. This mapping
// bridges the gap. Without it, `//nolint:govet` does not suppress a
// copylocks finding, and the `_test\.go` path-rule scoping `govet` does
// not cover sub-analyzer diagnostics — surfacing diagnostics upstream
// silently drops.
func familyByPrefix(name string) (string, bool) {
	switch {
	case strings.HasPrefix(name, "SA"),
		strings.HasPrefix(name, "ST"),
		strings.HasPrefix(name, "S1"),
		strings.HasPrefix(name, "QF"):
		return "staticcheck", true
	}
	if _, ok := govetSubAnalyzers[name]; ok {
		return "govet", true
	}
	return "", false
}

// dropLibraryVersionSkew reports whether d is a known-skew diagnostic —
// a finding plaid emits because a pinned upstream library version is
// newer than golangci-lint v2.9's. Mirrors the gosec analyzerFilters
// for G124/G407/G702/G703 (those are dropped pre-analysis via gosec's
// own AnalyzerFilter API); this function covers libraries whose
// upstream has no public rule-include/exclude API.
//
// Currently:
//   - noctx: `net/http/httptest.NewRequest must not be called …` —
//     the httptest rule was added in github.com/sonatard/noctx v0.5.0;
//     golangci-lint v2.9 pins v0.4.0 which doesn't emit it.
//
// Add a new entry only when the upstream library version skew is the
// proven root cause. Do NOT use this to suppress diagnostics that
// represent real semantic differences from golangci-lint.
func dropLibraryVersionSkew(d output.Diagnostic) bool {
	if d.Linter == "noctx" && strings.HasPrefix(d.Message, "net/http/httptest.NewRequest must not be called") {
		return true
	}
	return false
}

// govetSubAnalyzers lists the x/tools sub-analyzer names that
// golangci-lint v2 attributes to the umbrella "govet" linter. Mirrors
// the set wired into the registry (internal/registry/wire_analyzers.go,
// govetSubAnalyzers var); kept here as a flat string set so the
// exclusion package doesn't depend on the registry. Adding a new
// govet sub-analyzer to the registry requires adding it here too — see
// the corpus test in internal/registry/corpus_test.go.
var govetSubAnalyzers = map[string]struct{}{
	"appends":             {},
	"asmdecl":             {},
	"assign":              {},
	"atomic":              {},
	"atomicalign":         {},
	"bools":               {},
	"buildtag":            {},
	"cgocall":             {},
	"composites":          {},
	"copylocks":           {},
	"deepequalerrors":     {},
	"defers":              {},
	"directive":           {},
	"errorsas":            {},
	"fieldalignment":      {},
	"findcall":            {},
	"framepointer":        {},
	"hostport":            {},
	"httpmux":             {},
	"httpresponse":        {},
	"ifaceassert":         {},
	"loopclosure":         {},
	"lostcancel":          {},
	"nilfunc":             {},
	"nilness":             {},
	"printf":              {},
	"reflectvaluecompare": {},
	"shadow":              {},
	"shift":               {},
	"sigchanyzer":         {},
	"slog":                {},
	"sortslice":           {},
	"stdmethods":          {},
	"stdversion":          {},
	"stringintconv":       {},
	"structtag":           {},
	"testinggoroutine":    {},
	"tests":               {},
	"timeformat":          {},
	"unmarshal":           {},
	"unreachable":         {},
	"unsafeptr":           {},
	"unusedresult":        {},
	"unusedwrite":         {},
	"usesgenerics":        {},
	"waitgroup":           {},
}
