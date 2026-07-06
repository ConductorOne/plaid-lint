// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"fmt"
	"unsafe"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// runPlan is the per-Run decomposition of the resolved registry
// into the two execution paths: in-process analyzers and
// subprocess runners.
type runPlan struct {
	// analyzers carries every runnable in-process *analysis.Analyzer
	// pointer plus its registry name (kept for the warning surface
	// only — settings.AllAnalyzers consumes pointers, not names).
	analyzers []analyzerEntry

	// subproc is the list of constructed subprocess wrappers
	// (DuplRunner, CustomRunner). The engine invokes each once per
	// Run.
	subproc []subproc.Runner

	// warnings is the human-readable warning surface — typically
	// "ShapeRegistryOnly linter '<name>' skipped: no AnalyzerFn
	// wired and no CustomLinterSettings payload."
	warnings []string
}

// analyzerEntry pairs an *analysis.Analyzer with the registry
// linter name. Multiple entries may share the same pointer (e.g.
// gosimple aliased to staticcheck's S-* checks); wrapAnalyzers
// dedups by pointer when building settings.AllAnalyzers.
type analyzerEntry struct {
	name     string
	analyzer *analysis.Analyzer
}

// planFromRegistry walks the Registry.Enabled() set and assigns
// each Resolved row to one of the three execution paths:
//
//   - ShapeNative / ShapeNativeFamily with an Analyzer pointer:
//     in-process. Append to plan.analyzers.
//
//   - ShapeSubprocess with name == "unused" / "unparam": construct
//     the matching subproc.Runner. (CustomRunner is constructed
//     for ShapeRegistryOnly rows carrying a CustomLinterSettings
//     payload — see below.)
//
//   - ShapeRegistryOnly:
//
//   - If Settings is a config.CustomLinterSettings: construct a
//     CustomRunner.
//
//   - Otherwise: emit a warning and skip. These are the ~95
//     long-tail linters that still need a per-linter wiring PR.
//
// ShapeFormatter / StatusDisabled rows are not surfaced by
// Registry.Enabled() so they cannot reach this function.
func planFromRegistry(reg *registry.Registry) *runPlan {
	plan := &runPlan{}
	if reg == nil {
		return plan
	}
	for _, r := range reg.Enabled() {
		switch r.Shape {
		case registry.ShapeNative, registry.ShapeNativeFamily:
			if r.Analyzer == nil {
				continue
			}
			plan.analyzers = append(plan.analyzers, analyzerEntry{
				name:     r.Name,
				analyzer: r.Analyzer,
			})

		case registry.ShapeSubprocess:
			// ShapeSubprocess rows reach Enabled() when the catalog
			// flagged the row SubprocessWired=true (see build.go).
			// Each linter dispatches to its dedicated Runner. Only
			// `dupl` and `tracecheck` remain as ShapeSubprocess; the
			// rest have been ported to ShapeNative.
			switch r.Name {
			// gochecknoinits, dogsled, lll removed — now ShapeNative inline.
			// godox, gocyclo, nestif, unconvert removed — now ShapeNative library-wrap.
			// unused, unparam removed — now ShapeNative library-wrap.
			// dupl removed — now ShapeNative per-pass (see wire_dupl_native.go).
			// Only tracecheck remains as ShapeSubprocess (user custom plugin via T3.4).
			default:
				plan.warnings = append(plan.warnings,
					fmt.Sprintf("subprocess linter %q has no wrapper", r.Name))
			}

		case registry.ShapeRegistryOnly:
			if cs, ok := asCustomLinterSettings(r.Settings); ok {
				plan.subproc = append(plan.subproc, subproc.NewCustomRunner(r.Name, cs, nil))
				continue
			}
			plan.warnings = append(plan.warnings,
				fmt.Sprintf("ShapeRegistryOnly linter %q skipped: no AnalyzerFn wired", r.Name))
		}
	}
	return plan
}

// asCustomLinterSettings recovers a CustomLinterSettings from the
// registry's `any` settings field. The registry passes the typed
// per-linter sub-block; for a custom plugin that's the
// CustomLinterSettings value (not a pointer).
func asCustomLinterSettings(s any) (config.CustomLinterSettings, bool) {
	switch v := s.(type) {
	case config.CustomLinterSettings:
		return v, true
	case *config.CustomLinterSettings:
		if v == nil {
			return config.CustomLinterSettings{}, false
		}
		return *v, true
	default:
		return config.CustomLinterSettings{}, false
	}
}

// analyzerKey returns a stable map key for an *analysis.Analyzer.
// We use the pointer's address so dedup works regardless of
// equality of the underlying Analyzer struct (which is not
// hashable in the general case).
func analyzerKey(a *analysis.Analyzer) uintptr {
	return uintptr(unsafe.Pointer(a))
}
