// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"regexp"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// TestErrcheckAnalyzer_MessageFormat pins the diagnostic message format
// to golangci-lint v2's wrapper: `Error return value of \`f.Close\` is
// not checked`. The `std-error-handling` exclusion preset regex
// matches this format only — switching back to the upstream
// Analyzer's bare `unchecked error` message would break the preset
// and resurrect the 277-diagnostic errcheck divergence measured on
// the reference corpus.
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

	a := errcheckAnalyzer(nil)
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
// emitted message text passes the std-error-handling exclusion
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

// errcheckSrc is the shared analysistest fixture for the settings
// tests. Every construct the `linters.settings.errcheck` stanza can
// toggle appears exactly once:
//
//   - io.Copy          — a package-level func, the plain `pkg.Fn`
//     exclude-functions form.
//   - f.Close          — a pointer-receiver method, the
//     `(*pkg.T).M` exclude-functions form on a stdlib type.
//   - t.M              — a pointer-receiver method on a LOCAL type,
//     the `(*a.T).M` form (analysistest roots the fixture package at
//     import path `a`).
//   - fmt.Println      — a DefaultExcludedSymbols member, for
//     disable-default-exclusions.
//   - _ = os.Remove(…) — a blank assignment, for check-blank.
//   - n := i.(int)     — an unguarded type assertion, for
//     check-type-assertions.
//
// The `want` comments are omitted deliberately: each test asserts
// against its own expected set via errcheckMessages rather than
// analysistest's inline-comment matcher, because the SAME source has
// to produce four different diagnostic sets across the settings
// matrix and inline comments can only encode one.
const errcheckSrc = `package a

import (
	"fmt"
	"io"
	"os"
)

type T struct{}

func (t *T) M() error { return nil }

func F(i any) {
	f, err := os.Open("x")
	_ = err
	io.Copy(io.Discard, f)
	f.Close()
	fmt.Println("hi")
	_ = os.Remove("x")
	n := i.(int)
	_ = n
	var t T
	t.M()
}
`

// errcheckMessages runs the wrapper over errcheckSrc with the given
// settings and returns the emitted diagnostic messages in source
// order.
//
// analysistest.Run reports "unexpected diagnostic" through the
// *testing.T it is handed, so the errcheckSilentT shim swallows those
// (errcheckSrc carries no `want` comments) while still surfacing real
// load/build failures via t.Fatalf below.
func errcheckMessages(t *testing.T, s *config.ErrcheckSettings) []string {
	t.Helper()
	dir, cleanup, err := analysistest.WriteFiles(map[string]string{"a/a.go": errcheckSrc})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()

	results := analysistest.Run(&errcheckSilentT{T: t}, dir, errcheckAnalyzer(s), "a")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Err != nil {
		t.Fatalf("analysis error: %v", results[0].Err)
	}
	msgs := make([]string, 0, len(results[0].Diagnostics))
	for _, d := range results[0].Diagnostics {
		msgs = append(msgs, d.Message)
	}
	return msgs
}

// errcheckSilentT drops Errorf so analysistest's inline-`want`
// bookkeeping stays quiet; the surrounding test asserts on the
// returned diagnostics directly.
type errcheckSilentT struct{ *testing.T }

func (*errcheckSilentT) Errorf(string, ...any) {}

func errcheckHas(msgs []string, substr string) bool {
	for _, m := range msgs {
		if regexp.MustCompile(regexp.QuoteMeta(substr)).MatchString(m) {
			return true
		}
	}
	return false
}

