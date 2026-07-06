// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
)

// Warning is a structured note emitted by the legacy migrator when a
// v1 key is rewritten into v2 canonical form. The CLI surfaces these
// as deprecation notices.
type Warning struct {
	// Field is the dotted v1 path being deprecated, e.g.
	// "run.skip-dirs" or "issues.exclude-rules".
	Field string

	// Message is a one-line human-readable description.
	Message string
}

// String formats a [Warning] for display.
func (w Warning) String() string {
	return fmt.Sprintf("[deprecated] %s: %s", w.Field, w.Message)
}

// legacyConfig captures v1-only keys that have no v2 home. Anything
// that lives at the same path in v1 and v2 (e.g. `run.timeout`) is
// decoded by the main [Config] struct directly; the legacy shim only
// holds keys we need to migrate.
//
// The legacy shim is decoded over the same YAML body as [Config]; the
// yaml decoder ignores unknown keys when KnownFields is false, so the
// two decodes never collide.
type legacyConfig struct {
	Run struct {
		SkipDirs           []string `yaml:"skip-dirs"`
		SkipFiles          []string `yaml:"skip-files"`
		SkipDirsUseDefault bool     `yaml:"skip-dirs-use-default"`
		ShowStats          bool     `yaml:"show-stats"`
	} `yaml:"run"`

	Linters struct {
		EnableAll  bool     `yaml:"enable-all"`
		DisableAll bool     `yaml:"disable-all"`
		Fast       bool     `yaml:"fast"`
		Presets    []string `yaml:"presets"`
	} `yaml:"linters"`

	// LintersSettings is v1's top-level path; v2 moves it under
	// `linters.settings`. We decode into a raw map so we can detect
	// presence (any non-empty mapping triggers the migration) and
	// route the data via a re-marshal/unmarshal cycle.
	LintersSettingsRaw map[string]any `yaml:"linters-settings"`

	Output struct {
		Format           string `yaml:"format"`
		PrintIssuedLines *bool  `yaml:"print-issued-lines"`
		PrintLinterName  *bool  `yaml:"print-linter-name"`
		SortResults      *bool  `yaml:"sort-results"`
		UniqByLine       *bool  `yaml:"uniq-by-line"`
	} `yaml:"output"`

	Issues struct {
		ExcludeRules           []legacyExcludeRule `yaml:"exclude-rules"`
		ExcludeFiles           []string            `yaml:"exclude-files"`
		ExcludeDirs            []string            `yaml:"exclude-dirs"`
		ExcludeGenerated       string              `yaml:"exclude-generated"`
		ExcludeGeneratedStrict *bool               `yaml:"exclude-generated-strict"`
		Exclude                []string            `yaml:"exclude"`
		ExcludeUseDefault      *bool               `yaml:"exclude-use-default"`
		ExcludeDirsUseDefault  *bool               `yaml:"exclude-dirs-use-default"`
		ExcludeCaseSensitive   *bool               `yaml:"exclude-case-sensitive"`
		Include                []string            `yaml:"include"`
	} `yaml:"issues"`

	Severity struct {
		DefaultSeverity string `yaml:"default-severity"`
		CaseSensitive   *bool  `yaml:"case-sensitive"`
	} `yaml:"severity"`
}

// legacyExcludeRule mirrors v1's `issues.exclude-rules[]` entry shape.
// The field set is identical to v2's ExcludeRule (BaseRule fields).
type legacyExcludeRule struct {
	Linters    []string `yaml:"linters"`
	Path       string   `yaml:"path"`
	PathExcept string   `yaml:"path-except"`
	Text       string   `yaml:"text"`
	Source     string   `yaml:"source"`
}

