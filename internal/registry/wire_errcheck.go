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
)

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
// The Checker is constructed with DefaultExcludedSymbols (which
// already covers fmt.Print* / os.Std* and the rest of the upstream
// short-list). golangci's wrapper additionally appends config-driven
// excludes; since plaid has no exposed errcheck settings today, the
// defaults alone match upstream's default behavior.
func errcheckAnalyzer() *analysis.Analyzer {
	// BlankAssignments and TypeAssertions default to TRUE in
	// Exclusions because the Checker's visitor reads them as the
	// inverted `blank` / `asserts` flag (errcheck.go::CheckPackage
	// sets `blank: !c.Exclusions.BlankAssignments`). Leaving both at
	// the zero value inverts to `blank=true, asserts=true` and reports
	// every `_ = f()` assignment + every `x.(T)` assertion — which
	// matches neither the upstream Analyzer default nor golangci-lint's
	// wrapper (both default to `blank=false, asserts=false`).
	checker := errcheckpass.Checker{
		Exclusions: errcheckpass.Exclusions{
			Symbols:                append([]string(nil), errcheckpass.DefaultExcludedSymbols...),
			SymbolRegexpsByPackage: map[string]*regexp.Regexp{},
			BlankAssignments:       true,
			TypeAssertions:         true,
		},
	}

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
