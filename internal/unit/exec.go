// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"encoding/gob"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/gopls/driverutil"
	"github.com/conductorone/plaid-lint/internal/gopls/facts"
	"github.com/conductorone/plaid-lint/internal/output"
)

// analyzerEntry pairs a registry linter name with one of its
// analyzers. The name is what diagnostics report as the Linter (a
// NativeFamily linter contributes several analyzers, each keeping its
// own analyzer name — e.g. staticcheck's SA1000 — matching the engine's
// convention of reporting the analyzer name for family members).
type analyzerEntry struct {
	linter   string
	analyzer *analysis.Analyzer
}

// execResult is what runAnalyzers hands back to the orchestrator.
type execResult struct {
	// diagnostics are pre-filter, converted diagnostics in
	// nondeterministic order; callers sort post-filter.
	diagnostics []output.Diagnostic

	// facts is the canonical fact-set encoding for the package
	// (empty when no facts were produced).
	facts []byte

	// warnings surface per-analyzer failures (panics, errors) that
	// did not abort the run.
	warnings []string
}

// action is one node of the per-package analyzer graph: an analyzer
// plus its memoized execution state. Requires edges point at other
// actions in the same map.
type action struct {
	entry analyzerEntry
	root  bool // diagnostics are collected only from root actions

	once   sync.Once
	result any
	err    error
	diags  []analysis.Diagnostic
}

// runAnalyzers executes the analyzer graph for the checked package.
//
// roots hold the analyzers whose diagnostics matter for this mode;
// their transitive Requires closure runs as needed for results. All
// analyzers share one facts.Set (the vet/unitchecker model: one fact
// blob per package, fact types namespace the analyzers).
func runAnalyzers(ctx context.Context, cfg *Config, cp *checkedPackage, roots []analyzerEntry) (*execResult, error) {
	res := &execResult{}

	// Validate the analyzer graph up front — mirrors the gopls
	// driver's analysis.Validate call and turns malformed Requires
	// graphs into a clean infrastructure error.
	uniqueRoots := make([]*analysis.Analyzer, 0, len(roots))
	seen := map[*analysis.Analyzer]bool{}
	for _, r := range roots {
		if !seen[r.analyzer] {
			seen[r.analyzer] = true
			uniqueRoots = append(uniqueRoots, r.analyzer)
		}
	}
	if err := analysis.Validate(uniqueRoots); err != nil {
		return nil, fmt.Errorf("unit: analyzer graph: %w", err)
	}

	// gob-register every fact type in the Requires closure before any
	// fact decoding or encoding — the analysis-driver contract
	// (unitchecker does the same at startup). Registration is
	// process-global and idempotent for identical types.
	registerFactTypes(uniqueRoots)

	factSet, err := decodeDepFacts(cfg, cp)
	if err != nil {
		return nil, err
	}

	// Build the action map over the Requires closure.
	actions := map[*analysis.Analyzer]*action{}
	var register func(e analyzerEntry, root bool) *action
	register = func(e analyzerEntry, root bool) *action {
		act, ok := actions[e.analyzer]
		if !ok {
			act = &action{entry: e}
			actions[e.analyzer] = act
			for _, req := range e.analyzer.Requires {
				// Required analyzers keep their own analyzer name as
				// the linter attribution; they are non-root so their
				// diagnostics are never collected.
				register(analyzerEntry{linter: req.Name, analyzer: req}, false)
			}
		}
		if root {
			act.root = true
			// Roots win the attribution: an analyzer first seen as a
			// requirement can later be promoted to a root, and the
			// root entry carries the linter name selectRoots chose
			// (the analyzer's own name, matching the engine).
			act.entry = e
		}
		return act
	}
	for _, r := range roots {
		register(r, true)
	}

	var exec func(a *analysis.Analyzer) *action
	exec = func(a *analysis.Analyzer) *action {
		act := actions[a]
		act.once.Do(func() {
			// Execute requirements first (same goroutine; the
			// parallelism comes from the root fan-out below).
			inputs := make(map[*analysis.Analyzer]any, len(a.Requires))
			for _, req := range a.Requires {
				reqAct := exec(req)
				if reqAct.err != nil {
					act.err = fmt.Errorf("requirement %s: %w", req.Name, reqAct.err)
					return
				}
				inputs[req] = reqAct.result
			}
			act.result, act.diags, act.err = runOne(cfg, cp, act.entry, inputs, factSet)
		})
		return act
	}

	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for _, r := range roots {
		g.Go(func() error {
			exec(r.analyzer)
			return nil
		})
	}
	_ = g.Wait() // individual failures are per-action warnings, below

	// Collect: root diagnostics + per-action failures as warnings.
	// Deterministic warning order: sort by analyzer name.
	names := make([]*analysis.Analyzer, 0, len(actions))
	for a := range actions {
		names = append(names, a)
	}
	sort.Slice(names, func(i, j int) bool { return names[i].Name < names[j].Name })
	for _, a := range names {
		act := actions[a]
		if act.err != nil {
			res.warnings = append(res.warnings,
				fmt.Sprintf("analyzer %s failed: %v", a.Name, act.err))
			continue
		}
		if !act.root {
			continue
		}
		for _, d := range act.diags {
			res.diagnostics = append(res.diagnostics, convertDiagnostic(cp.fset, act.entry.linter, d))
		}
	}

	res.facts = factSet.Encode()
	return res, nil
}

