// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strconv"

	gccd "4d63.com/gocheckcompilerdirectives/checkcompilerdirectives"
	nakedretpass "github.com/alexkohler/nakedret/v2"
	bidichkpass "github.com/breml/bidichk/pkg/bidichk"
	durationcheckpass "github.com/charithe/durationcheck"
	goprintffuncnamepass "github.com/golangci/go-printf-func-name/pkg/analyzer"
	nilerrpass "github.com/gostaticanalysis/nilerr"
	tparallelpass "github.com/moricho/tparallel"
	predeclaredpass "github.com/nishanths/predeclared/passes/predeclared"
	usestdlibvarspass "github.com/sashamelentyev/usestdlibvars/pkg/analyzer"
	noctxpass "github.com/sonatard/noctx"
	asciicheckpass "github.com/tdakkota/asciicheck"
	bodyclosepass "github.com/timakin/bodyclose/passes/bodyclose"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch1 attaches AnalyzerFns for the first long-tail
// wiring batch. Twelve entries, all native `*analysis.Analyzer` exports
// from their upstream modules; settings range from none (most rows) to
// flag-translated bools (bidichk, usestdlibvars) to constructor args
// (nakedret).
//
// Each row also flips the catalog Shape from ShapeRegistryOnly to
// ShapeNative — without that, [Build] still hits the "no-analyzer-wired"
// branch even with an AnalyzerFn attached.
func wireAnalyzerFnsBatch1(c *catalog) {
	wireNativeFn(c, "asciicheck", func(_ any) []*analysis.Analyzer {
		// asciicheck's Run reads only
		// pass.ResultOf[inspect.Analyzer] (a purely AST inspector,
		// not type-providing) and walks AST nodes checking
		// identifier names for non-ASCII chars via pass.Report.
		// No pass.TypesInfo, pass.Pkg, or pass.ResultOf of a
		// type-providing prerequisite. inspect.Analyzer's Run is
		// `inspector.New(pass.Files)` — package-local syntax only.
		// Classified TypeUseSyntaxOnly. Source-of-truth audit:
		// github.com/tdakkota/asciicheck@v0.4.1/asciicheck.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(asciicheckpass.NewAnalyzer(), 1)}
	})

	wireNativeFn(c, "bidichk", func(cfg any) []*analysis.Analyzer {
		a := bidichkpass.NewAnalyzer()
		s, _ := cfg.(*config.BiDiChkSettings)
		if s != nil {
			applyBidichkFlags(a, s)
		}
		// bidichk's Run reads pass.Files, pass.Fset, and
		// pass.ReadFile (workspace-local bytes), then byte-scans for
		// dangerous bidirectional unicode codepoints via bytes.IndexRune
		// and pass.Reportf. No pass.TypesInfo, pass.Pkg, or
		// pass.ResultOf. Classified TypeUseSyntaxOnly. Source-of-truth
		// audit: github.com/breml/bidichk@v0.3.3/pkg/bidichk/bidichk.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "bodyclose", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{bodyclosepass.Analyzer}
	})

	wireNativeFn(c, "durationcheck", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{durationcheckpass.Analyzer}
	})

	wireNativeFn(c, "gocheckcompilerdirectives", func(_ any) []*analysis.Analyzer {
		// gocheckcompilerdirectives's Run iterates
		// pass.Files, walks each file.Comments group, string-checks
		// comment text for `//go:` directives, and reports unknown /
		// space-prefixed directives via pass.ReportRangef. No
		// pass.TypesInfo, pass.Pkg, or pass.ResultOf access.
		// Classified TypeUseSyntaxOnly. Source-of-truth audit:
		// 4d63.com/gocheckcompilerdirectives@v1.4.0/checkcompilerdirectives/checkcompilerdirectives.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(gccd.Analyzer(), 1)}
	})

	wireNativeFn(c, "goprintffuncname", func(_ any) []*analysis.Analyzer {
		// goprintffuncname's Run reads only
		// pass.ResultOf[inspect.Analyzer] (purely AST inspector, not
		// type-providing) and walks *ast.FuncDecl, type-checking
		// parameters by AST shape: format-param Type is *ast.Ident
		// with .Name == "string", args-param is *ast.Ellipsis whose
		// .Elt is *ast.InterfaceType (empty methods) or *ast.Ident
		// "any". All comparisons are name-based on *ast.Ident.Name —
		// no .Obj reads, no types.Object. Reports via pass.Reportf.
		// No pass.TypesInfo, pass.Pkg, or pass.ResultOf of a
		// type-providing prerequisite. Classified TypeUseSyntaxOnly.
		// Source-of-truth audit:
		// github.com/golangci/go-printf-func-name@v0.1.1/pkg/analyzer/analyzer.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(goprintffuncnamepass.Analyzer, 1)}
	})

	wireNativeFn(c, "nakedret", func(cfg any) []*analysis.Analyzer {
		runner := &nakedretpass.NakedReturnRunner{}
		if s, ok := cfg.(*config.NakedretSettings); ok && s != nil {
			runner.MaxLength = s.MaxFuncLines
		}
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(nakedretpass.NakedReturnAnalyzer(runner), 1)}
	})

	wireNativeFn(c, "nilerr", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{nilerrpass.Analyzer}
	})

	wireNativeFn(c, "noctx", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{noctxpass.Analyzer}
	})

	wireNativeFn(c, "predeclared", func(cfg any) []*analysis.Analyzer {
		// predeclared's Analyzer is package-level with flag-bound
		// global vars (fIgnore, fQualified). Calling Flags.Set
		// mutates those globals — fine for a single Build, but
		// concurrent Builds with different predeclared settings would
		// race.
		a := predeclaredpass.Analyzer
		if s, ok := cfg.(*config.PredeclaredSettings); ok && s != nil {
			if v := joinComma(s.Ignore); v != "" {
				_ = a.Flags.Set(predeclaredpass.IgnoreFlag, v)
			}
			if s.Qualified {
				_ = a.Flags.Set(predeclaredpass.QualifiedFlag, "true")
			}
		}
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "tparallel", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{tparallelpass.Analyzer}
	})

	wireNativeFn(c, "usestdlibvars", func(cfg any) []*analysis.Analyzer {
		a := usestdlibvarspass.New()
		if s, ok := cfg.(*config.UseStdlibVarsSettings); ok && s != nil {
			applyUsestdlibvarsFlags(a, s)
		}
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})
}

