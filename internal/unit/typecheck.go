// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
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

	mu      sync.Mutex
	imports map[string]*types.Package
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
	if !ok {
		return nil, fmt.Errorf("package %q not named by importcfg", path)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("open export data for %q: %w", path, err)
	}
	defer f.Close()
	// NewReader locates the export data section whether the file is
	// raw export data (rules_go .x, `go tool compile -o`) or a
	// package archive (`go list -export`, .a files) — the same
	// tolerance nogo's importer has.
	r, err := gcexportdata.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("export data for %q (%s): %w", path, file, err)
	}
	pkg, err := gcexportdata.Read(r, imp.fset, imp.imports, path)
	if err != nil {
		return nil, fmt.Errorf("decode export data for %q (%s): %w", path, file, err)
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

	for _, name := range cfg.Package.GoFiles {
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
		cp.files = append(cp.files, f)
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
		fset:    fset,
		paths:   paths,
		imports: make(map[string]*types.Package),
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
