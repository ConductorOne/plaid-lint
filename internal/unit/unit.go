// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// Result is what Run hands back to the CLI layer.
type Result struct {
	// Diagnostics is the post-filter, sorted diagnostic stream. In
	// ModeFactsOnly it is always empty.
	Diagnostics []output.Diagnostic

	// Warnings are non-fatal problems (analyzer panics, skipped
	// linters) the caller should surface on stderr.
	Warnings []string
}

// Run executes one unit action: analyze the declared package (or
// module), write every declared output, and return the diagnostics.
//
// Contract:
//   - Findings are data, never an error. An error return means the
//     action's inputs were unusable (infrastructure), and declared
//     outputs may not exist.
//   - On a nil error, every output named in cfg.Out exists and is
//     well-formed, whatever the findings — including packages that
//     fail to parse or type-check (those surface as `typecheck`
//     diagnostics, and the facts output is an empty fact set).
//   - Nothing is discovered: no toolchain, no network, no environment
//     other than the declared inputs. The exclusion filter reads the
//     declared source files (nolint, generated-file detection) and
//     the .golangci.yml — both declared inputs.
func Run(ctx context.Context, cfg *Config, golangci *config.Config, reg *registry.Registry, filter *exclusion.Filter) (*Result, error) {
	res := &Result{}

	var (
		diags      []output.Diagnostic
		factsBlob  []byte
		emitDiags  = cfg.EffectiveMode() != ModeFactsOnly
		pkgPath    = cfg.Package.Path
		exclStream = filter.NewStream()
	)
	if exclStream != nil {
		defer exclStream.Finish()
	}

	switch cfg.EffectiveMode() {
	case ModeModule:
		md, err := runModuleMode(cfg, golangci, reg)
		if err != nil {
			return nil, err
		}
		diags = md

	case ModeFull, ModeFactsOnly:
		cp, err := typecheck(cfg)
		if err != nil {
			return nil, err
		}

		if !cp.compiles() {
			// A package that does not compile is a result, not an
			// infrastructure failure: report syntax/type errors as
			// `typecheck` findings (golangci semantics) and skip
			// analyzers — an ill-typed package would crash or
			// mislead most of them. Facts output: empty set, so
			// dependents' actions still have their declared input.
			diags = typecheckDiagnostics(cp)
			if !emitDiags {
				// facts_only discards diagnostics, so surface the
				// situation as a warning: dependents will see an
				// empty fact set for this package.
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"package %s does not compile (%d typecheck findings); facts output is empty",
					pkgPath, len(diags)))
			}
		} else {
			roots, skipped := selectRoots(reg, cfg.EffectiveMode())
			res.Warnings = append(res.Warnings, skipped...)
			er, err := runAnalyzers(ctx, cfg, cp, roots)
			if err != nil {
				return nil, err
			}
			res.Warnings = append(res.Warnings, er.warnings...)
			factsBlob = er.facts
			if emitDiags {
				diags = er.diagnostics
			}
		}
	}

	// Filter + sort. The exclusion stream needs real on-disk paths in
	// Pos.Filename (nolint and generated-file detection read the
	// files); unit-mode diagnostics carry the paths the sources were
	// declared with, which satisfies that.
	if emitDiags && exclStream != nil {
		diags = exclStream.AddPackage(pkgPath, diags)
	}
	if !emitDiags {
		diags = nil
	}
	output.Sort(diags)
	res.Diagnostics = diags

	// Write declared outputs. SARIF first (always named), then facts.
	if err := writeSarif(cfg.Out.Sarif, res.Diagnostics); err != nil {
		return nil, err
	}
	if cfg.Out.Facts != "" {
		if err := writeFileAtomic(cfg.Out.Facts, wrapFacts(factsBlob)); err != nil {
			return nil, fmt.Errorf("unit: write facts: %w", err)
		}
	}
	return res, nil
}