// applyBidichkFlags translates BiDiChkSettings bools into the
// `disallowed-runes` flag string bidichk's Analyzer carries. The
// upstream default (per NewAnalyzer) enables every rune; if the user
// flips any bool we narrow the set to the rune names whose bool is
// true. If every bool is the zero value we leave the default alone.
func applyBidichkFlags(a *analysis.Analyzer, s *config.BiDiChkSettings) {
	type pair struct {
		on   bool
		name string
	}
	pairs := []pair{
		{s.LeftToRightEmbedding, "LEFT-TO-RIGHT-EMBEDDING"},
		{s.RightToLeftEmbedding, "RIGHT-TO-LEFT-EMBEDDING"},
		{s.PopDirectionalFormatting, "POP-DIRECTIONAL-FORMATTING"},
		{s.LeftToRightOverride, "LEFT-TO-RIGHT-OVERRIDE"},
		{s.RightToLeftOverride, "RIGHT-TO-LEFT-OVERRIDE"},
		{s.LeftToRightIsolate, "LEFT-TO-RIGHT-ISOLATE"},
		{s.RightToLeftIsolate, "RIGHT-TO-LEFT-ISOLATE"},
		{s.FirstStrongIsolate, "FIRST-STRONG-ISOLATE"},
		{s.PopDirectionalIsolate, "POP-DIRECTIONAL-ISOLATE"},
	}
	var enabled []string
	for _, p := range pairs {
		if p.on {
			enabled = append(enabled, p.name)
		}
	}
	if len(enabled) == 0 {
		return
	}
	_ = a.Flags.Set("disallowed-runes", joinComma(enabled))
}