// TestErrcheckAnalyzer_ExcludeFunctions asserts that
// `linters.settings.errcheck.exclude-functions` actually reaches the
// Checker. Before the settings were plumbed through the wire fn, all
// four errcheck knobs were parsed into config.ErrcheckSettings and
// then silently discarded.
//
// Covers all three symbol grammars a user can write: a package-level
// func (`io.Copy`), a pointer-receiver method on a stdlib type
// (`(*os.File).Close`), and a pointer-receiver method on a local type
// (`(*a.T).M`). Entries are passed to errcheck verbatim — golangci-lint
// does no parsing or validation of them and neither do we.
func TestErrcheckAnalyzer_ExcludeFunctions(t *testing.T) {
	before := errcheckMessages(t, nil)
	for _, want := range []string{"io.Copy", "f.Close", "t.M"} {
		if !errcheckHas(before, want) {
			t.Fatalf("baseline did not report %q; got %q", want, before)
		}
	}

	after := errcheckMessages(t, &config.ErrcheckSettings{
		ExcludeFunctions: []string{"io.Copy", "(*os.File).Close", "(*a.T).M"},
	})
	if len(after) != 0 {
		t.Errorf("exclude-functions did not suppress: got %q", after)
	}
}

// TestErrcheckAnalyzer_CheckBlankPolarity pins the INVERTED mapping
// between the YAML key and errcheck.Exclusions.BlankAssignments.
// `check-blank: true` must set BlankAssignments to FALSE ("do not
// exclude blank assignments"). Flipping this silently reports — or
// silently stops reporting — every `_ = f()` in the tree.
func TestErrcheckAnalyzer_CheckBlankPolarity(t *testing.T) {
	if got := errcheckMessages(t, nil); errcheckHas(got, "os.Remove") {
		t.Errorf("check-blank defaults to false but `_ = os.Remove(...)` was reported: %q", got)
	}
	got := errcheckMessages(t, &config.ErrcheckSettings{CheckAssignToBlank: true})
	if !errcheckHas(got, "os.Remove") {
		t.Errorf("check-blank: true did not report `_ = os.Remove(...)`: %q", got)
	}
}

// TestErrcheckAnalyzer_CheckTypeAssertionsPolarity is the
// check-blank sibling for errcheck.Exclusions.TypeAssertions. An
// unguarded `n := i.(int)` carries neither a SelectorName nor a
// FuncName, so the wrapper emits the bare
// `Error return value is not checked` form.
func TestErrcheckAnalyzer_CheckTypeAssertionsPolarity(t *testing.T) {
	const bare = "Error return value is not checked"
	if got := errcheckMessages(t, nil); errcheckHas(got, bare) {
		t.Errorf("check-type-assertions defaults to false but `n := i.(int)` was reported: %q", got)
	}
	got := errcheckMessages(t, &config.ErrcheckSettings{CheckTypeAssertions: true})
	if !errcheckHas(got, bare) {
		t.Errorf("check-type-assertions: true did not report `n := i.(int)`: %q", got)
	}
}

// TestErrcheckAnalyzer_DisableDefaultExclusions asserts that
// errcheck.DefaultExcludedSymbols is applied by default and dropped
// when the user opts out. fmt.Println is the canonical member.
func TestErrcheckAnalyzer_DisableDefaultExclusions(t *testing.T) {
	if got := errcheckMessages(t, nil); errcheckHas(got, "fmt.Println") {
		t.Errorf("DefaultExcludedSymbols not applied; fmt.Println reported: %q", got)
	}
	got := errcheckMessages(t, &config.ErrcheckSettings{DisableDefaultExclusions: true})
	if !errcheckHas(got, "fmt.Println") {
		t.Errorf("disable-default-exclusions: true did not report fmt.Println: %q", got)
	}
}

// TestErrcheckAnalyzer_NilSettingsFallsBackToDefaults pins the
// nil / wrong-typed cfg contract: both must behave exactly like a
// zero-valued ErrcheckSettings, which is exactly the pre-settings
// wrapper behavior (defaults-only Symbols, blank + assert checks off).
func TestErrcheckAnalyzer_NilSettingsFallsBackToDefaults(t *testing.T) {
	zero := errcheckMessages(t, &config.ErrcheckSettings{})
	nilCfg := errcheckMessages(t, nil)
	if len(zero) != len(nilCfg) {
		t.Fatalf("nil settings diverged from zero settings: %q vs %q", nilCfg, zero)
	}
	for i := range zero {
		if zero[i] != nilCfg[i] {
			t.Fatalf("nil settings diverged from zero settings at %d: %q vs %q", i, nilCfg[i], zero[i])
		}
	}
	// A wrong-typed cfg reaching the wire fn must degrade the same
	// way rather than panicking on the type assertion.
	fn := AnalyzerFnForTest("errcheck")
	if fn == nil {
		t.Fatal("no AnalyzerFn wired for errcheck")
	}
	if got := fn("not-errcheck-settings"); len(got) != 1 || got[0] == nil {
		t.Fatalf("wrong-typed cfg produced %v", got)
	}
}

