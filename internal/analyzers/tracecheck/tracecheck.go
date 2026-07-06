// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tracecheck vendors c1's tracecheck analyzer for native
// integration into plaid-lint.
//
// Upstream: github.com/ductone/ci-tools/src/tracecheck (module path
// `gitlab.com/ductone/tracecheck`). The upstream ships as a Go plugin
// (.so loaded via `plugin.Open`) consumed by golangci-lint at runtime.
// Plugin loading requires byte-identical Go toolchain + transitive
// package versions between the .so and the loader, which is fragile in
// practice. Vendoring the source as a native analyzer avoids that
// constraint and lets plaid-lint's analyzer-set treat tracecheck the
// same way it treats every other wired analyzer.
//
// Maintenance: when upstream tracecheck changes, re-sync this file
// from github.com/ductone/ci-tools/src/tracecheck/tracecheck.go.
// Behavior must match upstream verbatim — the testdata fixture under
// testdata/src/trace/ pins the diagnostic surface.
package tracecheck

import (
	"fmt"
	"go/ast"
	"strings"

	"github.com/iancoleman/strcase"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the entry point plaid-lint's registry wires. Named
// `Analyzer` (not `TraceCheck` as in upstream) to match plaid-lint's
// convention for wired-analyzer packages.
var Analyzer = &analysis.Analyzer{
	Name:     "tracecheck",
	Doc:      "finds invalid trace span names — must match the enclosing function name in snake_case",
	Run:      run,
	Requires: []*analysis.Analyzer{inspect.Analyzer},
}

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	nodeFilter := []ast.Node{
		(*ast.FuncDecl)(nil),
	}

	insp.Preorder(nodeFilter, func(n ast.Node) {
		functionDec, ok := n.(*ast.FuncDecl)
		if !ok {
			return
		}
		// A FuncDecl whose implementation lives outside Go (assembly-backed
		// functions, compiler intrinsics, //go:linkname stubs) has a nil
		// Body; this is legal AST. Skip it to avoid a nil-pointer panic.
		if functionDec.Body == nil {
			return
		}
		for _, stmt := range functionDec.Body.List {
			s, ok := stmt.(*ast.AssignStmt)
			if !ok {
				continue
			}
			for _, r := range s.Rhs {
				functionCall, ok := r.(*ast.CallExpr)
				if !ok {
					continue
				}
				if len(functionCall.Args) != 2 {
					continue
				}

				functionName, ok := functionCall.Fun.(*ast.SelectorExpr)
				if !ok || strings.ToLower(functionName.Sel.Name) != "start" {
					continue
				}

				argVal, ok := functionCall.Args[1].(*ast.BasicLit)
				if !ok {
					continue
				}

				comparisonName := functionDec.Name.Name
				snakeFunc := strcase.ToSnake(comparisonName)
				snakeFunc = strings.Replace(snakeFunc, "_i_ds", "_ids", -1)
				noQuoteArg := strings.Replace(argVal.Value, "\"", "", -1)
				traceArg := strcase.ToSnake(noQuoteArg)
				fixText := fmt.Sprintf("'%s' should be changed to '%s'", noQuoteArg, snakeFunc)

				if snakeFunc != traceArg && comparisonName != noQuoteArg {
					diag := analysis.Diagnostic{
						Pos:      argVal.Pos(),
						End:      argVal.End(),
						Category: "tracecheck",
						Message:  fmt.Sprintf("Span Name does not match Function Name. %s", fixText),
						SuggestedFixes: []analysis.SuggestedFix{{
							Message: fixText,
							TextEdits: []analysis.TextEdit{{
								Pos:     argVal.Pos(),
								End:     argVal.End(),
								NewText: []byte("\"" + snakeFunc + "\""),
							}},
						}},
					}
					pass.Report(diag)
				}
			}
		}
	})

	return nil, nil
}
