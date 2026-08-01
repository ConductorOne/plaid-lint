// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/tools/go/gcexportdata"
)

// checkedPackage bundles everything the analyzer executor needs about
// the type-checked package under analysis.
type checkedPackage struct {
	fset      *token.FileSet
	files     []*ast.File
	pkg       *types.Package
	info      *types.Info
	sizes     types.Sizes
	imports   map[string]*types.Package // shared gcexportdata package map
	typeErrs  []types.Error
	parseErrs []error // scanner.ErrorList entries, one per file at most

	// goFiles are the source paths actually analyzed, post
	// constraint- and test-filtering — the package's file-set
	// identity for the SARIF run properties and the collect
	// supersede rule.
	goFiles []string

	// ignoredFiles are declared sources excluded by build
	// constraints; merged into analysis.Pass.IgnoredFiles alongside
	// any config-declared ignored files.
	ignoredFiles []string
}

// compiles reports whether the package parsed and type-checked
// cleanly. Analyzers without RunDespiteErrors only run when true.
func (p *checkedPackage) compiles() bool {
	return len(p.typeErrs) == 0 && len(p.parseErrs) == 0
}

// exportDataImporter resolves import paths through the importcfg map,
// reading gc export data lazily and memoizing through the shared
// imports map (which gcexportdata also uses to canonicalize transitive
// package references).
type exportDataImporter struct {
	fset  *token.FileSet
	paths map[string]string // importpath -> export data file

	// stdlibDir optionally resolves import paths absent from paths:
	// <stdlibDir>/<goos>_<goarch>/<importpath>.a — the layout of
	// rules_go's compiled stdlib tree (and a classic GOPATH/pkg).
	stdlibDir string
	platform  string // "<goos>_<goarch>"

	mu      sync.Mutex
	imports map[string]*types.Package

	// infraErrs collects failures reading or decoding files the
	// importcfg DECLARED. Those are broken action inputs (a wiring
	// bug, a corrupt artifact), not properties of the source under
	// analysis — they must fail the action rather than surface as
	// `typecheck` findings, or a facts_only action would go green
	// with an empty fact set and silently lose downstream findings.
	// A path simply missing from the importcfg stays a type error:
	// that is what a source file importing an undeclared package
	// legitimately looks like.
	infraErrs []error
}

func (imp *exportDataImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	imp.mu.Lock()
	defer imp.mu.Unlock()
	if pkg, ok := imp.imports[path]; ok && pkg.Complete() {
		return pkg, nil
	}
	file, ok := imp.paths[path]
	if !ok && imp.stdlibDir != "" {
		candidate := filepath.Join(imp.stdlibDir, imp.platform, filepath.FromSlash(path)+".a")
		if _, err := os.Stat(candidate); err == nil {
			file, ok = candidate, true
		}
	}
	if !ok {
		return nil, fmt.Errorf("package %q not named by importcfg", path)
	}
	f, err := os.Open(file)
	if err != nil {
		err = fmt.Errorf("open export data for %q: %w", path, err)
		imp.infraErrs = append(imp.infraErrs, err)
		return nil, err
	}
	defer f.Close()
	// NewReader locates the export data section whether the file is
	// raw export data (rules_go .x, `go tool compile -o`) or a
	// package archive (`go list -export`, .a files) — the same
	// tolerance nogo's importer has.
	r, err := gcexportdata.NewReader(f)
	if err != nil {
		err = fmt.Errorf("export data for %q (%s): %w", path, file, err)
		imp.infraErrs = append(imp.infraErrs, err)
		return nil, err
	}
	pkg, err := gcexportdata.Read(r, imp.fset, imp.imports, path)
	if err != nil {
		err = fmt.Errorf("decode export data for %q (%s): %w", path, file, err)
		imp.infraErrs = append(imp.infraErrs, err)
		return nil, err
	}
	return pkg, nil
}