// hasLegacyKeys reports whether the legacy shim found any v1-only
// fields that warrant a migration pass. Used by [Load] to skip the
// migration when the input is a clean v2 file.
func (l *legacyConfig) hasLegacyKeys() bool {
	if len(l.Run.SkipDirs) > 0 || len(l.Run.SkipFiles) > 0 ||
		l.Run.SkipDirsUseDefault || l.Run.ShowStats {
		return true
	}
	if l.Linters.EnableAll || l.Linters.DisableAll || l.Linters.Fast || len(l.Linters.Presets) > 0 {
		return true
	}
	if len(l.LintersSettingsRaw) > 0 {
		return true
	}
	if l.Output.Format != "" || l.Output.PrintIssuedLines != nil ||
		l.Output.PrintLinterName != nil || l.Output.SortResults != nil ||
		l.Output.UniqByLine != nil {
		return true
	}
	if len(l.Issues.ExcludeRules) > 0 || len(l.Issues.ExcludeFiles) > 0 ||
		len(l.Issues.ExcludeDirs) > 0 || l.Issues.ExcludeGenerated != "" ||
		l.Issues.ExcludeGeneratedStrict != nil || len(l.Issues.Exclude) > 0 ||
		l.Issues.ExcludeUseDefault != nil || l.Issues.ExcludeDirsUseDefault != nil ||
		l.Issues.ExcludeCaseSensitive != nil || len(l.Issues.Include) > 0 {
		return true
	}
	if l.Severity.DefaultSeverity != "" || l.Severity.CaseSensitive != nil {
		return true
	}
	return false
}

