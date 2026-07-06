// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Duration wraps time.Duration with YAML/JSON unmarshalers that accept
// both string forms (`"5m"`, `"1h30m"`) and raw nanosecond integers.
// Mirrors upstream's `mapstructure.StringToTimeDurationHookFunc()`.
type Duration time.Duration

// AsDuration returns the underlying time.Duration value.
func (d Duration) AsDuration() time.Duration { return time.Duration(d) }

// String formats the duration in time.Duration's canonical form.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts both `5m` and `300000000000` (ns).
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var asString string
	if err := unmarshal(&asString); err == nil {
		parsed, perr := time.ParseDuration(asString)
		if perr != nil {
			return fmt.Errorf("run.timeout: %w", perr)
		}
		*d = Duration(parsed)
		return nil
	}
	var asInt int64
	if err := unmarshal(&asInt); err == nil {
		*d = Duration(asInt)
		return nil
	}
	return fmt.Errorf("run.timeout: must be a duration string (e.g. \"5m\") or nanoseconds")
}

// UnmarshalJSON accepts both `"5m"` strings and raw numeric ns values.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("run.timeout: %w", err)
		}
		parsed, perr := time.ParseDuration(s)
		if perr != nil {
			return fmt.Errorf("run.timeout: %w", perr)
		}
		*d = Duration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("run.timeout: must be a duration string or nanoseconds: %w", err)
	}
	*d = Duration(n)
	return nil
}

// MarshalYAML emits the canonical string form. Implementing both
// halves keeps round-trips stable.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// MarshalJSON emits the canonical string form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Run mirrors golangci-lint v2's `run:` block. Field rename map vs
// upstream's mapstructure tags is in SCHEMA.md.
type Run struct {
	Timeout Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`

	Concurrency int `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`

	Go string `yaml:"go,omitempty" json:"go,omitempty"`

	RelativePathMode string `yaml:"relative-path-mode,omitempty" json:"relative-path-mode,omitempty"`

	BuildTags           []string `yaml:"build-tags,omitempty" json:"build-tags,omitempty"`
	ModulesDownloadMode string   `yaml:"modules-download-mode,omitempty" json:"modules-download-mode,omitempty"`
	EnableBuildVCS      bool     `yaml:"enable-build-vcs,omitempty" json:"enable-build-vcs,omitempty"`

	ExitCodeIfIssuesFound int `yaml:"issues-exit-code,omitempty" json:"issues-exit-code,omitempty"`

	// AnalyzeTests mirrors golangci-lint v2's `run.tests`. A nil
	// pointer selects the upstream default of true (test variants
	// reach the analyzer pass so unused / staticcheck / etc. see
	// `_test.go` files). Set to &false to opt out.
	AnalyzeTests *bool `yaml:"tests,omitempty" json:"tests,omitempty"`

	AllowParallelRunners bool `yaml:"allow-parallel-runners,omitempty" json:"allow-parallel-runners,omitempty"`
	AllowSerialRunners   bool `yaml:"allow-serial-runners,omitempty" json:"allow-serial-runners,omitempty"`
}

// Validate checks the run block for invalid enum values. Empty values
// are accepted (zero-value-safe).
func (r *Run) Validate() error {
	allowedModes := []string{"mod", "readonly", "vendor"}
	if r.ModulesDownloadMode != "" && !slices.Contains(allowedModes, r.ModulesDownloadMode) {
		return fmt.Errorf("run.modules-download-mode: %q is not one of (%s)",
			r.ModulesDownloadMode, strings.Join(allowedModes, "|"))
	}

	allowedRelative := []string{"wd", "cfg", "gomod", "gitroot"}
	if r.RelativePathMode != "" && !slices.Contains(allowedRelative, r.RelativePathMode) {
		return fmt.Errorf("run.relative-path-mode: %q is not one of (%s)",
			r.RelativePathMode, strings.Join(allowedRelative, "|"))
	}

	return nil
}
