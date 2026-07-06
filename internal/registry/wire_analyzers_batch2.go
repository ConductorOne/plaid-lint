// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	noinlineerrpass "github.com/AlwxSin/noinlineerr"
	errnamepass "github.com/Antonboom/errname/pkg/analyzer"
	asasalintpass "github.com/alingse/asasalint"
	nilnesserrpass "github.com/alingse/nilnesserr"
	mirrorpass "github.com/butuzov/mirror"
	intrangepass "github.com/ckaznocha/intrange"
	nonamedreturnspass "github.com/firefart/nonamedreturns/analyzer"
	forcetypeassertpass "github.com/gostaticanalysis/forcetypeassert"
	canonicalheaderpass "github.com/lasiar/canonicalheader"
	exptostdpass "github.com/ldez/exptostd"
	wastedassignpass "github.com/sanposhiho/wastedassign/v2"
	containedctxpass "github.com/sivchari/containedctx"
	nosprintfhostportpass "github.com/stbenjam/no-sprintf-host-port/pkg/analyzer"
	zerologlintpass "github.com/ykadowak/zerologlint"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch2 attaches AnalyzerFns for the second long-tail
// wiring batch. Fifteen entries, all native `*analysis.Analyzer` exports
// from their upstream modules; settings range from none (most rows) to
// flag translation (nonamedreturns) to constructor args (asasalint,
// nilnesserr, whitespace).
//
// Like batch1, each row must also have its catalog Shape flipped from
// ShapeRegistryOnly to ShapeNative in seed.go — wireNativeFn refuses to
// silently promote, surfacing seed/wiring mismatches.
func wireAnalyzerFnsBatch2(c *catalog) {
	wireNativeFn(c, "asasalint", func(cfg any) []*analysis.Analyzer {
		// asasalint's upstream field is negated ("no builtin
		// exclusions") while the config field is positive ("use
		// builtin exclusions"). The zero-value config
		// (UseBuiltinExclusions=false, Exclude=nil) keeps the
		// upstream defaults (builtins included) intact — only flip
		// NoBuiltinExclusions when the user explicitly opted in.
		setting := asasalintpass.LinterSetting{}
		if s, ok := cfg.(*config.AsasalintSettings); ok && s != nil {
			setting.Exclude = s.Exclude
			if !s.UseBuiltinExclusions && (len(s.Exclude) > 0) {
				setting.NoBuiltinExclusions = true
			}
		}
		a, err := asasalintpass.NewAnalyzer(setting)
		if err != nil {
			// asasalint's NewAnalyzer fails only on bad regex in
			// Exclude. Surface the failure as nil so the engine
			// reports no diagnostics rather than crashing.
			return nil
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "canonicalheader", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{canonicalheaderpass.Analyzer}
	})

	wireNativeFn(c, "containedctx", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{containedctxpass.Analyzer}
	})

	wireNativeFn(c, "errname", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{errnamepass.New()}
	})

	wireNativeFn(c, "exptostd", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{exptostdpass.NewAnalyzer()}
	})

	wireNativeFn(c, "forcetypeassert", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{forcetypeassertpass.Analyzer}
	})

	wireNativeFn(c, "intrange", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{intrangepass.Analyzer}
	})

	wireNativeFn(c, "mirror", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{mirrorpass.NewAnalyzer()}
	})

	wireNativeFn(c, "nilnesserr", func(_ any) []*analysis.Analyzer {
		// nilnesserr's LinterSetting is empty today; pass the zero
		// value and surface a nil slot if upstream ever adds a
		// constructor-time validation that rejects it.
		a, err := nilnesserrpass.NewAnalyzer(nilnesserrpass.LinterSetting{})
		if err != nil {
			return nil
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "noinlineerr", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{noinlineerrpass.NewAnalyzer()}
	})

	wireNativeFn(c, "nonamedreturns", func(cfg any) []*analysis.Analyzer {
		// Like predeclared, nonamedreturns.Analyzer is a package-level
		// global; calling Flags.Set mutates its per-Analyzer FlagSet.
		a := nonamedreturnspass.Analyzer
		if s, ok := cfg.(*config.NoNamedReturnsSettings); ok && s != nil {
			if s.ReportErrorInDefer {
				_ = a.Flags.Set(nonamedreturnspass.FlagReportErrorInDefer, "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "nosprintfhostport", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(nosprintfhostportpass.Analyzer, 1)}
	})

	wireNativeFn(c, "wastedassign", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{wastedassignpass.Analyzer}
	})

	// whitespace is wired in wire_whitespace_native.go.

	wireNativeFn(c, "zerologlint", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{zerologlintpass.Analyzer}
	})
}
