// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"regexp"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

// TestErrcheckAnalyzer_MessageFormat pins the diagnostic message format
// to golangci-lint v2's wrapper: `Error return value of \`f.Close\` is
// not checked`. The c1 .golangci.yml `std-error-handling` exclusion
// preset regex matches this format only — switching back to the
// upstream Analyzer's bare `unchecked error` message would break
// the preset and resurrect the 277-on-c1 errcheck divergence.
func TestErrcheckAnalyzer_MessageFormat(t *testing.T) {
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{
		"a/a.go": `package a

import "os"

func Close() {
	f, err := os.Open("x")
	_ = err
	f.Close() // want ` + "`Error return value of .*f.Close.* is not checked`" + `
}
`,
	})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()

	a := errcheckAnalyzer()
	results := analysistest.Run(t, dir, a, "a")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Sanity check: the result also exposes the upstream Result, which
	// allows downstream tools to consume the structured form. Without
	// it, the `_ "github.com/...".errcheckpass.Result` ResultType
	// declaration is silently dropped at register time.
	if results[0].Result == nil {
		t.Error("Analyzer Result is nil — ResultType plumbing dropped")
	}
}

// TestErrcheckAnalyzer_StdHandling_ClosePassesPreset asserts that the
// emitted message text passes the c1 std-error-handling exclusion
// preset regex. The preset is wired in
// internal/exclusion/presets.go::ExclusionPresetStdErrorHandling and
// is the same regex golangci-lint v2 ships at master 72798d3.
func TestErrcheckAnalyzer_StdHandling_ClosePassesPreset(t *testing.T) {
	const preset = `(?i)Error return value of .((os\.)?std(out|err)\..*|.*Close|.*Flush|os\.Remove(All)?|.*print(f|ln)?|os\.(Un)?Setenv). is not checked`
	msg := "Error return value of `f.Close` is not checked"
	if !regexpMatches(preset, msg) {
		t.Errorf("std-error-handling preset did not match %q — exclusion will not fire", msg)
	}
	// Negative: the old "unchecked error" message must NOT match,
	// otherwise the regression check is meaningless.
	if regexpMatches(preset, "unchecked error") {
		t.Error("preset regex matched the legacy 'unchecked error' string; test is invalid")
	}
}

func regexpMatches(pattern, s string) bool {
	return regexp.MustCompile(pattern).MatchString(s)
}
