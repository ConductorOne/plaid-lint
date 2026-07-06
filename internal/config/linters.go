// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
	"slices"
)

// Default-group constants for `linters.default`. v1's
// enable-all/disable-all/fast collapse into these v2 names.
const (
	GroupStandard = "standard"
	GroupAll      = "all"
	GroupNone     = "none"
	GroupFast     = "fast"
)

// Linters mirrors v2's `linters:` block.
//
// v1 callers see the following migrations applied during decode:
//   - `linters.enable-all: true`  → `default: all`
//   - `linters.disable-all: true` → `default: none`
//   - `linters.fast: true`        → `default: fast`
//   - top-level `linters-settings:` → `linters.settings`
type Linters struct {
	Default string `yaml:"default,omitempty" json:"default,omitempty"`

	Enable  []string `yaml:"enable,omitempty" json:"enable,omitempty"`
	Disable []string `yaml:"disable,omitempty" json:"disable,omitempty"`

	// FastOnly is a CLI-only flag (`--fast-only`); never set from YAML.
	FastOnly bool `yaml:"fast-only,omitempty" json:"fast-only,omitempty"`

	Settings LintersSettings `yaml:"settings,omitempty" json:"settings,omitempty"`

	Exclusions LinterExclusions `yaml:"exclusions,omitempty" json:"exclusions,omitempty"`
}

// Validate checks the linters block. It does NOT verify that the
// names in Enable/Disable refer to real linters — that's the
// registry's job (T2.3).
func (l *Linters) Validate() error {
	if err := l.Exclusions.Validate(); err != nil {
		return err
	}
	return l.validateNoFormatters()
}

// validateNoFormatters rejects formatter names appearing under
// `linters.enable` / `linters.disable`. Upstream surfaces this with
// the exact message plaid-lint mirrors.
func (l *Linters) validateNoFormatters() error {
	for _, n := range slices.Concat(l.Enable, l.Disable) {
		if slices.Contains(allFormatterNames, n) {
			return fmt.Errorf("linters: %s is a formatter (move it to formatters.enable)", n)
		}
	}
	return nil
}

// Generated-code exclusion mode constants for [LinterExclusions.Generated].
const (
	GeneratedModeLax     = "lax"
	GeneratedModeStrict  = "strict"
	GeneratedModeDisable = "disable"
)

// Built-in v2 exclusion preset names for [LinterExclusions.Presets].
const (
	ExclusionPresetComments             = "comments"
	ExclusionPresetStdErrorHandling     = "std-error-handling"
	ExclusionPresetCommonFalsePositives = "common-false-positives"
	ExclusionPresetLegacy               = "legacy"
)

// LinterExclusions mirrors v2's `linters.exclusions:` block. The v1
// migration writes `issues.exclude-*` keys here.
type LinterExclusions struct {
	Generated   string        `yaml:"generated,omitempty" json:"generated,omitempty"`
	WarnUnused  bool          `yaml:"warn-unused,omitempty" json:"warn-unused,omitempty"`
	Presets     []string      `yaml:"presets,omitempty" json:"presets,omitempty"`
	Rules       []ExcludeRule `yaml:"rules,omitempty" json:"rules,omitempty"`
	Paths       []string      `yaml:"paths,omitempty" json:"paths,omitempty"`
	PathsExcept []string      `yaml:"paths-except,omitempty" json:"paths-except,omitempty"`
}

// Validate checks the generated mode + preset names and recurses into
// each rule.
func (e *LinterExclusions) Validate() error {
	switch e.Generated {
	case "", GeneratedModeLax, GeneratedModeStrict, GeneratedModeDisable:
		// valid
	default:
		return fmt.Errorf("linters.exclusions.generated: %q is not one of (lax|strict|disable)", e.Generated)
	}

	allPresets := []string{
		ExclusionPresetComments,
		ExclusionPresetStdErrorHandling,
		ExclusionPresetCommonFalsePositives,
		ExclusionPresetLegacy,
	}
	for _, p := range e.Presets {
		if !slices.Contains(allPresets, p) {
			return fmt.Errorf("linters.exclusions.presets: invalid preset %q", p)
		}
	}

	for i, rule := range e.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("linters.exclusions.rules[%d]: %w", i, err)
		}
	}
	return nil
}

// allFormatterNames is the closed list of names that go under
// `formatters.enable`. Mirrors upstream's hard-coded list.
var allFormatterNames = []string{
	"gci",
	"gofmt",
	"gofumpt",
	"goimports",
	"golines",
	"swaggo",
}