// selectRoots derives the root analyzer set for the mode from the
// registry's enabled resolution. Module-scoped linters are excluded
// from package modes (they run in ModeModule; their wrappers would
// shell out to the toolchain — see moduleScopedLinters). Returns
// human-readable notes for anything skipped.
//
// In ModeFactsOnly the roots are narrowed to the fact-producing
// analyzers — see factProducers.
func selectRoots(reg *registry.Registry, mode Mode) ([]analyzerEntry, []string) {
	var roots []analyzerEntry
	var notes []string
	for _, r := range reg.Enabled() {
		if r.Analyzer == nil {
			// e.g. the `typecheck` pseudo-linter: handled by the
			// driver itself.
			continue
		}
		if isModuleScoped(r.Name) {
			notes = append(notes,
				fmt.Sprintf("linter %s is module-scoped; run a mode=module action for it", r.Name))
			continue
		}
		// Attribute diagnostics by the analyzer's own name, matching
		// the engine (output.FromAnalysis reports the analyzer name:
		// family members surface as SA1004 / ST1000 / printf, not
		// staticcheck / govet). The exclusion filter depends on this
		// convention — its staticcheck default-disabled set matches
		// check IDs, and familyByPrefix maps analyzer names back to
		// their umbrella for nolint / rules.
		roots = append(roots, analyzerEntry{linter: r.Analyzer.Name, analyzer: r.Analyzer})
	}
	if mode == ModeFactsOnly {
		roots = factProducers(roots)
	}
	return roots, notes
}

// factProducers narrows a root set to the analyzers that DECLARE fact
// types, including producers buried in a Requires closure — e.g.
// staticcheck's deprecated/purity passes, which only ever run as
// requirements of fact-less diagnostic checks like SA1019/SA4017.
// A facts_only action must export the same fact set a full run of the
// same configuration would (downstream actions consume the file
// either way), so the fact-less roots are dropped and every reachable
// fact producer is promoted to a root. Order is deterministic: roots
// arrive sorted from the registry and Requires edges are fixed slices.
func factProducers(roots []analyzerEntry) []analyzerEntry {
	seen := map[*analysis.Analyzer]bool{}
	var out []analyzerEntry
	var walk func(a *analysis.Analyzer)
	walk = func(a *analysis.Analyzer) {
		if seen[a] {
			return
		}
		seen[a] = true
		if len(a.FactTypes) > 0 {
			// A promoted producer reports under its own analyzer
			// name (the attribution only labels panic warnings in
			// facts_only mode — diagnostics are discarded).
			out = append(out, analyzerEntry{linter: a.Name, analyzer: a})
		}
		for _, req := range a.Requires {
			walk(req)
		}
	}
	for _, r := range roots {
		walk(r.analyzer)
	}
	return out
}

// typecheckDiagnostics renders parse and type errors as `typecheck`
// findings, mirroring golangci-lint's convention.
func typecheckDiagnostics(cp *checkedPackage) []output.Diagnostic {
	var diags []output.Diagnostic
	for _, pe := range parseErrorDiagnostics(cp.parseErrs) {
		diags = append(diags, output.Diagnostic{
			Linter:   "typecheck",
			Message:  pe.msg,
			Severity: output.SeverityError,
			Pos: output.Position{
				Filename: pe.pos.Filename,
				Line:     pe.pos.Line,
				Column:   pe.pos.Column,
			},
		})
	}
	for _, te := range cp.typeErrs {
		p := cp.fset.Position(te.Pos)
		diags = append(diags, output.Diagnostic{
			Linter:   "typecheck",
			Message:  te.Msg,
			Severity: output.SeverityError,
			Pos:      output.Position{Filename: p.Filename, Line: p.Line, Column: p.Column},
		})
	}
	return diags
}

// writeSarif writes the SARIF output.
func writeSarif(path string, diags []output.Diagnostic) error {
	var buf, err = renderSarif(diags)
	if err != nil {
		return fmt.Errorf("unit: render sarif: %w", err)
	}
	if err := writeFileAtomic(path, buf); err != nil {
		return fmt.Errorf("unit: write sarif: %w", err)
	}
	return nil
}

// renderSarif serializes diagnostics via the shared SARIF printer.
func renderSarif(diags []output.Diagnostic) ([]byte, error) {
	var b safeBuffer
	if err := output.NewSarif(&b).Print(diags); err != nil {
		return nil, err
	}
	return b.bytes, nil
}

// safeBuffer is a minimal io.Writer over a byte slice (bytes.Buffer
// without the extra API surface; kept tiny for clarity).
type safeBuffer struct{ bytes []byte }

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.bytes = append(b.bytes, p...)
	return len(p), nil
}

// writeFileAtomic writes body to path via a temp file + rename so a
// crashed action never leaves a torn output for the build system to
// cache.
func writeFileAtomic(path string, body []byte) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, ".plaid-unit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
