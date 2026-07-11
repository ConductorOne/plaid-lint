// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/registry"
)

func TestAnalyzerSelectionPrunesUnselectedRoots(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = config.GroupNone
	cfg.Linters.Enable = []string{"staticcheck"}
	reg, _, err := registry.Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg, err = reg.SelectAnalyzers([]string{"SA1019"})
	if err != nil {
		t.Fatalf("SelectAnalyzers: %v", err)
	}

	plan := planFromRegistry(reg)
	if got, want := len(plan.analyzers), 1; got != want {
		t.Fatalf("plan analyzers = %d, want %d", got, want)
	}
	selected := plan.analyzers[0].analyzer
	if got, want := selected.Name, "SA1019"; got != want {
		t.Fatalf("selected analyzer = %q, want %q", got, want)
	}
	if analyzers.AnalyzerRequiresIR(selected) {
		t.Fatal("SA1019 unexpectedly requires IR")
	}
}
