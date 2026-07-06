// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/packages"
	"mvdan.cc/unparam/check"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsUnparamNative attaches the in-process `unparam`
// Analyzer. Upstream `mvdan.cc/unparam/check` exposes a `Checker`
// type that consumes a `*packages.Package` + a `*ssa.Program` and
// returns `[]Issue` with `Pos() token.Pos` and `Message() string`.
//
// We reconstruct a `*packages.Package` from `pass.Fset/Files/Pkg/
// TypesInfo` and pull the SSA program from `buildssa.Analyzer`'s
// result — mirrors golangci-lint v2's
// `pkg/golinters/unparam/unparam.go` line-for-line.
//
// Settings: [config.UnparamSettings.CheckExported] threads through
// to `Checker.CheckExportedFuncs`. The message format upstream
// emits (e.g. `<f> - result 0 (error) is always nil`) matches the
// subproc wrapper's canonicalized output so c1 exclusion rules
// over the diagnostic stem continue to apply.
func wireAnalyzerFnsUnparamNative(c *catalog) {
	wireNativeFn(c, "unparam", func(cfg any) []*analysis.Analyzer {
		checkExported := false
		if s, ok := cfg.(*config.UnparamSettings); ok && s != nil {
			checkExported = s.CheckExported
		}
		return []*analysis.Analyzer{unparamAnalyzer(checkExported)}
	})
}

func unparamAnalyzer(checkExported bool) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:     "unparam",
		Doc:      "Reports unused function parameters.",
		Requires: []*analysis.Analyzer{buildssa.Analyzer},
		Run: func(pass *analysis.Pass) (any, error) {
			return runUnparam(pass, checkExported)
		},
	}
}

func runUnparam(pass *analysis.Pass, checkExported bool) (any, error) {
	ssa, ok := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	if !ok || ssa == nil || ssa.Pkg == nil {
		return nil, nil
	}

	pkg := &packages.Package{
		Fset:      pass.Fset,
		Syntax:    pass.Files,
		Types:     pass.Pkg,
		TypesInfo: pass.TypesInfo,
	}

	c := &check.Checker{}
	c.CheckExportedFuncs(checkExported)
	c.Packages([]*packages.Package{pkg})
	c.ProgramSSA(ssa.Pkg.Prog)

	issues, err := c.Check()
	if err != nil {
		return nil, err
	}
	for _, i := range issues {
		pass.Report(analysis.Diagnostic{
			Pos:     i.Pos(),
			Message: i.Message(),
		})
	}
	return nil, nil
}