// migrateLegacy folds v1-only fields from src into dst (the v2 Config).
// Returns the warnings the caller surfaces. dst is modified in place.
//
// The migration is intentionally one-way: keys that have no v2
// equivalent are dropped with a warning rather than preserved.
func migrateLegacy(dst *Config, src *legacyConfig, raw map[string]any) []Warning {
	var warnings []Warning
	add := func(field, msg string) {
		warnings = append(warnings, Warning{Field: field, Message: msg})
	}

	// run.skip-files / skip-dirs → linters.exclusions.paths
	for _, p := range src.Run.SkipFiles {
		if p == "" {
			continue
		}
		dst.Linters.Exclusions.Paths = append(dst.Linters.Exclusions.Paths, p)
	}
	if len(src.Run.SkipFiles) > 0 {
		add("run.skip-files", "moved to linters.exclusions.paths")
	}
	for _, p := range src.Run.SkipDirs {
		if p == "" {
			continue
		}
		dst.Linters.Exclusions.Paths = append(dst.Linters.Exclusions.Paths, p)
	}
	if len(src.Run.SkipDirs) > 0 {
		add("run.skip-dirs", "moved to linters.exclusions.paths")
	}
	if src.Run.SkipDirsUseDefault {
		add("run.skip-dirs-use-default", "removed in v2; built-in dir excludes no longer exist (set linters.exclusions.presets: [legacy] for closest equivalent)")
	}
	if src.Run.ShowStats {
		dst.Output.ShowStats = true
		add("run.show-stats", "moved to output.show-stats")
	}

	// linters.{enable-all,disable-all,fast} → linters.default
	switch {
	case src.Linters.EnableAll:
		dst.Linters.Default = GroupAll
		add("linters.enable-all", "use linters.default: all")
	case src.Linters.DisableAll:
		dst.Linters.Default = GroupNone
		add("linters.disable-all", "use linters.default: none")
	case src.Linters.Fast:
		dst.Linters.Default = GroupFast
		add("linters.fast", "use linters.default: fast")
	}
	if len(src.Linters.Presets) > 0 {
		add("linters.presets", "removed in v2; the closest replacement is linters.exclusions.presets (different semantics)")
	}

	// v1 listed formatters under `linters.enable`; v2 splits them into
	// `formatters.enable`. Move any known formatter names over and
	// strip them from linters.enable.
	migrateFormatterEnables(dst, &add)

	// top-level linters-settings → linters.settings (re-decode through YAML).
	if len(src.LintersSettingsRaw) > 0 {
		if err := remarshalInto(src.LintersSettingsRaw, &dst.Linters.Settings); err == nil {
			add("linters-settings", "moved to linters.settings")
		} else {
			add("linters-settings", fmt.Sprintf("could not migrate (%v); structured keys lost", err))
		}
	}

	// output.format (v1 single string) → output.formats.<name>.path.
	if src.Output.Format != "" {
		name, path := parseV1Format(src.Output.Format)
		applyV1Format(&dst.Output.Formats, name, path)
		add("output.format", "moved to output.formats.<name>.path (one named printer per format)")
	}
	if src.Output.PrintIssuedLines != nil {
		dst.Output.Formats.Text.PrintIssuedLine = *src.Output.PrintIssuedLines
		add("output.print-issued-lines", "moved to output.formats.text.print-issued-lines")
	}
	if src.Output.PrintLinterName != nil {
		dst.Output.Formats.Text.PrintLinterName = *src.Output.PrintLinterName
		dst.Output.Formats.Tab.PrintLinterName = *src.Output.PrintLinterName
		add("output.print-linter-name", "moved to output.formats.{text,tab}.print-linter-name")
	}
	if src.Output.SortResults != nil {
		add("output.sort-results", "removed in v2 (output is always sorted)")
	}
	if src.Output.UniqByLine != nil {
		dst.Issues.UniqByLine = *src.Output.UniqByLine
		add("output.uniq-by-line", "moved to issues.uniq-by-line")
	}

	// issues.exclude-rules → linters.exclusions.rules. The legacy
	// shape is intentionally a structural twin of BaseRule so this
	// migration is a direct conversion, not a field-by-field copy.
	for _, r := range src.Issues.ExcludeRules {
		dst.Linters.Exclusions.Rules = append(dst.Linters.Exclusions.Rules, ExcludeRule{
			BaseRule: BaseRule(r),
		})
	}
	if len(src.Issues.ExcludeRules) > 0 {
		add("issues.exclude-rules", "moved to linters.exclusions.rules")
	}

	// issues.exclude-files / exclude-dirs → linters.exclusions.paths
	for _, p := range src.Issues.ExcludeFiles {
		if p == "" {
			continue
		}
		dst.Linters.Exclusions.Paths = append(dst.Linters.Exclusions.Paths, p)
	}
	if len(src.Issues.ExcludeFiles) > 0 {
		add("issues.exclude-files", "moved to linters.exclusions.paths")
	}
	for _, p := range src.Issues.ExcludeDirs {
		if p == "" {
			continue
		}
		dst.Linters.Exclusions.Paths = append(dst.Linters.Exclusions.Paths, p)
	}
	if len(src.Issues.ExcludeDirs) > 0 {
		add("issues.exclude-dirs", "moved to linters.exclusions.paths")
	}

	// issues.exclude-generated (v1 string) → linters.exclusions.generated.
	if g := normalizeV1Generated(src.Issues.ExcludeGenerated); g != "" {
		dst.Linters.Exclusions.Generated = g
		add("issues.exclude-generated", "moved to linters.exclusions.generated (none|default|strict → disable|lax|strict)")
	}
	if src.Issues.ExcludeGeneratedStrict != nil && *src.Issues.ExcludeGeneratedStrict {
		dst.Linters.Exclusions.Generated = GeneratedModeStrict
		add("issues.exclude-generated-strict", "moved to linters.exclusions.generated: strict")
	}

	// issues.exclude → synthetic exclude rules.
	for _, txt := range src.Issues.Exclude {
		if txt == "" {
			continue
		}
		dst.Linters.Exclusions.Rules = append(dst.Linters.Exclusions.Rules, ExcludeRule{
			BaseRule: BaseRule{
				Text: txt,
				// Wide net: no `linters` filter == all linters.
				// Pair with a path to satisfy the 2-condition rule;
				// otherwise v2 validation would reject it. Users who
				// want a true "text-only" rule must add a path filter
				// after migration. We err on permissive (`.*`).
				Path: ".*",
			},
		})
	}
	if len(src.Issues.Exclude) > 0 {
		add("issues.exclude", "wrapped into linters.exclusions.rules (each entry becomes a {text, path: .*} rule)")
	}

	// issues.exclude-use-default → linters.exclusions.presets: [legacy]
	if src.Issues.ExcludeUseDefault != nil && *src.Issues.ExcludeUseDefault {
		hasLegacy := false
		for _, p := range dst.Linters.Exclusions.Presets {
			if p == ExclusionPresetLegacy {
				hasLegacy = true
				break
			}
		}
		if !hasLegacy {
			dst.Linters.Exclusions.Presets = append(dst.Linters.Exclusions.Presets, ExclusionPresetLegacy)
		}
		add("issues.exclude-use-default", "add 'legacy' to linters.exclusions.presets")
	}
	if src.Issues.ExcludeDirsUseDefault != nil {
		add("issues.exclude-dirs-use-default", "removed in v2; no equivalent")
	}
	if src.Issues.ExcludeCaseSensitive != nil {
		add("issues.exclude-case-sensitive", "removed in v2 (always case-sensitive)")
	}
	if len(src.Issues.Include) > 0 {
		add("issues.include", "removed in v2; use linters.exclusions.{presets, rules} to re-enable specific defaults")
	}

	// severity.default-severity → severity.default
	if src.Severity.DefaultSeverity != "" && dst.Severity.Default == "" {
		dst.Severity.Default = src.Severity.DefaultSeverity
		add("severity.default-severity", "moved to severity.default")
	}
	if src.Severity.CaseSensitive != nil {
		add("severity.case-sensitive", "removed in v2")
	}

	_ = raw // reserved for future use (unknown-key detection)
	return warnings
}

