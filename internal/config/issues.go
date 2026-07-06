// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

// Issues mirrors v2's `issues:` block.
//
// v1 callers see the following migrations applied during decode (handled
// by the v1 shim and folded into Config; see SCHEMA.md):
//   - `issues.exclude-rules`           → `linters.exclusions.rules`
//   - `issues.exclude-files`           → `linters.exclusions.paths`
//   - `issues.exclude-dirs`            → `linters.exclusions.paths`
//   - `issues.exclude-generated`       → `linters.exclusions.generated`
//   - `issues.exclude-generated-strict`→ forces `generated: strict`
//   - `issues.exclude`                 → synthetic [ExcludeRule] entries
//   - `issues.exclude-use-default`     → adds `presets: [legacy]`
type Issues struct {
	MaxIssuesPerLinter int  `yaml:"max-issues-per-linter,omitempty" json:"max-issues-per-linter,omitempty"`
	MaxSameIssues      int  `yaml:"max-same-issues,omitempty" json:"max-same-issues,omitempty"`
	UniqByLine         bool `yaml:"uniq-by-line,omitempty" json:"uniq-by-line,omitempty"`

	DiffFromRevision  string `yaml:"new-from-rev,omitempty" json:"new-from-rev,omitempty"`
	DiffFromMergeBase string `yaml:"new-from-merge-base,omitempty" json:"new-from-merge-base,omitempty"`
	DiffPatchFilePath string `yaml:"new-from-patch,omitempty" json:"new-from-patch,omitempty"`
	WholeFiles        bool   `yaml:"whole-files,omitempty" json:"whole-files,omitempty"`
	Diff              bool   `yaml:"new,omitempty" json:"new,omitempty"`

	NeedFix bool `yaml:"fix,omitempty" json:"fix,omitempty"`
}
