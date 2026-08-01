// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// TestHermeticSkip pins the refusal semantics for linters that cannot
// honor the declared-inputs contract: they are dropped from the root
// set with a loud warning, never run silently degraded.
func TestHermeticSkip(t *testing.T) {
	cfg, _, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - gomodguard
    - depguard
    - errcheck
  settings:
    depguard:
      rules:
        main:
          deny:
            - pkg: "$gostd"
              desc: "no std"
`), ".yml")
	if err != nil {
		t.Fatal(err)
	}
	reg, _, err := registry.BuildFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	roots, notes := selectRoots(reg, cfg, ModeFull)

	for _, r := range roots {
		if r.linter == "gomodguard" || r.linter == "depguard" {
			t.Errorf("hermetically unsupported linter %s in root set", r.linter)
		}
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "gomodguard") || !strings.Contains(joined, "toolchain") {
		t.Errorf("missing gomodguard skip warning in %q", joined)
	}
	if !strings.Contains(joined, "depguard") || !strings.Contains(joined, "$gostd") {
		t.Errorf("missing depguard $gostd skip warning in %q", joined)
	}

	// errcheck must survive; and depguard WITHOUT $gostd must run.
	foundErrcheck := false
	for _, r := range roots {
		if r.linter == "errcheck" {
			foundErrcheck = true
		}
	}
	if !foundErrcheck {
		t.Error("errcheck missing from root set")
	}

	cfg2, _, err := config.Decode([]byte(`
version: "2"
linters:
  default: none
  enable:
    - depguard
  settings:
    depguard:
      rules:
        main:
          deny:
            - pkg: "example.com/forbidden"
              desc: "no"
`), ".yml")
	if err != nil {
		t.Fatal(err)
	}
	reg2, _, err := registry.BuildFromConfig(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	roots2, notes2 := selectRoots(reg2, cfg2, ModeFull)
	found := false
	for _, r := range roots2 {
		if r.linter == "depguard" {
			found = true
		}
	}
	if !found {
		t.Errorf("depguard without $gostd should run; notes=%v", notes2)
	}
}