// migrateFormatterEnables moves formatter names from
// dst.Linters.Enable to dst.Formatters.Enable. v1 listed gofmt /
// goimports / gci / gofumpt / golines / swaggo under `linters.enable`;
// v2 requires them under `formatters.enable`. We also relocate the
// per-linter settings (which v1 nested under linters-settings.X) into
// formatters.settings.X — that fan-out already happened via the
// remarshal pass when linters-settings was migrated, but the Settings
// struct shares fields between Formatters.Settings and any
// formatter-named linter settings stuffed into LintersSettings under
// the same name (there is no such field, since formatters live in
// FormatterSettings only). For now the path is one-way: extract names
// and emit one warning.
func migrateFormatterEnables(dst *Config, add *func(field, msg string)) {
	if len(dst.Linters.Enable) == 0 {
		return
	}
	keep := dst.Linters.Enable[:0:0]
	moved := false
	for _, name := range dst.Linters.Enable {
		isFormatter := false
		for _, f := range allFormatterNames {
			if name == f {
				isFormatter = true
				break
			}
		}
		if isFormatter {
			dst.Formatters.Enable = appendIfMissing(dst.Formatters.Enable, name)
			moved = true
			continue
		}
		keep = append(keep, name)
	}
	dst.Linters.Enable = keep
	if moved {
		(*add)("linters.enable", "formatters (gci/gofmt/gofumpt/goimports/golines/swaggo) moved from linters.enable to formatters.enable")
	}
}

// appendIfMissing appends s to ss when not already present. Preserves
// order. Used by the formatter-enable migration.
func appendIfMissing(ss []string, s string) []string {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(ss, s)
}

// normalizeV1Generated maps v1's exclude-generated values to v2's
// linters.exclusions.generated. Per the v2 migration guide:
//
//	v1 "none"    → v2 "disable"
//	v1 "default" → v2 "strict" (v2 default is strict)
//	v1 "strict"  → v2 "strict"
//
// Empty input returns "" so the caller can leave dst alone.
func normalizeV1Generated(v string) string {
	switch v {
	case "":
		return ""
	case "none", "disable":
		return GeneratedModeDisable
	case "lax":
		return GeneratedModeLax
	case "default", "strict":
		return GeneratedModeStrict
	default:
		// Pass through unknown values; Validate will reject.
		return v
	}
}

// parseV1Format splits a v1 `format` string of the form `<name>` or
// `<name>:<path>` into its parts. Multiple `,`-separated values aren't
// supported here — the bench's existing exclude loader doesn't see
// them, and they're rare in real configs.
func parseV1Format(v string) (name, path string) {
	for i := 0; i < len(v); i++ {
		if v[i] == ':' {
			return v[:i], v[i+1:]
		}
	}
	return v, ""
}

// applyV1Format routes a (name, path) pair from a v1 `output.format`
// declaration into the matching v2 [Formats] entry. Unknown names are
// dropped with no error — Validate doesn't check format names, and the
// printer registry (T2.2) will surface the misconfiguration.
func applyV1Format(f *Formats, name, path string) {
	switch name {
	case "", "colored-line-number", "line-number", "tab", "colored-tab":
		// v1 grouped a few names under one printer; for the parser's
		// purposes we collapse all "text-shaped" names onto Text.
		if name == "tab" || name == "colored-tab" {
			f.Tab.Path = path
			if name == "colored-tab" {
				f.Tab.Colors = true
			}
			return
		}
		f.Text.Path = path
		if name == "colored-line-number" {
			f.Text.Colors = true
		}
	case "json":
		f.JSON.Path = path
	case "html":
		f.HTML.Path = path
	case "checkstyle":
		f.Checkstyle.Path = path
	case "code-climate":
		f.CodeClimate.Path = path
	case "junit-xml":
		f.JUnitXML.Path = path
	case "teamcity":
		f.TeamCity.Path = path
	case "sarif":
		f.Sarif.Path = path
	default:
		// Unknown format name. The v2 schema also won't accept it;
		// dropping it here matches upstream's "unknown printer name"
		// behavior at CLI time.
	}
}