// applyUsestdlibvarsFlags translates UseStdlibVarsSettings bools into
// the per-check flags usestdlibvars' Analyzer carries. Upstream
// defaults are: HTTPMethod=true, HTTPStatusCode=true, everything else
// false. We only emit Flags.Set calls when the user's value differs
// from the upstream default, so the analyzer's defaults survive when
// the settings sub-block is the zero value.
func applyUsestdlibvarsFlags(a *analysis.Analyzer, s *config.UseStdlibVarsSettings) {
	defaults := map[string]bool{
		usestdlibvarspass.HTTPMethodFlag:         true,
		usestdlibvarspass.HTTPStatusCodeFlag:     true,
		usestdlibvarspass.TimeWeekdayFlag:        false,
		usestdlibvarspass.TimeMonthFlag:          false,
		usestdlibvarspass.TimeLayoutFlag:         false,
		usestdlibvarspass.CryptoHashFlag:         false,
		usestdlibvarspass.RPCDefaultPathFlag:     false,
		usestdlibvarspass.SQLIsolationLevelFlag:  false,
		usestdlibvarspass.TLSSignatureSchemeFlag: false,
		usestdlibvarspass.ConstantKindFlag:       false,
		usestdlibvarspass.TimeDateMonthFlag:      false,
	}
	wanted := map[string]bool{
		usestdlibvarspass.HTTPMethodFlag:         s.HTTPMethod,
		usestdlibvarspass.HTTPStatusCodeFlag:     s.HTTPStatusCode,
		usestdlibvarspass.TimeWeekdayFlag:        s.TimeWeekday,
		usestdlibvarspass.TimeMonthFlag:          s.TimeMonth,
		usestdlibvarspass.TimeLayoutFlag:         s.TimeLayout,
		usestdlibvarspass.CryptoHashFlag:         s.CryptoHash,
		usestdlibvarspass.RPCDefaultPathFlag:     s.DefaultRPCPath,
		usestdlibvarspass.SQLIsolationLevelFlag:  s.SQLIsolationLevel,
		usestdlibvarspass.TLSSignatureSchemeFlag: s.TLSSignatureScheme,
		usestdlibvarspass.ConstantKindFlag:       s.ConstantKind,
		usestdlibvarspass.TimeDateMonthFlag:      s.TimeDateMonth,
	}
	// If every "wanted" matches the zero value of the struct, treat
	// it as "use upstream defaults" — the most common path.
	var anyNonZero bool
	for _, v := range wanted {
		if v {
			anyNonZero = true
			break
		}
	}
	if !anyNonZero {
		return
	}
	for flag, want := range wanted {
		if want == defaults[flag] {
			continue
		}
		_ = a.Flags.Set(flag, strconv.FormatBool(want))
	}
}

// wireNativeFn attaches an AnalyzerFn to a long-tail wiring entry.
// The entry must already be ShapeNative (set in seed.go for batch
// rows) — wireNativeFn does not silently promote, because that would
// hide a seed/wiring mismatch.
func wireNativeFn(c *catalog, name string, fn func(any) []*analysis.Analyzer) {
	e, ok := c.resolve(name)
	if !ok {
		panic("registry: wireNativeFn: missing catalog entry " + name)
	}
	if e.Shape != ShapeNative {
		panic("registry: wireNativeFn: entry " + name + " is not ShapeNative; update seed.go")
	}
	e.AnalyzerFn = fn
}

// joinComma is a tiny helper that joins a string slice with commas
// without pulling in `strings` at the call site (every batch1 wiring
// closure stays one-liner small).
func joinComma(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
