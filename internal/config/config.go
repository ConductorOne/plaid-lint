// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package config parses .golangci.yml / .yaml / .toml / .json into a
// canonical [Config] mirroring upstream golangci-lint v2's schema.
//
// Two callers exist for this package:
//
//   - The production CLI (T2.4 — not yet wired) walks up from the
//     working directory to find a config file, calls [LoadDirs], and
//     hands the resulting [Config] to the engine.
//   - The bench harness's existing minimal exclude parser
//     (internal/bench/exclude.go) is unaffected by this package and
//     will be replaced by the linter-registry plumbing in T2.3.
//
// v1 schemas (golangci-lint pre-v2) are accepted and downconverted to
// canonical v2 form. Each downconversion records a [Warning] so the
// CLI can surface deprecation notices. This is a deliberate divergence
// from upstream — upstream v2 rejects v1 outright.
//
// See internal/config/SCHEMA.md for the full field map and the v1→v2
// migration rules.
package config

// Config encapsulates the parsed shape of a .golangci.{yml,yaml,toml,json}
// file. Field shape mirrors golangci-lint v2 master at commit 72798d3
// (see SCHEMA.md for details).
type Config struct {
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	Run Run `yaml:"run,omitempty" json:"run,omitempty"`

	Output Output `yaml:"output,omitempty" json:"output,omitempty"`

	Linters Linters `yaml:"linters,omitempty" json:"linters,omitempty"`

	Issues   Issues   `yaml:"issues,omitempty" json:"issues,omitempty"`
	Severity Severity `yaml:"severity,omitempty" json:"severity,omitempty"`

	Formatters Formatters `yaml:"formatters,omitempty" json:"formatters,omitempty"`

	// cfgDir is the absolute path to the directory containing the loaded
	// config file. Populated by [Load]/[LoadDirs]; empty otherwise.
	cfgDir string
	// sourcePath is the absolute path to the loaded config file.
	sourcePath string
}

// GetConfigDir returns the absolute directory of the loaded config
// file, or "" when [Config] was constructed without a file (e.g.
// programmatically or via stdin).
func (c *Config) GetConfigDir() string {
	if c == nil {
		return ""
	}
	return c.cfgDir
}

// SetConfigDir sets the directory the config file was loaded from. The
// CLI uses this to anchor relative paths in v2 `relative-path-mode`.
func (c *Config) SetConfigDir(dir string) {
	c.cfgDir = dir
}

// SourcePath returns the absolute path to the loaded config file, or
// "" when no file was loaded.
func (c *Config) SourcePath() string {
	if c == nil {
		return ""
	}
	return c.sourcePath
}

// NewDefault returns a [Config] populated with golangci-lint's compiled-in
// defaults for those fields that have non-zero defaults. Mirrors upstream's
// `NewDefault` + post-load default-injection in `Loader.Load`.
//
// Defaults populated here intentionally mirror upstream's
// `defaultLintersSettings` for linters whose zero-value behavior
// diverges from golangci-lint's compiled-in defaults (e.g. goconst's
// `min-occurrences` = 3, vs. the library's 0 which reports every
// string). Add new linter defaults to [applyLinterSettingsDefaults]
// rather than scattering them across the codebase.
func NewDefault() *Config {
	c := &Config{}
	c.Linters.Exclusions.Generated = GeneratedModeStrict
	applyLinterSettingsDefaults(&c.Linters.Settings)
	return c
}

// applyLinterSettingsDefaults sets the non-zero compiled-in defaults
// that upstream's `defaultLintersSettings` carries. Only fields that
// remain at their zero value are touched, so an explicit YAML setting
// always wins.
//
// Mirrors `golangci-lint v2.9` `pkg/config/linters_settings.go`
// (`defaultLintersSettings`). Keep these in sync as the upstream
// defaults shift.
func applyLinterSettingsDefaults(s *LintersSettings) {
	// goconst: the library's defaults (0/0/0) cause every single string
	// to fire as "has 1 occurrences". Upstream filters this away with
	// MinStringLen=3, MinOccurrencesCount=3, IgnoreCalls=true,
	// MatchWithConstants=true, NumberMin=3, NumberMax=3.
	if s.Goconst.MinStringLen == 0 {
		s.Goconst.MinStringLen = 3
	}
	if s.Goconst.MinOccurrencesCount == 0 {
		s.Goconst.MinOccurrencesCount = 3
	}
	if s.Goconst.NumberMin == 0 {
		s.Goconst.NumberMin = 3
	}
	if s.Goconst.NumberMax == 0 {
		s.Goconst.NumberMax = 3
	}
	// Bool zero-values can't tell "user set false" from "unset", so we
	// only set true when no other goconst field is non-default. This
	// matches what users actually mean: a YAML block that touches any
	// goconst key is treated as authoritative for the bool toggles too.
	if !s.Goconst.MatchWithConstants && !s.Goconst.IgnoreCalls &&
		!s.Goconst.ParseNumbers && !s.Goconst.FindDuplicates &&
		!s.Goconst.EvalConstExpressions {
		s.Goconst.MatchWithConstants = true
		s.Goconst.IgnoreCalls = true
	}

	// Unused: honnef.co/go/tools/unused's library zero-values flag every
	// parameter, local, write-only field, and (when not exported)
	// declaration as "unused" because its Options struct defaults to
	// off-everywhere. Upstream golangci-lint compensates by setting
	// FieldWritesAreUses=true, ExportedFieldsAreUsed=true,
	// ParametersAreUsed=true, LocalVariablesAreUsed=true,
	// GeneratedIsUsed=true. Without these the native port emits
	// ~5k unused diagnostics on c1's pkg/controller/... where the same
	// config under golangci-lint v2.9 emits 0. Same bool-toggle
	// authoritative-block detection as goconst above: only inject when
	// every key is at its zero value, so an explicit YAML block wins.
	if !s.Unused.FieldWritesAreUses && !s.Unused.PostStatementsAreReads &&
		!s.Unused.ExportedFieldsAreUsed && !s.Unused.ParametersAreUsed &&
		!s.Unused.LocalVariablesAreUsed && !s.Unused.GeneratedIsUsed {
		s.Unused.FieldWritesAreUses = true
		s.Unused.ExportedFieldsAreUsed = true
		s.Unused.ParametersAreUsed = true
		s.Unused.LocalVariablesAreUsed = true
		s.Unused.GeneratedIsUsed = true
	}
}
