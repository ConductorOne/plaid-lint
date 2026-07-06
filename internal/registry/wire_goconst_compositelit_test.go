// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestGoconst_CompositeLitMasked pins the v1.10 → upstream parity hack:
// strings inside composite literals (`[]string{"foo"}`, struct fields,
// map values) must NOT surface as goconst diagnostics. golangci-lint
// v2.9 pins goconst v1.8 which has no CompositeLit visitor; plaid
// uses v1.10 which added one. Without the mask, c1 surfaces ~1.5K
// diagnostics on strings that only show up in composite literals.
func TestGoconst_CompositeLitMasked(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"goconst"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var a *analysis.Analyzer
	for _, r := range reg.Enabled() {
		if r.Name == "goconst" {
			a = r.Analyzer
			break
		}
	}
	if a == nil {
		t.Fatal("goconst not enabled")
	}

	// Fixture: three composite-literal occurrences of `/foo`. Without
	// the CompositeLit mask, goconst v1.10 fires three times. With
	// it (matching golangci-lint v2.9 / goconst v1.8 behavior), zero
	// diagnostics are emitted.
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

type Cmd struct {
	Text string
}

var Cmds = []Cmd{
	{Text: "/foo"},
	{Text: "/foo"},
	{Text: "/foo"},
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()

	// analysistest.Run with no `// want` comments asserts zero
	// diagnostics. Adding any expected diagnostic would shift this
	// test into "regression: CompositeLit mask removed" territory.
	analysistest.Run(t, dir, a, "a")
}

// TestGoconst_CallContextStillIgnored is the non-regression counter-
// part: function-call arguments must continue to be excluded (this is
// the existing IgnoreCalls = true behavior). Without it, every
// `log.Info("hello", ...)` site fires goconst.
func TestGoconst_CallContextStillIgnored(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = "none"
	cfg.Linters.Enable = []string{"goconst"}

	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var a *analysis.Analyzer
	for _, r := range reg.Enabled() {
		if r.Name == "goconst" {
			a = r.Analyzer
			break
		}
	}
	if a == nil {
		t.Fatal("goconst not enabled")
	}

	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

import "fmt"

func F() {
	fmt.Println("hello")
	fmt.Println("hello")
	fmt.Println("hello")
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	analysistest.Run(t, dir, a, "a")
}
