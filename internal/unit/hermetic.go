// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"fmt"
	"strings"

	"github.com/conductorone/plaid-lint/internal/config"
)

// Unit mode's hermeticity contract: the result is a pure function of
// declared inputs — no toolchain on PATH, no GOROOT/GOPATH/GOMOD
// environment, no undeclared file reads. A few upstream linters
// violate that inside their own libraries, in ways the driver cannot
// intercept through the analysis.Pass seam. Running them anyway would
// be worse than skipping them: their output would either silently
// vary with the executor's environment (poisoning any content-
// addressed cache keyed on declared inputs) or silently no-op when
// the toolchain is absent (a finding-losing parity break with `run`).
//
// The driver therefore refuses to run them in unit mode, loudly: each
// enabled-but-unsupported linter surfaces as a warning naming why.
// TestUnit_HermeticAllLintersDifferential keeps this list honest — it
// runs every wired linter under a normal and a poisoned environment
// and fails on any output difference, so a new escape is caught as a
// test failure rather than a cache bug.

// hermeticUnsupported names linters whose upstream implementations
// reach outside declared inputs in ways the driver cannot redirect.
// Values are the operator-facing reason. Only linters VERIFIED to
// escape belong here, each tracked in the deferred-work ledger.
var hermeticUnsupported = map[string]string{
	// ryancurrah/gomodguard: NewProcessor execs `go env -json` and
	// reads the go.mod it names; without a toolchain it silently
	// no-ops (findings lost). Needs an upstream declared-input entry
	// point (like gomoddirectives.AnalyzeFile) before unit mode can
	// run it.
	"gomodguard": "requires the Go toolchain (`go env`) at analysis time; not runnable from declared inputs",
}

// hermeticSkip reports whether the named linter must be dropped from
// the unit root set, with the operator-facing warning when so.
func hermeticSkip(name string, golangci *config.Config) (string, bool) {
	if reason, ok := hermeticUnsupported[name]; ok {
		return fmt.Sprintf("linter %s is not supported in unit mode: %s", name, reason), true
	}
	if name == "depguard" && depguardUsesGostd(golangci) {
		// OpenPeeDeeP/depguard expands the $gostd token by listing
		// $GOROOT/src (falling back to `go env GOROOT`), so the
		// result depends on the executor's toolchain — identical
		// declared inputs would produce different SARIF on different
		// machines. Refuse rather than silently vary; rules without
		// $gostd are pure prefix matches and run fine.
		return "linter depguard is not supported in unit mode when its rules use $gostd " +
			"(expansion lists $GOROOT/src, which is not a declared input); " +
			"rewrite the rule with explicit std package prefixes or drop $gostd", true
	}
	return "", false
}

// depguardUsesGostd reports whether any depguard rule references the
// $gostd expansion token.
func depguardUsesGostd(golangci *config.Config) bool {
	if golangci == nil {
		return false
	}
	for _, rule := range golangci.Linters.Settings.Depguard.Rules {
		if rule == nil {
			continue
		}
		for _, f := range rule.Files {
			if strings.Contains(f, "$gostd") {
				return true
			}
		}
		for _, a := range rule.Allow {
			if strings.Contains(a, "$gostd") {
				return true
			}
		}
		for _, d := range rule.Deny {
			if strings.Contains(d.Pkg, "$gostd") {
				return true
			}
		}
	}
	return false
}
