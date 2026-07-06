// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/token"

	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/analysis/facts/directives"
	"honnef.co/go/tools/analysis/facts/generated"
	"honnef.co/go/tools/analysis/lint"
	"honnef.co/go/tools/unused"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsUnusedNative attaches the in-process `unused`
// Analyzer (staticcheck U1000). Upstream `honnef.co/go/tools/unused`
// exposes `Analyzer` whose embedded `*analysis.Analyzer` returns a
// `unused.Result` rather than emitting diagnostics. The driver
// (staticcheck CLI or golangci-lint) is responsible for translating
// the per-package Used/Unused object lists into diagnostics.
//
// We wrap upstream `unused.Graph` + `SerializedGraph.Merge` at the
// per-pass level, then filter `Unused` by `Used` and emit a
// `<kind> <name> is unused` diagnostic — matching golangci-lint v2's
// `pkg/golinters/unused/unused.go` line-for-line and the subproc
// wrapper's existing message format.
//
// Cross-package "unused" detection is approximated by setting
// `ExportedIsUsed: true` (mirrors golangci's choice — see
// golangci-lint #4218 / go-tools #1474). That avoids false positives
// on exported APIs without requiring a whole-program driver.
//
// Settings: [config.UnusedSettings] knobs are passed straight through
// to `unused.Options`. Defaults match golangci's New() defaults.
//
// Message format: `<kind> <name> is unused` — same stem the subproc
// wrapper canonicalized (`func F is unused`, `var X is unused`),
// preserving c1's exclusion-rule parity.
func wireAnalyzerFnsUnusedNative(c *catalog) {
	wireNativeFn(c, "unused", func(cfg any) []*analysis.Analyzer {
		s, _ := cfg.(*config.UnusedSettings)
		return []*analysis.Analyzer{unusedAnalyzer(s)}
	})
}

func unusedAnalyzer(s *config.UnusedSettings) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name:     "unused",
		Doc:      "Checks Go code for unused constants, variables, functions and types.",
		Requires: []*analysis.Analyzer{generated.Analyzer, directives.Analyzer},
		Run: func(pass *analysis.Pass) (any, error) {
			return runUnused(pass, s)
		},
	}
}

func runUnused(pass *analysis.Pass, s *config.UnusedSettings) (any, error) {
	opts := unused.Options{
		ExportedIsUsed: true,
	}
	if s != nil {
		opts.FieldWritesAreUses = s.FieldWritesAreUses
		opts.PostStatementsAreReads = s.PostStatementsAreReads
		opts.ExportedFieldsAreUsed = s.ExportedFieldsAreUsed
		opts.ParametersAreUsed = s.ParametersAreUsed
		opts.LocalVariablesAreUsed = s.LocalVariablesAreUsed
		opts.GeneratedIsUsed = s.GeneratedIsUsed
	}

	dirs, ok := pass.ResultOf[directives.Analyzer].([]lint.Directive)
	if !ok {
		dirs = nil
	}
	gen, _ := pass.ResultOf[generated.Analyzer].(map[string]generated.Generator)

	nodes := unused.Graph(
		pass.Fset,
		pass.Files,
		pass.Pkg,
		pass.TypesInfo,
		dirs,
		gen,
		opts,
	)
	sg := unused.SerializedGraph{}
	sg.Merge(nodes)
	res := sg.Results()

	// Per-pass key + filename→*token.File index, so the
	// used-set lookup and the unusedPos resolution don't allocate a
	// Sprintf-built string per object or O(file-count) Iterate per
	// finding. With ~1500 packages in c1's controller workspace, the
	// per-Object map key + per-finding Iterate add up to a measurable
	// share of unused.Graph's wrapper-level cost (profiling attributed
	// 8.70% of cold CPU to the unused walk).
	used := make(map[unusedKeyStruct]bool, len(res.Used))
	for _, obj := range res.Used {
		used[unusedKey(obj)] = true
	}
	fileIdx := buildFileIndex(pass.Fset)
	for _, obj := range res.Unused {
		// Mirror golangci's choice to skip type params — see the
		// upstream go-tools lintcmd/lint.go cross-pkg loop which
		// folds type params into the type's own usage.
		if obj.Kind == "type param" {
			continue
		}
		if used[unusedKey(obj)] {
			continue
		}
		pass.Report(analysis.Diagnostic{
			Pos:     unusedPosFromIndex(fileIdx, obj),
			Message: fmt.Sprintf("%s %s is unused", obj.Kind, obj.Name),
		})
	}
	return nil, nil
}

// unusedKeyStruct is the comparable key for the used-set lookup. Using
// a struct value keys directly on (filename, line, name) without the
// Sprintf-built intermediate string the previous implementation
// allocated per object.
type unusedKeyStruct struct {
	filename string
	line     int
	name     string
}

func unusedKey(obj unused.Object) unusedKeyStruct {
	return unusedKeyStruct{
		filename: obj.Position.Filename,
		line:     obj.Position.Line,
		name:     obj.Name,
	}
}

// buildFileIndex returns a filename → *token.File map. The map captures
// every file visible in the pass's FileSet so the per-finding lookup
// becomes O(1) (vs the prior O(file-count) Iterate call). Built
// once per pass, then re-used for every Unused-list entry's pos lookup.
func buildFileIndex(fset *token.FileSet) map[string]*token.File {
	idx := make(map[string]*token.File)
	fset.Iterate(func(f *token.File) bool {
		idx[f.Name()] = f
		return true
	})
	return idx
}

// unusedPosFromIndex resolves the library's token.Position back to a
// token.Pos using the pre-built filename → *token.File index. Falls
// back to NoPos when the file isn't in this pass's set (cross-file
// diagnostics from cached graph data).
func unusedPosFromIndex(fileIdx map[string]*token.File, obj unused.Object) token.Pos {
	p := obj.Position
	if !p.IsValid() {
		return token.NoPos
	}
	found, ok := fileIdx[p.Filename]
	if !ok {
		return token.NoPos
	}
	if p.Line < 1 || p.Line > found.LineCount() {
		return token.NoPos
	}
	off := token.Pos(p.Column - 1)
	if p.Column < 1 {
		off = 0
	}
	return found.LineStart(p.Line) + off
}
