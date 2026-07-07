// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"strings"
	"testing"
)

// TestMergeLinters_Enable exercises the enable-list merge semantics.
//
// The additive path (`--enable=X` with default standard/all/unset)
// appends X to the base/file-config enabled linters. The exclusive
// path (`--default=none`, produced by `--default=none` or the
// `--enable-only` flag) must instead REPLACE the base enable list so
// the base/file-config linters do not leak through.
func TestMergeLinters_Enable(t *testing.T) {
	// A representative file-config enable list that a real project
	// would carry alongside `default: standard`.
	fileEnable := []string{"bodyclose", "errorlint", "gocritic", "gosec", "misspell", "revive"}

	tests := []struct {
		name        string
		baseDefault string
		baseEnable  []string
		ovlDefault  string
		ovlEnable   []string
		wantDefault string
		wantEnable  []string
	}{
		{
			name:        "enable-only single replaces file config",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  GroupNone, // --enable-only sets default=none
			ovlEnable:   []string{"staticcheck"},
			wantDefault: GroupNone,
			wantEnable:  []string{"staticcheck"},
		},
		{
			name:        "enable-only multiple replaces file config",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  GroupNone,
			ovlEnable:   []string{"bodyclose", "gosec"},
			wantDefault: GroupNone,
			wantEnable:  []string{"bodyclose", "gosec"},
		},
		{
			name:        "default=none with enable replaces file config",
			baseDefault: GroupAll,
			baseEnable:  fileEnable,
			ovlDefault:  GroupNone,
			ovlEnable:   []string{"staticcheck"},
			wantDefault: GroupNone,
			wantEnable:  []string{"staticcheck"},
		},
		{
			name:        "default=none without enable clears file config",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  GroupNone,
			ovlEnable:   nil,
			wantDefault: GroupNone,
			wantEnable:  nil,
		},
		{
			name:        "additive enable keeps file config (regression guard)",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  "", // plain --enable=extra: no default override
			ovlEnable:   []string{"unconvert"},
			wantDefault: GroupStandard,
			wantEnable:  append(append([]string{}, fileEnable...), "unconvert"),
		},
		{
			name:        "additive enable with default=all keeps file config",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  GroupAll,
			ovlEnable:   []string{"unconvert"},
			wantDefault: GroupAll,
			wantEnable:  append(append([]string{}, fileEnable...), "unconvert"),
		},
		{
			name:        "additive enable dedups against file config",
			baseDefault: GroupStandard,
			baseEnable:  fileEnable,
			ovlDefault:  "",
			ovlEnable:   []string{"gosec", "unconvert"}, // gosec already in base
			wantDefault: GroupStandard,
			wantEnable:  append(append([]string{}, fileEnable...), "unconvert"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := Linters{Default: tt.baseDefault, Enable: tt.baseEnable}
			overlay := Linters{Default: tt.ovlDefault, Enable: tt.ovlEnable}
			got := mergeLinters(base, overlay)

			if got.Default != tt.wantDefault {
				t.Errorf("Default = %q, want %q", got.Default, tt.wantDefault)
			}
			if !equalStrings(got.Enable, tt.wantEnable) {
				t.Errorf("Enable = %v, want %v", got.Enable, tt.wantEnable)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMergeLinters_EnableOnly_NoBaseLeak is a focused end-to-end
// assertion on the reported bug: `--enable-only staticcheck` layered
// over a file config that enables many linters yields exactly
// `{staticcheck}` — no base linter leaks through.
func TestMergeLinters_EnableOnly_NoBaseLeak(t *testing.T) {
	base := &Config{}
	base.Linters.Default = GroupStandard
	base.Linters.Enable = []string{"bodyclose", "errorlint", "gocritic", "gosec", "misspell", "revive"}

	// Overlay mirrors what applyOverlay() builds for `--enable-only staticcheck`.
	overlay := &Config{}
	overlay.Linters.Default = GroupNone
	overlay.Linters.Enable = []string{"staticcheck"}

	out := Merge(base, overlay)

	if out.Linters.Default != GroupNone {
		t.Fatalf("Default = %q, want %q", out.Linters.Default, GroupNone)
	}
	if got := strings.Join(out.Linters.Enable, ","); got != "staticcheck" {
		t.Fatalf("Enable = %q, want %q", got, "staticcheck")
	}
}