// TestErrcheckWireFn_ThreadsSettings closes the loop the bug left
// open: registry's AnalyzerFn must forward its cfg argument into the
// Checker. The pre-fix wire fn was `func(_ any)` and dropped it.
func TestErrcheckWireFn_ThreadsSettings(t *testing.T) {
	fn := AnalyzerFnForTest("errcheck")
	if fn == nil {
		t.Fatal("no AnalyzerFn wired for errcheck")
	}
	built := fn(&config.ErrcheckSettings{ExcludeFunctions: []string{"io.Copy", "(*os.File).Close", "(*a.T).M"}})
	if len(built) != 1 {
		t.Fatalf("expected 1 analyzer, got %d", len(built))
	}

	dir, cleanup, err := analysistest.WriteFiles(map[string]string{"a/a.go": errcheckSrc})
	if err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	defer cleanup()
	results := analysistest.Run(&errcheckSilentT{T: t}, dir, built[0], "a")
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if n := len(results[0].Diagnostics); n != 0 {
		t.Errorf("wire fn dropped settings: got %d diagnostics, want 0", n)
	}
}

// TestErrcheckWireFn_RegistersSettingsDerivedSalt is the cache-
// correctness regression test. The L1/L0 key folds in
// descriptor.ConfigSalt(nil), and the descriptor is looked up by
// ANALYZER POINTER. bundled.go registers the upstream
// errcheck.Analyzer pointer while the wire fn mints a fresh one, so
// before registerErrcheck the lookup missed and every config shared
// one constant fallback salt — editing exclude-functions would serve
// stale cached diagnostics.
//
// Two different exclude-functions lists must produce different salts;
// two identical ones must produce identical salts.
func TestErrcheckWireFn_RegistersSettingsDerivedSalt(t *testing.T) {
	fn := AnalyzerFnForTest("errcheck")
	if fn == nil {
		t.Fatal("no AnalyzerFn wired for errcheck")
	}
	saltFor := func(s *config.ErrcheckSettings) [32]byte {
		built := fn(s)
		if len(built) != 1 {
			t.Fatalf("expected 1 analyzer, got %d", len(built))
		}
		d := analyzers.BundledRegistry.Lookup(built[0])
		if d == nil {
			t.Fatal("wire fn did not register a descriptor for its fresh analyzer pointer")
		}
		if d.CacheVersion != 2 {
			t.Errorf("CacheVersion = %d, want 2", d.CacheVersion)
		}
		// configSaltFor (internal/gopls/cache/l1.go and its
		// internal/engine/l0.go mirror) always calls the closure with
		// nil, so the settings must already be baked in.
		return d.ConfigSalt(nil)
	}

	a := saltFor(&config.ErrcheckSettings{ExcludeFunctions: []string{"io.Copy"}})
	b := saltFor(&config.ErrcheckSettings{ExcludeFunctions: []string{"os.Setenv"}})
	if a == b {
		t.Error("different exclude-functions produced the same ConfigSalt — L1/L0 would serve stale diagnostics")
	}
	if again := saltFor(&config.ErrcheckSettings{ExcludeFunctions: []string{"io.Copy"}}); again != a {
		t.Error("identical exclude-functions produced different ConfigSalt — cache would never warm")
	}
	if nilSalt := saltFor(nil); nilSalt == a {
		t.Error("nil settings collided with a populated exclude-functions salt")
	}

	// The boolean knobs must move the salt too — they change the
	// emission contract just as much as exclude-functions does.
	blank := saltFor(&config.ErrcheckSettings{CheckAssignToBlank: true})
	if blank == saltFor(nil) {
		t.Error("check-blank did not move the ConfigSalt")
	}
}