// runOne executes a single analyzer over the package, recovering
// panics into errors so one broken analyzer degrades to a warning
// rather than failing the action.
func runOne(cfg *Config, cp *checkedPackage, e analyzerEntry, inputs map[*analysis.Analyzer]any, factSet *facts.Set) (result any, diags []analysis.Diagnostic, err error) {
	a := e.analyzer

	factFilter := make(map[reflect.Type]bool, len(a.FactTypes))
	for _, f := range a.FactTypes {
		factFilter[reflect.TypeOf(f)] = true
	}

	pass := &analysis.Pass{
		Analyzer:     a,
		Fset:         cp.fset,
		Files:        cp.files,
		OtherFiles:   nil,
		IgnoredFiles: ignoredFiles(cfg, cp),
		Pkg:          cp.pkg,
		TypesInfo:    cp.info,
		TypesSizes:   cp.sizes,
		TypeErrors:   cp.typeErrs,
		Module:       moduleInfo(cfg),
		ResultOf:     inputs,
		Report: func(d analysis.Diagnostic) {
			if verr := driverutil.ValidateFixes(cp.fset, a, d.SuggestedFixes); verr != nil {
				d.SuggestedFixes = nil
			}
			diags = append(diags, d)
		},
		ImportObjectFact:  factSet.ImportObjectFact,
		ExportObjectFact:  factSet.ExportObjectFact,
		ImportPackageFact: factSet.ImportPackageFact,
		ExportPackageFact: factSet.ExportPackageFact,
		AllObjectFacts:    func() []analysis.ObjectFact { return factSet.AllObjectFacts(factFilter) },
		AllPackageFacts:   func() []analysis.PackageFact { return factSet.AllPackageFacts(factFilter) },
	}
	pass.ReadFile = func(filename string) ([]byte, error) {
		if cerr := driverutil.CheckReadable(pass, filename); cerr != nil {
			return nil, cerr
		}
		return os.ReadFile(filename)
	}

	defer func() {
		if r := recover(); r != nil {
			result, diags = nil, nil
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	result, err = a.Run(pass)
	if err != nil {
		return nil, nil, err
	}
	// Mirror the x/tools driver contract check exactly: the result
	// type must equal Analyzer.ResultType in both directions — an
	// untyped-nil result from an analyzer that declared a type, and a
	// non-nil result from one that declared none, are both driver
	// errors here rather than misattributed panics in a consumer's
	// pass.ResultOf assertion later.
	if got, want := reflect.TypeOf(result), a.ResultType; got != want {
		return nil, nil, fmt.Errorf("internal error: result type %v, declared %v", got, want)
	}
	return result, diags, nil
}

// ignoredFiles combines the config-declared ignored sources with the
// files the driver itself excluded by build constraints, preserving
// analysis.Pass.IgnoredFiles semantics (sources present in the
// package directory but not built).
func ignoredFiles(cfg *Config, cp *checkedPackage) []string {
	if len(cp.ignoredFiles) == 0 {
		return cfg.Package.IgnoredFiles
	}
	out := make([]string, 0, len(cfg.Package.IgnoredFiles)+len(cp.ignoredFiles))
	out = append(out, cfg.Package.IgnoredFiles...)
	out = append(out, cp.ignoredFiles...)
	return out
}

// decodeDepFacts builds the package's shared fact set from the
// declared dependency fact files. Imports without a declared file
// contribute no facts (stdlib, unlinted deps) — the nogo contract.
func decodeDepFacts(cfg *Config, cp *checkedPackage) (*facts.Set, error) {
	dec := facts.NewDecoderFunc(cp.pkg, func(pkgPath string) *types.Package {
		return cp.imports[pkgPath]
	})
	set, err := dec.Decode(func(pkgPath string) ([]byte, error) {
		file, ok := cfg.Deps.Facts[pkgPath]
		if !ok {
			return nil, nil // no facts for this dep
		}
		blob, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read facts for %q: %w", pkgPath, err)
		}
		payload, err := unwrapFacts(blob)
		if err != nil {
			return nil, fmt.Errorf("facts for %q (%s): %w", pkgPath, file, err)
		}
		return payload, nil
	})
	if err != nil {
		return nil, fmt.Errorf("unit: decode dependency facts: %w", err)
	}
	return set, nil
}

// moduleInfo builds the analysis.Pass.Module value from the unit
// config, when module identity was declared.
func moduleInfo(cfg *Config) *analysis.Module {
	if cfg.Module.Path == "" {
		return nil
	}
	return &analysis.Module{
		Path:      cfg.Module.Path,
		GoVersion: cfg.Package.GoVersion,
	}
}

// convertDiagnostic maps an analysis.Diagnostic into the printer
// shape. Positions resolve through the package FileSet; fix edits are
// carried through so SARIF consumers can apply them.
func convertDiagnostic(fset *token.FileSet, linter string, d analysis.Diagnostic) output.Diagnostic {
	out := output.Diagnostic{
		Linter:   linter,
		Message:  d.Message,
		Severity: output.SeverityError,
		Pos:      toPosition(fset, d.Pos),
	}
	for _, fix := range d.SuggestedFixes {
		of := output.SuggestedFix{Message: fix.Message}
		for _, ed := range fix.TextEdits {
			of.TextEdits = append(of.TextEdits, output.TextEdit{
				Start:   toPosition(fset, ed.Pos),
				End:     toPosition(fset, ed.End),
				NewText: string(ed.NewText),
			})
		}
		out.SuggestedFixes = append(out.SuggestedFixes, of)
	}
	for _, rel := range d.Related {
		out.Related = append(out.Related, output.RelatedInformation{
			Position: toPosition(fset, rel.Pos),
			Message:  rel.Message,
		})
	}
	return out
}

func toPosition(fset *token.FileSet, pos token.Pos) output.Position {
	if !pos.IsValid() {
		return output.Position{}
	}
	p := fset.Position(pos)
	return output.Position{Filename: p.Filename, Line: p.Line, Column: p.Column}
}

// factTypesRegistered guards gob registration: gob.Register panics
// when two DIFFERENT types share a name, but re-registering the same
// type is tolerated; the guard just avoids redundant work across
// worker requests.
var (
	factTypesMu         sync.Mutex
	factTypesRegistered = map[reflect.Type]bool{}
)

// registerFactTypes gob-registers the fact types of every analyzer
// reachable from roots through Requires edges.
func registerFactTypes(roots []*analysis.Analyzer) {
	factTypesMu.Lock()
	defer factTypesMu.Unlock()
	seen := map[*analysis.Analyzer]bool{}
	var walk func(a *analysis.Analyzer)
	walk = func(a *analysis.Analyzer) {
		if seen[a] {
			return
		}
		seen[a] = true
		for _, f := range a.FactTypes {
			t := reflect.TypeOf(f)
			if !factTypesRegistered[t] {
				factTypesRegistered[t] = true
				gob.Register(f)
			}
		}
		for _, req := range a.Requires {
			walk(req)
		}
	}
	for _, a := range roots {
		walk(a)
	}
}
