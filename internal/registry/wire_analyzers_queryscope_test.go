// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

func TestQueryscopeWiring(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"queryscope"}
	cfg.Linters.Settings.QueryScope.UnscopedMethods = []string{"CountStar"}
	cfg.Linters.Settings.QueryScope.IgnoreDirective = "queryscope:ignore"

	registry, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, resolved := range registry.Enabled() {
		if resolved.Name != "queryscope" {
			continue
		}
		if resolved.Analyzer == nil {
			t.Fatal("queryscope Analyzer is nil")
		}
		if _, ok := resolved.Settings.(*config.QueryScope); !ok {
			t.Fatalf("Settings type = %T, want *config.QueryScope", resolved.Settings)
		}
		return
	}
	t.Fatal("queryscope missing from enabled linters")
}
