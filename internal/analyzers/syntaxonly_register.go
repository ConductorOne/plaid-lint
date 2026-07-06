// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import "golang.org/x/tools/go/analysis"

// RegisterSyntaxOnly registers a TypeUseSyntaxOnly descriptor for a
// against BundledRegistry and returns a so the call site can stay a
// one-liner inside an AnalyzerFn factory:
//
//	return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(newX(s), 1)}
//
// Use this when the analyzer is built dynamically by a registry wire
// function (each AnalyzerFn call produces a fresh pointer) AND its
// Run body has been audited to read only pass.Files / pass.Fset /
// pass.Report — no pass.TypesInfo, pass.Pkg, or pass.ResultOf of a
// type-providing prerequisite. See descriptor.go's TypeUseScope godoc
// for the safety contract; misuse causes stale L1 hits.
//
// cacheVersion is the per-wrapper cache-stamp (same semantics as
// AnalyzerDescriptor.CacheVersion). New wrappers should pass 1.
//
// The descriptor uses [KeyInputAllPackageSource] and a name-salted
// ConfigSalt, mirroring bundled.go's noConfigSalt(). Registration is
// idempotent on the analyzer pointer; callers that build a fresh
// instance on every AnalyzerFn invocation accumulate one descriptor
// per instance over the process lifetime, all keyed by their own
// pointer (no collisions, no leak in practice for batch runs).
func RegisterSyntaxOnly(a *analysis.Analyzer, cacheVersion uint8) *analysis.Analyzer {
	if a == nil {
		return nil
	}
	name := a.Name
	salt := ConfigSalt(name, nil)
	BundledRegistry.Register(&AnalyzerDescriptor{
		Analyzer:     a,
		KeyInputs:    []KeyInput{KeyInputAllPackageSource},
		ConfigSalt:   func(any) [32]byte { return salt },
		CacheVersion: cacheVersion,
		TypeUseScope: TypeUseSyntaxOnly,
	})
	return a
}