// typecheck parses and type-checks the package described by cfg.
// Parse and type errors are collected, not returned: a package that
// fails to compile is a valid analysis subject (typecheck findings)
// — only infrastructure problems (unreadable files) are errors.
func typecheck(cfg *Config) (*checkedPackage, error) {
	fset := token.NewFileSet()
	cp := &checkedPackage{fset: fset}

	// Build-constraint filtering mirrors the rules_go compile
	// builder's file selection (see filterByConstraints): it runs
	// before parsing — and therefore before the test filter below —
	// so excluded files contribute neither declarations nor goFiles
	// identity. A package whose files are ALL excluded proceeds like
	// an empty-after-test-filter one: no findings, empty facts.
	srcs, ignored, err := filterByConstraints(&cfg.Package)
	if err != nil {
		return nil, err
	}
	cp.ignoredFiles = ignored

	for _, name := range srcs {
		// ParseComments: nolint directives and several analyzers need
		// them. No SkipObjectResolution: some analyzers still consult
		// ast.Object scopes.
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			if f == nil {
				// Unreadable file or catastrophic parse failure with
				// no partial AST: distinguish I/O problems
				// (infrastructure) from syntax errors (findings).
				if _, statErr := os.Stat(name); statErr != nil {
					return nil, fmt.Errorf("unit: source %s: %w", name, statErr)
				}
			}
			cp.parseErrs = append(cp.parseErrs, err)
			if f == nil {
				continue
			}
		}
		// Test filtering mirrors the rules_go compile builder's
		// applyTestFilter: a go_test's declared sources span the
		// internal and external test packages, and each archive keeps
		// only the files whose package clause matches its side of the
		// _test suffix split.
		if f.Name != nil {
			isTestPkg := strings.HasSuffix(f.Name.Name, "_test")
			switch cfg.Package.TestFilter {
			case "only":
				if !isTestPkg {
					continue
				}
			case "exclude":
				if isTestPkg {
					continue
				}
			}
		}
		cp.files = append(cp.files, f)
		cp.goFiles = append(cp.goFiles, name)
	}

	paths := map[string]string{}
	if cfg.Deps.Importcfg != "" {
		var err error
		paths, err = parseImportcfg(cfg.Deps.Importcfg)
		if err != nil {
			return nil, err
		}
	}
	imp := &exportDataImporter{
		fset:      fset,
		paths:     paths,
		stdlibDir: cfg.Deps.StdlibDir,
		platform:  cfg.Package.GOOS + "_" + cfg.Package.GOARCH,
		imports:   make(map[string]*types.Package),
	}
	cp.imports = imp.imports

	sizes := types.SizesFor("gc", cfg.Package.GOARCH)
	if sizes == nil {
		return nil, fmt.Errorf("unit: unknown GOARCH %q", cfg.Package.GOARCH)
	}
	cp.sizes = sizes

	tcfg := &types.Config{
		Importer: imp,
		Sizes:    sizes,
		Error: func(err error) {
			if te, ok := err.(types.Error); ok {
				cp.typeErrs = append(cp.typeErrs, te)
			}
		},
	}
	if v := cfg.Package.GoVersion; v != "" {
		tcfg.GoVersion = "go" + v
	}

	info := &types.Info{
		Types:        make(map[ast.Expr]types.TypeAndValue),
		Instances:    make(map[*ast.Ident]types.Instance),
		Defs:         make(map[*ast.Ident]types.Object),
		Uses:         make(map[*ast.Ident]types.Object),
		Implicits:    make(map[ast.Node]types.Object),
		Selections:   make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:       make(map[ast.Node]*types.Scope),
		FileVersions: make(map[*ast.File]string),
	}
	cp.info = info

	pkg := types.NewPackage(cfg.Package.Path, packageName(cp.files, cfg.Package.Path))
	checker := types.NewChecker(tcfg, fset, pkg, info)
	// Checker.Files returns the first error; every error already
	// arrived through tcfg.Error, so the return is redundant here.
	_ = checker.Files(cp.files)
	cp.pkg = pkg

	// Broken DECLARED inputs fail the action (see infraErrs). This is
	// checked after Files so every importer failure is collected, and
	// takes precedence over the typecheck-findings path: type errors
	// caused by an unreadable artifact are not source findings.
	if len(imp.infraErrs) > 0 {
		return nil, fmt.Errorf("unit: broken action inputs: %w", errors.Join(imp.infraErrs...))
	}

	return cp, nil
}

// packageName extracts the package clause name from the first parsed
// file, falling back to the import path's base segment when no file
// parsed (the checker still needs a named package to hang errors on).
func packageName(files []*ast.File, importPath string) string {
	for _, f := range files {
		if f != nil && f.Name != nil && f.Name.Name != "" {
			return f.Name.Name
		}
	}
	if i := lastSlash(importPath); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// parseErrorDiagnostics flattens scanner.ErrorList entries so each
// syntax error surfaces as its own diagnostic.
func parseErrorDiagnostics(errs []error) []positionedError {
	var out []positionedError
	for _, err := range errs {
		switch e := err.(type) {
		case scanner.ErrorList:
			for _, item := range e {
				out = append(out, positionedError{pos: item.Pos, msg: item.Msg})
			}
		case *scanner.Error:
			out = append(out, positionedError{pos: e.Pos, msg: e.Msg})
		default:
			out = append(out, positionedError{msg: err.Error()})
		}
	}
	return out
}

// positionedError is a (position, message) pair independent of the
// error's concrete type.
type positionedError struct {
	pos token.Position
	msg string
}
