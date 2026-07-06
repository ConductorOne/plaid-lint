// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// Reflective adapter for honnef's buildir.IR result type.
//
// buildir.IR lives under honnef.co/go/tools/internal/passes/buildir, an
// internal/ package plaid-lint cannot import directly. The analysis
// framework's result-type check (analysis.go around line 1887) compares
// reflect.TypeOf(result) against pass.Analyzer.ResultType pointer-equality,
// so M1's shared-buildir dispatch must hand back a value of exactly that
// type. We construct it via reflection.
//
// buildir.IR is shaped { Pkg *ir.Package; SrcFuncs []*ir.Function } at
// honnef v0.6.1. A future bump that adds fields would silently drop them
// under reflection, so a package-init assertion pins the shape and panics
// on mismatch.

import (
	"fmt"
	"reflect"

	"honnef.co/go/tools/go/ir"
)

// buildirIRType is the reflect.Type of *buildir.IR, captured at package
// init from the buildir analyzer's ResultType. We don't import buildir
// directly because it lives under honnef.co/go/tools/internal/.
var buildirIRType reflect.Type

// buildirIRFieldPkg / buildirIRFieldSrcFuncs are the reflect field indices
// within the buildir.IR struct, resolved at init.
var (
	buildirIRFieldPkg      int
	buildirIRFieldSrcFuncs int
)

// expectedBuildirIRNumFields pins the struct shape we built M1 against.
// If honnef bumps and adds/removes a field, initBuildirIRShape will panic
// at startup rather than silently corrupting the IR result.
const expectedBuildirIRNumFields = 2

// initBuildirIRShape resolves the buildir.IR reflect.Type from a known
// buildir-consuming analyzer (sa1000's Requires[0]) and asserts the
// struct shape. Called once from the package-init guard installed in
// shared_buildir.go.
//
// We pass the buildir analyzer pointer rather than importing
// honnef.co/go/tools/internal/passes/buildir because that path is
// internal/ and plaid cannot import it.
func initBuildirIRShape(buildirAnalyzerResultType reflect.Type) {
	// ResultType is reflect.TypeOf(new(IR)) — a *IR pointer type.
	if buildirAnalyzerResultType.Kind() != reflect.Ptr {
		panic(fmt.Sprintf("plaid-lint M1: buildir.Analyzer.ResultType is %v (kind %v), want pointer", buildirAnalyzerResultType, buildirAnalyzerResultType.Kind()))
	}
	elem := buildirAnalyzerResultType.Elem()
	if elem.Kind() != reflect.Struct {
		panic(fmt.Sprintf("plaid-lint M1: buildir.IR elem kind is %v, want struct", elem.Kind()))
	}

	if got := elem.NumField(); got != expectedBuildirIRNumFields {
		panic(fmt.Sprintf("plaid-lint M1: buildir.IR has %d fields, want %d. "+
			"An honnef bump may have changed the struct shape; M1's reflective "+
			"constructor needs review before this version can ship.",
			got, expectedBuildirIRNumFields))
	}

	pkgField, ok := elem.FieldByName("Pkg")
	if !ok {
		panic("plaid-lint M1: buildir.IR has no Pkg field; honnef struct shape changed")
	}
	if want := reflect.TypeOf((*ir.Package)(nil)); pkgField.Type != want {
		panic(fmt.Sprintf("plaid-lint M1: buildir.IR.Pkg type is %v, want %v", pkgField.Type, want))
	}

	srcFuncsField, ok := elem.FieldByName("SrcFuncs")
	if !ok {
		panic("plaid-lint M1: buildir.IR has no SrcFuncs field; honnef struct shape changed")
	}
	if want := reflect.TypeOf([]*ir.Function(nil)); srcFuncsField.Type != want {
		panic(fmt.Sprintf("plaid-lint M1: buildir.IR.SrcFuncs type is %v, want %v", srcFuncsField.Type, want))
	}

	buildirIRType = elem
	buildirIRFieldPkg = pkgField.Index[0]
	buildirIRFieldSrcFuncs = srcFuncsField.Index[0]
}

// newBuildirIR allocates a *buildir.IR via reflection and populates its
// two fields. Returns the value as `any` so the analysis-framework's
// reflect.TypeOf result-type check succeeds.
//
// Caller must invoke initBuildirIRShape (via ensureBuildirIRShape) first.
func newBuildirIR(pkg *ir.Package, srcFuncs []*ir.Function) any {
	if buildirIRType == nil {
		panic("plaid-lint M1: buildir.IR shape not initialized; ensureBuildirIRShape was not called")
	}
	ptr := reflect.New(buildirIRType) // *IR
	elem := ptr.Elem()
	elem.Field(buildirIRFieldPkg).Set(reflect.ValueOf(pkg))
	elem.Field(buildirIRFieldSrcFuncs).Set(reflect.ValueOf(srcFuncs))
	return ptr.Interface()
}
