// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strconv"
	"strings"

	dupwordpass "github.com/Abirdcfly/dupword"
	cycloppass "github.com/bkielbasa/cyclop/pkg/analyzer"
	errchkjsonpass "github.com/breml/errchkjson"
	reassignpass "github.com/curioswitch/go-reassign"
	rowserrcheckpass "github.com/jingyugao/rowserrcheck/passes/rowserr"
	paralleltestpass "github.com/kunwardeep/paralleltest/pkg/paralleltest"
	inamedparampass "github.com/macabu/inamedparam"
	funcorderpass "github.com/manuelarte/funcorder/analyzer"
	testpackagepass "github.com/maratori/testpackage/pkg/testpackage"
	ginkgolinterpass "github.com/nunnatsa/ginkgolinter"
	ginkgolinterconfig "github.com/nunnatsa/ginkgolinter/config"
	interfacebloatpass "github.com/sashamelentyev/interfacebloat/pkg/analyzer"
	funlenpass "github.com/ultraware/funlen"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch4 attaches AnalyzerFns for the fourth long-tail
// wiring batch. Thirteen entries, all native `*analysis.Analyzer`
// exports. Settings translate via constructor args (funlen,
// rowserrcheck), Flags.Set (cyclop, dupword, errchkjson, funcorder,
// inamedparam, interfacebloat, paralleltest, reassign, testpackage),
// or NewAnalyzerWithConfig (ginkgolinter).
func wireAnalyzerFnsBatch4(c *catalog) {
	wireNativeFn(c, "cyclop", func(cfg any) []*analysis.Analyzer {
		// cyclop's upstream uses camelCase flag names — not kebab-case
		// like most other linters in our long-tail set. See playbook
		// landmine 16.
		a := cycloppass.NewAnalyzer()
		if s, ok := cfg.(*config.CyclopSettings); ok && s != nil {
			if s.MaxComplexity > 0 {
				_ = a.Flags.Set("maxComplexity", strconv.Itoa(s.MaxComplexity))
			}
			if s.PackageAverage > 0 {
				_ = a.Flags.Set("packageAverage",
					strconv.FormatFloat(s.PackageAverage, 'f', -1, 64))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "dupword", func(cfg any) []*analysis.Analyzer {
		a := dupwordpass.NewAnalyzer()
		if s, ok := cfg.(*config.DupWordSettings); ok && s != nil {
			for _, k := range s.Keywords {
				_ = a.Flags.Set("keyword", k)
			}
			for _, k := range s.Ignore {
				_ = a.Flags.Set("ignore", k)
			}
			if s.CommentsOnly {
				_ = a.Flags.Set("comments-only", "true")
			}
		}
		// dupword's Run reads pass.Files, pass.Fset, and
		// pass.ResultOf[inspect.Analyzer] (purely AST inspector; not
		// type-providing). It scans file.Comments for duplicate
		// consecutive words and string literals for the same; reports
		// via pass.Report. No pass.TypesInfo, pass.Pkg, or pass.ResultOf
		// of a type-providing prerequisite. Classified TypeUseSyntaxOnly.
		// Source-of-truth audit:
		// github.com/Abirdcfly/dupword@v0.1.8/dupword.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "errchkjson", func(cfg any) []*analysis.Analyzer {
		a := errchkjsonpass.NewAnalyzer()
		if s, ok := cfg.(*config.ErrChkJSONSettings); ok && s != nil {
			// Note: upstream uses inverted semantic: `omit-safe=true`
			// disables the safe-encoding check. `CheckErrorFreeEncoding`
			// is positive ("do check"), so flip when the user explicitly
			// disabled the check via CheckErrorFreeEncoding=false; the
			// zero value (false) is ambiguous — defer to upstream default
			// (omit-safe=false → safe check enabled), matching upstream.
			// See playbook landmine 11 (positive-vs-negated bools).
			if s.ReportNoExported {
				_ = a.Flags.Set("report-no-exported", "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "funcorder", func(cfg any) []*analysis.Analyzer {
		a := funcorderpass.NewAnalyzer()
		if s, ok := cfg.(*config.FuncOrderSettings); ok && s != nil {
			_ = a.Flags.Set(funcorderpass.ConstructorCheckName, strconv.FormatBool(s.Constructor))
			_ = a.Flags.Set(funcorderpass.StructMethodCheckName, strconv.FormatBool(s.StructMethod))
			_ = a.Flags.Set(funcorderpass.AlphabeticalCheckName, strconv.FormatBool(s.Alphabetical))
		}
		// funcorder's Run reads only
		// pass.ResultOf[inspect.Analyzer] (purely AST inspector, not
		// type-providing). The internal/ subpackage's reports.go uses
		// pass.Report only; the file_processor walks *ast.GenDecl /
		// *ast.FuncDecl shapes and matches receivers by *ast.Ident.Name
		// — no .Obj reads, no types.Object. Reports via pass.Report.
		// Classified TypeUseSyntaxOnly. Source-of-truth audit:
		// github.com/manuelarte/funcorder@v0.6.0/{analyzer,internal}/*.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "funlen", func(cfg any) []*analysis.Analyzer {
		// funlen.NewAnalyzer takes constructor args directly.
		lineLimit := 60 // upstream default
		stmtLimit := 40 // upstream default
		ignoreComments := false
		if s, ok := cfg.(*config.FunlenSettings); ok && s != nil {
			if s.Lines > 0 {
				lineLimit = s.Lines
			}
			if s.Statements > 0 {
				stmtLimit = s.Statements
			}
			ignoreComments = s.IgnoreComments
		}
		// funlen's Run reads only pass.Files and
		// pass.Fset, builds ast.NewCommentMap, ranges over file.Decls
		// for *ast.FuncDecl, counts statements via ast.Inspect, and
		// reports via pass.Reportf. No pass.TypesInfo, pass.Pkg, or
		// pass.ResultOf. Classified TypeUseSyntaxOnly. Source-of-truth
		// audit: github.com/ultraware/funlen@v0.2.0/funlen.go.
		return []*analysis.Analyzer{
			analyzers.RegisterSyntaxOnly(funlenpass.NewAnalyzer(lineLimit, stmtLimit, ignoreComments), 1),
		}
	})

	wireNativeFn(c, "ginkgolinter", func(cfg any) []*analysis.Analyzer {
		if s, ok := cfg.(*config.GinkgoLinterSettings); ok && s != nil {
			gc := &ginkgolinterconfig.Config{
				SuppressLen:               s.SuppressLenAssertion,
				SuppressNil:               s.SuppressNilAssertion,
				SuppressErr:               s.SuppressErrAssertion,
				SuppressCompare:           s.SuppressCompareAssertion,
				SuppressAsync:             s.SuppressAsyncAssertion,
				ForbidFocus:               s.ForbidFocusContainer,
				SuppressTypeCompare:       s.SuppressTypeCompareWarning,
				AllowHaveLen0:             s.AllowHaveLenZero,
				ForceExpectTo:             s.ForceExpectTo,
				ValidateAsyncIntervals:    s.ValidateAsyncIntervals,
				ForbidSpecPollution:       s.ForbidSpecPollution,
				ForceSucceedForFuncs:      s.ForceSucceedForFuncs,
				ForceAssertionDescription: s.ForceAssertionDescription,
				ForeToNot:                 s.ForeToNot,
			}
			return []*analysis.Analyzer{ginkgolinterpass.NewAnalyzerWithConfig(gc)}
		}
		return []*analysis.Analyzer{ginkgolinterpass.NewAnalyzer()}
	})

	wireNativeFn(c, "inamedparam", func(cfg any) []*analysis.Analyzer {
		// inamedparam.Analyzer is a package-level global. Same race
		// surface as predeclared (landmine 4) / nonamedreturns (12) /
		// gocognit + maintidx (batch 3). One Build per process keeps it
		// safe today.
		a := inamedparampass.Analyzer
		if s, ok := cfg.(*config.INamedParamSettings); ok && s != nil {
			if s.SkipSingleParam {
				_ = a.Flags.Set("skip-single-param", "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "interfacebloat", func(cfg any) []*analysis.Analyzer {
		a := interfacebloatpass.New()
		if s, ok := cfg.(*config.InterfaceBloatSettings); ok && s != nil {
			if s.Max > 0 {
				_ = a.Flags.Set(interfacebloatpass.InterfaceMaxMethodsFlag, strconv.Itoa(s.Max))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "paralleltest", func(cfg any) []*analysis.Analyzer {
		a := paralleltestpass.NewAnalyzer()
		if s, ok := cfg.(*config.ParallelTestSettings); ok && s != nil {
			if s.IgnoreMissing {
				_ = a.Flags.Set("i", "true")
			}
			if s.IgnoreMissingSubtests {
				_ = a.Flags.Set("ignoremissingsubtests", "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "reassign", func(cfg any) []*analysis.Analyzer {
		a := reassignpass.NewAnalyzer()
		if s, ok := cfg.(*config.ReassignSettings); ok && s != nil {
			if len(s.Patterns) > 0 {
				// Upstream takes a single regex pattern. Join multiple
				// settings entries via alternation (`a|b`).
				_ = a.Flags.Set(reassignpass.FlagPattern, strings.Join(s.Patterns, "|"))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "rowserrcheck", func(cfg any) []*analysis.Analyzer {
		// Constructor takes a variadic ...string of SQL driver package
		// paths to check. Empty arg lets upstream use its built-in set.
		var pkgs []string
		if s, ok := cfg.(*config.RowsErrCheckSettings); ok && s != nil {
			pkgs = s.Packages
		}
		return []*analysis.Analyzer{rowserrcheckpass.NewAnalyzer(pkgs...)}
	})

	wireNativeFn(c, "testpackage", func(cfg any) []*analysis.Analyzer {
		a := testpackagepass.NewAnalyzer()
		if s, ok := cfg.(*config.TestpackageSettings); ok && s != nil {
			if s.SkipRegexp != "" {
				_ = a.Flags.Set(testpackagepass.SkipRegexpFlagName, s.SkipRegexp)
			}
			if len(s.AllowPackages) > 0 {
				_ = a.Flags.Set(testpackagepass.AllowPackagesFlagName,
					strings.Join(s.AllowPackages, ","))
			}
		}
		return []*analysis.Analyzer{a}
	})
}
