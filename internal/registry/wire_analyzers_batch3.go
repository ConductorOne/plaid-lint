// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strconv"
	"strings"

	gochecknoglobalspass "4d63.com/gochecknoglobals/checknoglobals"
	nilnilpass "github.com/Antonboom/nilnil/pkg/analyzer"
	makezeropass "github.com/ashanbrown/makezero/pkg/analyzer"
	copyloopvarpass "github.com/karamaru-alpha/copyloopvar"
	contextcheckpass "github.com/kkHAIKE/contextcheck"
	testableexamplespass "github.com/maratori/testableexamples/pkg/testableexamples"
	sqlclosecheckpass "github.com/ryanrolds/sqlclosecheck/pkg/analyzer"
	nlreturnpass "github.com/ssgreg/nlreturn/v2/pkg/nlreturn"
	gocognitpass "github.com/uudashr/gocognit"
	gosmopolitanpass "github.com/xen0n/gosmopolitan"
	maintidxpass "github.com/yagipy/maintidx"
	fatcontextpass "go.augendre.info/fatcontext/pkg/analyzer"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch3 attaches AnalyzerFns for the third long-tail
// wiring batch. Twelve entries, all native `*analysis.Analyzer` exports.
// Settings range from none (contextcheck, gochecknoglobals, sqlclosecheck,
// testableexamples) to single-flag translation (gocognit, maintidx,
// nlreturn, fatcontext, makezero, copyloopvar) to multi-flag (nilnil) to
// constructor-arg (gosmopolitan).
func wireAnalyzerFnsBatch3(c *catalog) {
	wireNativeFn(c, "contextcheck", func(_ any) []*analysis.Analyzer {
		// Configuration{DisableFact: false} matches upstream default.
		return []*analysis.Analyzer{contextcheckpass.NewAnalyzer(contextcheckpass.Configuration{})}
	})

	wireNativeFn(c, "copyloopvar", func(cfg any) []*analysis.Analyzer {
		a := copyloopvarpass.NewAnalyzer()
		if s, ok := cfg.(*config.CopyLoopVarSettings); ok && s != nil {
			if s.CheckAlias {
				_ = a.Flags.Set("check-alias", "true")
			}
		}
		// copyloopvar's Run reads only
		// pass.ResultOf[inspect.Analyzer] (a purely AST inspector,
		// not type-providing) and walks *ast.RangeStmt / *ast.ForStmt
		// nodes; identifier comparisons are name-based on
		// *ast.Ident.Name (no .Obj, no types.Object). Reports via
		// pass.Report. No pass.TypesInfo, pass.Pkg, or pass.ResultOf
		// of a type-providing prerequisite. Classified
		// TypeUseSyntaxOnly. Source-of-truth audit:
		// github.com/karamaru-alpha/copyloopvar@v1.2.2/copyloopvar.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "fatcontext", func(cfg any) []*analysis.Analyzer {
		a := fatcontextpass.NewAnalyzer()
		if s, ok := cfg.(*config.FatcontextSettings); ok && s != nil {
			if s.CheckStructPointers {
				_ = a.Flags.Set(fatcontextpass.FlagCheckStructPointers, "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "gochecknoglobals", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{gochecknoglobalspass.Analyzer()}
	})

	wireNativeFn(c, "gocognit", func(cfg any) []*analysis.Analyzer {
		// gocognit.Analyzer is a package-level global. Like
		// predeclared (landmine 4) and nonamedreturns (landmine 12),
		// Flags.Set mutates a per-Analyzer FlagSet whose backing
		// storage is a package global ("over" var). Safe today since
		// the engine runs one Build per process.
		a := gocognitpass.Analyzer
		if s, ok := cfg.(*config.GocognitSettings); ok && s != nil {
			if s.MinComplexity > 0 {
				_ = a.Flags.Set("over", strconv.Itoa(s.MinComplexity))
			}
		}
		// gocognit's Run reads only
		// pass.ResultOf[inspect.Analyzer] (purely AST inspector, not
		// type-providing) and visits *ast.FuncDecl nodes, computing
		// cognitive complexity from AST shape (BranchStmt, BinaryExpr,
		// IfStmt, etc.). The one *ast.Ident.Obj read (visitCallExpr at
		// gocognit.go:447-449) compares two same-file idents for direct
		// recursion detection — .Obj here is the parser's intra-file
		// resolver bag (go/ast.Object), populated when ast.File is
		// parsed; it is NOT types.Object and does not cross package
		// boundaries. Reports via pass.Reportf. No pass.TypesInfo,
		// pass.Pkg, or pass.ResultOf of a type-providing prerequisite.
		// Classified TypeUseSyntaxOnly. Source-of-truth audit:
		// github.com/uudashr/gocognit@v1.2.1/gocognit.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "gosmopolitan", func(cfg any) []*analysis.Analyzer {
		if s, ok := cfg.(*config.GosmopolitanSettings); ok && s != nil {
			ac := &gosmopolitanpass.AnalyzerConfig{
				LookAtTests:     false,
				EscapeHatches:   s.EscapeHatches,
				WatchForScripts: s.WatchForScripts,
				AllowTimeLocal:  s.AllowTimeLocal,
			}
			return []*analysis.Analyzer{gosmopolitanpass.NewAnalyzerWithConfig(ac)}
		}
		return []*analysis.Analyzer{gosmopolitanpass.NewAnalyzer()}
	})

	wireNativeFn(c, "maintidx", func(cfg any) []*analysis.Analyzer {
		// Same package-global flag pattern as gocognit.
		a := maintidxpass.Analyzer
		if s, ok := cfg.(*config.MaintIdxSettings); ok && s != nil {
			if s.Under > 0 {
				_ = a.Flags.Set("under", strconv.Itoa(s.Under))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "makezero", func(cfg any) []*analysis.Analyzer {
		a := makezeropass.NewAnalyzer()
		if s, ok := cfg.(*config.MakezeroSettings); ok && s != nil {
			if s.Always {
				_ = a.Flags.Set("always", "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "nilnil", func(cfg any) []*analysis.Analyzer {
		a := nilnilpass.New()
		if s, ok := cfg.(*config.NilNilSettings); ok && s != nil {
			if s.OnlyTwo != nil {
				_ = a.Flags.Set("only-two", strconv.FormatBool(*s.OnlyTwo))
			}
			if s.DetectOpposite {
				_ = a.Flags.Set("detect-opposite", "true")
			}
			if len(s.CheckedTypes) > 0 {
				_ = a.Flags.Set("checked-types", strings.Join(s.CheckedTypes, ","))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "nlreturn", func(cfg any) []*analysis.Analyzer {
		a := nlreturnpass.NewAnalyzer()
		if s, ok := cfg.(*config.NlreturnSettings); ok && s != nil {
			if s.BlockSize > 0 {
				_ = a.Flags.Set("block-size", strconv.Itoa(s.BlockSize))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "sqlclosecheck", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{sqlclosecheckpass.NewAnalyzer()}
	})

	wireNativeFn(c, "testableexamples", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{testableexamplespass.NewAnalyzer()}
	})
}
