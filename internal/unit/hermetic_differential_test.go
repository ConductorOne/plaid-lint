// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"bytes"
	"os"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// TestUnit_HermeticAllLintersDifferential is the gate that keeps the
// hermeticUnsupported list honest: it runs EVERY wired linter over
// the same declared inputs under (a) the normal test environment and
// (b) a poisoned one (no toolchain on PATH, bogus GOROOT/GOPATH/
// GOMOD/HOME), and requires byte-identical SARIF and facts outputs.
//
// A linter that shells out to `go`, reads GOROOT, or consults any
// other ambient state either changes its findings between the two
// runs (caught here as a diff) or fails loudly. Escapes of this class
// are exactly what poisons a content-addressed cache keyed on
// declared inputs — see hermetic.go for the two known offenders this
// gate already forced out of the root set.
//
// The fixture deliberately contains no enum-shaped const blocks: the
// exhaustive linter's fact payload is map-typed (see the factaudit
// allowlist), so enum-bearing fixtures would trip the byte comparison
// on gob map ordering rather than on a hermeticity escape.
func TestUnit_HermeticAllLintersDifferential(t *testing.T) {
	allCfg := func() *config.Config {
		cfg, warnings, err := config.Decode([]byte(`
version: "2"
linters:
  default: all
`), ".yml")
		if err != nil {
			t.Fatalf("decode config: %v", err)
		}
		for _, w := range warnings {
			t.Logf("config warning: %s: %s", w.Field, w.Message)
		}
		return cfg
	}

	pkgs := buildFixture(t, map[string]string{
		"go.mod": fixtureGoMod,
		"dep/dep.go": `package dep

import (
	"fmt"
	"os"
)

// Logf is a printf wrapper.
func Logf(format string, args ...any) {
	fmt.Printf(format, args...)
}

func sloppy() {
	os.Mkdir("x", 0o777)
	var unused_local int
	_ = unused_local
}
`,
	})
	dep := pkgs["example.com/fix/dep"]
	if dep == nil {
		t.Fatal("fixture package missing")
	}

	// Run 1: normal environment.
	_, sarif1, facts1 := runUnitOn(t, dep, ModeFull, nil, allCfg())

	// Run 2: poisoned environment. t.Setenv restores after the test.
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("GOROOT", "/nonexistent")
	t.Setenv("GOPATH", "/nonexistent")
	t.Setenv("GOMOD", "/nonexistent/go.mod")
	t.Setenv("GOFLAGS", "")
	t.Setenv("HOME", "/nonexistent")
	t.Setenv("XDG_CACHE_HOME", "/nonexistent")
	_, sarif2, facts2 := runUnitOn(t, dep, ModeFull, nil, allCfg())

	for _, pair := range [][2]string{{sarif1, sarif2}, {facts1, facts2}} {
		a, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, b) {
			t.Errorf("output differs between normal and poisoned environments:\n  %s (%d bytes)\n  %s (%d bytes)\nan enabled linter is reading ambient state — add it to hermeticUnsupported or fix its wiring",
				pair[0], len(a), pair[1], len(b))
		}
	}
}
