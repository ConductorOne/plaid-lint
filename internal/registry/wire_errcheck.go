// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"cmp"
	"fmt"
	"reflect"
	"regexp"

	errcheckpass "github.com/kisielk/errcheck/errcheck"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// errcheckExclusions builds the errcheck Exclusions block from the
// user's `linters.settings.errcheck` stanza, byte-for-byte replicating
// golangci-lint v2.9.0's `pkg/golinters/errcheck/errcheck.go::getChecker`
// (verified identical at v2.11.4).
//
// The polarity trap: every boolean in errcheck.Exclusions is the
// INVERSE of the YAML key it comes from. `Exclusions.BlankAssignments`
// means "EXCLUDE blank assignments from checking", so the user-facing
// `check-blank: true` maps to `BlankAssignments: false`. The Checker's
// visitor re-inverts it (errcheck.go::CheckPackage sets
// `blank: !c.Exclusions.BlankAssignments`). Getting this backwards
// silently flips two checks for every consumer, so the mapping is
// spelled out here rather than folded into the struct literal's
// field order.
//
// ExcludeFunctions entries are appended VERBATIM — no parsing,
// splitting, trimming, or validation. errcheck itself owns the symbol
// grammar (`io.Copy`, `(*os.File).Close`, `(hash.Hash).Write`, …) and
// silently ignores names it cannot resolve; pre-validating here would
// diverge from golangci-lint for anything at the edges of that
// grammar.
//
// SymbolRegexpsByPackage is initialised to an empty non-nil map and
// never populated: golangci-lint does not expose that knob either, and
// the Checker dereferences the map unconditionally.
//
// A nil settings pointer yields the zero-valued behavior — defaults-only
// Symbols, BlankAssignments/TypeAssertions both true — which is exactly
// what the wrapper emitted before the settings were plumbed through.
func errcheckExclusions(s *config.ErrcheckSettings) errcheckpass.Exclusions {
	if s == nil {
		s = &config.ErrcheckSettings{}
	}
	ex := errcheckpass.Exclusions{
		BlankAssignments:       !s.CheckAssignToBlank,
		TypeAssertions:         !s.CheckTypeAssertions,
		SymbolRegexpsByPackage: map[string]*regexp.Regexp{},
	}
	if !s.DisableDefaultExclusions {
		ex.Symbols = append(ex.Symbols, errcheckpass.DefaultExcludedSymbols...)
	}
	ex.Symbols = append(ex.Symbols, s.ExcludeFunctions...)
	return ex
}

// registerErrcheck registers a freshly-built errcheck analyzer pointer
// against analyzers.BundledRegistry with a settings-derived ConfigSalt,
// and returns a so the wire fn stays a one-liner.
//
// This exists because the L1/L0 cache key folds in
// `descriptor.ConfigSalt(nil)` (see internal/gopls/cache/l1.go's
// configSaltFor and its internal/engine/l0.go mirror), and the
// descriptor is looked up by ANALYZER POINTER
// (analyzers.Registry.byPtr). bundled.go registers the UPSTREAM
// `errcheck.Analyzer` pointer, but errcheckAnalyzer() mints a fresh
// pointer on every AnalyzerFn call — so the lookup missed and every
// config landed on the same constant fallback salt. Editing
// `exclude-functions` would then hit a stale L1/L0 entry produced under
// the old exclusion set.
//
// The salt is precomputed from s (not read off the closure's argument)
// because configSaltFor always calls ConfigSalt(nil); the settings must
// already be baked into the closure's captured value.
//
// TypeUseScope is deliberately left at the zero value
// (TypeUseFullTypeGraph): errcheck's Run reads pass.TypesInfo and
// pass.Pkg, so it is NOT syntax-only and must not go through
// analyzers.RegisterSyntaxOnly.
//
// Like RegisterSyntaxOnly, this accumulates one descriptor per fresh
// analyzer instance over the process lifetime — keyed by its own
// pointer, so no collisions and no practical leak for batch runs.
//
// CacheVersion is 2, one past bundled.go's 1: the emission contract now
// depends on config, so entries written by a build that ignored the
// settings must not round-trip into this one.
func registerErrcheck(a *analysis.Analyzer, s *config.ErrcheckSettings) *analysis.Analyzer {
	if a == nil {
		return nil
	}
	salt := analyzers.ConfigSalt("errcheck", s)
	analyzers.BundledRegistry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:     a,
		KeyInputs:    []analyzers.KeyInput{analyzers.KeyInputAllPackageSource},
		ConfigSalt:   func(any) [32]byte { return salt },
		CacheVersion: 2,
	})
	return a
}

// errcheckAnalyzer wraps errcheck's library Checker so the emitted
// diagnostic message matches golangci-lint v2's wrapper —
// `Error return value of \`f.Close\` is not checked` — instead of the
// upstream Analyzer's bare `unchecked error`. The richer message is
// what golangci-lint's `std-error-handling` exclusion preset regex
// matches against (`.*Close|.*Flush|os.Remove(All)?`); without it the
// preset can't fire and every `defer f.Close()` surfaces a diagnostic.
//
// Behavior mirrors `pkg/golinters/errcheck/errcheck.go::runErrCheck`
// at golangci-lint v2.9 master 72798d3:
//   - construct a per-pass *packages.Package from pass fields
//   - call Checker.CheckPackage(...).Unique()
//   - emit `Error return value of <code> is not checked` where
//     <code> is SelectorName (e.g. `f.Close`) when present, else
//     FuncName (e.g. `(io.Closer).Close`).
//   - emit `Error return value is not checked` when both are empty
//     (rare; happens for type-assertion checks).
//
// The Exclusions block comes from errcheckExclusions(settings), which
// mirrors golangci's getChecker: DefaultExcludedSymbols (unless
// `disable-default-exclusions`) plus the user's `exclude-functions`,
// and the inverted `check-blank` / `check-type-assertions` flags. A nil
// settings pointer reproduces the pre-settings defaults exactly.
func errcheckAnalyzer(settings *config.ErrcheckSettings) *analysis.Analyzer {
	checker := errcheckpass.Checker{Exclusions: errcheckExclusions(settings)}

	return &analysis.Analyzer{
		Name:       "errcheck",
		Doc:        "check for unchecked errors",
		ResultType: reflect.TypeOf(errcheckpass.Result{}),
		Run: func(pass *analysis.Pass) (any, error) {
			pkg := &packages.Package{
				Fset:      pass.Fset,
				Syntax:    pass.Files,
				Types:     pass.Pkg,
				TypesInfo: pass.TypesInfo,
			}
			result := checker.CheckPackage(pkg).Unique()
			for _, ue := range result.UncheckedErrors {
				text := "Error return value is not checked"
				if ue.FuncName != "" {
					code := cmp.Or(ue.SelectorName, ue.FuncName)
					text = fmt.Sprintf("Error return value of `%s` is not checked", code)
				}
				// Restore from token.Position to token.Pos so the
				// engine's printer pipeline can render the file/line
				// consistently. posFromPosition handles the case where
				// the file is in pass.Fset.
				pass.Report(analysis.Diagnostic{
					Pos:     posFromPosition(pass, ue.Pos),
					Message: text,
				})
			}
			return result, nil
		},
	}
}
