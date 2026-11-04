// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
)

// StdlibConfig describes one `plaid-lint stdlib` invocation: export
// data for every standard-library package of the SDK at GOROOT, built
// for one {GOOS, GOARCH, tags, cgo} configuration.
type StdlibConfig struct {
	// GOROOT is the Go SDK root; sources are read from GOROOT/src.
	// Nothing else under it is consulted (no compiled packages, no go
	// command).
	GOROOT string

	GOOS   string
	GOARCH string
	Tags   []string
	Cgo    bool

	// GoVersion is the SDK's language version ("1.26"). Empty means
	// "read GOROOT/VERSION".
	GoVersion string

	// Out is the output directory. Export data for package P lands at
	// Out/<GOOS>_<GOARCH>/<P>.a — the layout DepsConfig.StdlibDir
	// resolves — as raw gcexportdata.
	Out string

	// Jobs bounds type-checking parallelism; <= 0 means GOMAXPROCS.
	Jobs int

	// Files, when non-empty, is the complete set of SDK source files
	// the run may read (paths spelled as GOROOT/src/...). Anything
	// else found under GOROOT/src is ignored, so the output depends
	// only on the declared inputs whether or not the executor
	// sandboxes the action — a build system's SDK file group
	// typically omits test-only packages that an unsandboxed walk
	// would otherwise see.
	Files []string
}

// StdlibResult reports what an ExportStdlib run produced.
type StdlibResult struct {
	// Packages are the import paths exported, sorted.
	Packages []string

	// Skipped maps directories under GOROOT/src that hold Go files but
	// no buildable package for the configuration (e.g. runtime/cgo
	// with cgo off) to the reason. They are skipped the same way
	// `go install std` skips them: nothing can import them in that
	// configuration either.
	Skipped map[string]string

	// Warnings are non-fatal problems: packages that did not
	// type-check cleanly (their export data describes what the checker
	// could type) and cgo packages type-checked approximately.
	Warnings []string
}

// stdPkg is one standard-library package queued for type-checking.
type stdPkg struct {
	path    string // import path, e.g. "vendor/golang.org/x/net/route"
	files   []string
	imports []string // resolved import paths of other std packages
	cgo     bool     // keeps import-"C" files (cgo enabled)

	done chan struct{}
	pkg  *types.Package
}

// ExportStdlib type-checks the SDK's standard library from source, in
// dependency order, and writes deep export data for every package.
//
// Deep export data from go/types is all a type-checker consumer needs
// from the standard library; compiling it (rules_go's GoStdlib action)
// additionally runs the backend and links nothing the linter reads.
//
// Each package is checked against the in-memory *types.Package of its
// dependencies, so the universe is consistent and every export is
// deep. File selection mirrors the go command's for std: build
// constraints under the configured GOOS/GOARCH/tags, the SDK's release
// tags, and — when the SDK is this binary's own release — its
// goexperiment tool tags; files importing "C" drop out with cgo off.
// With cgo on, cgo files are type-checked with go/types' FakeImportC,
// which types the package's Go-visible API exactly and C-typed
// internals as invalid; those packages are named in the warnings.
func ExportStdlib(ctx context.Context, cfg StdlibConfig) (*StdlibResult, error) {
	if cfg.GOROOT == "" || cfg.GOOS == "" || cfg.GOARCH == "" || cfg.Out == "" {
		return nil, errors.New("stdlib: goroot, goos, goarch and out are required")
	}
	sizes := types.SizesFor("gc", cfg.GOARCH)
	if sizes == nil {
		return nil, fmt.Errorf("stdlib: unknown GOARCH %q", cfg.GOARCH)
	}
	goVersion := cfg.GoVersion
	if goVersion == "" {
		v, err := sdkVersion(cfg.GOROOT)
		if err != nil {
			return nil, err
		}
		goVersion = v
	}
	bctx := build.Context{
		GOOS:        cfg.GOOS,
		GOARCH:      cfg.GOARCH,
		BuildTags:   cfg.Tags,
		Compiler:    "gc",
		CgoEnabled:  cfg.Cgo,
		ReleaseTags: releaseTags(goVersion),
	}
	if sameRelease(goVersion, runtime.Version()) {
		// goexperiment.* tags are compiled into a toolchain. They are
		// only knowable for the SDK this binary was built with; for
		// any other SDK no experiment tags apply (the experiment-off
		// file variants), which leaves exported APIs unaffected.
		bctx.ToolTags = build.Default.ToolTags
	}

	srcRoot := filepath.Join(cfg.GOROOT, "src")
	res := &StdlibResult{Skipped: map[string]string{}}
	var allowed map[string]bool
	if len(cfg.Files) > 0 {
		allowed = make(map[string]bool, len(cfg.Files))
		for _, f := range cfg.Files {
			allowed[filepath.Clean(f)] = true
		}
	}
	pkgs, err := discoverStdlib(&bctx, srcRoot, allowed, res)
	if err != nil {
		return nil, err
	}

	fset := token.NewFileSet()
	jobs := cfg.Jobs
	if jobs <= 0 {
		jobs = runtime.GOMAXPROCS(0)
	}
	var (
		mu       sync.Mutex
		warnings []string
	)
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, jobs)
	for _, p := range pkgs {
		g.Go(func() error {
			for _, dep := range p.imports {
				select {
				case <-pkgs[dep].done:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()
			defer close(p.done)

			pkg, errs, err := checkStdPkg(fset, p, pkgs, sizes, goVersion)
			if err != nil {
				return err
			}
			p.pkg = pkg
			blob, err := writeExportData(fset, pkg)
			if err != nil {
				return fmt.Errorf("stdlib: encode export data for %s: %w", p.path, err)
			}
			dest := stdlibPathOf(cfg.Out, cfg.GOOS, cfg.GOARCH, p.path)
			if err := writeFileAtomic(dest, blob); err != nil {
				return fmt.Errorf("stdlib: write %s: %w", dest, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(errs) > 0 {
				warnings = append(warnings, fmt.Sprintf("package %s: %d type errors (first: %v)", p.path, len(errs), errs[0]))
			}
			if p.cgo {
				warnings = append(warnings, fmt.Sprintf("package %s: cgo files type-checked with FakeImportC (C-typed internals are invalid types)", p.path))
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	sort.Strings(warnings)
	res.Warnings = warnings
	for ip := range pkgs {
		res.Packages = append(res.Packages, ip)
	}
	sort.Strings(res.Packages)
	return res, nil
}

// checkStdPkg parses and type-checks one std package against its
// already-checked dependencies.
func checkStdPkg(fset *token.FileSet, p *stdPkg, all map[string]*stdPkg, sizes types.Sizes, goVersion string) (*types.Package, []error, error) {
	var files []*ast.File
	var errs []error
	for _, name := range p.files {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			if f == nil {
				return nil, nil, fmt.Errorf("stdlib: parse %s: %w", name, err)
			}
			errs = append(errs, err)
		}
		files = append(files, f)
	}
	tcfg := &types.Config{
		Importer:    stdImporter{from: p.path, all: all},
		Sizes:       sizes,
		GoVersion:   "go" + goVersion,
		FakeImportC: p.cgo,
		Error:       func(err error) { errs = append(errs, err) },
	}
	pkg := types.NewPackage(p.path, packageName(files, p.path))
	_ = types.NewChecker(tcfg, fset, pkg, nil).Files(files)
	pkg.MarkComplete()
	return pkg, errs, nil
}

// stdImporter resolves a std package's imports to the in-memory
// packages checked before it.
type stdImporter struct {
	from string
	all  map[string]*stdPkg
}

func (imp stdImporter) Import(ip string) (*types.Package, error) {
	if ip == "unsafe" {
		return types.Unsafe, nil
	}
	resolved := resolveStdImport(ip, imp.all)
	dep, ok := imp.all[resolved]
	if !ok || dep.pkg == nil {
		return nil, fmt.Errorf("std package %s imports %q, which is not in the standard library for this configuration", imp.from, ip)
	}
	return dep.pkg, nil
}

// resolveStdImport applies GOROOT/src/vendor resolution: std code
// imports golang.org/x/... by its upstream path, and the go command
// resolves it to the vendored copy "vendor/golang.org/x/...". That
// vendored path is the package's identity in compiler export data,
// so it is the identity here too.
func resolveStdImport(ip string, all map[string]*stdPkg) string {
	if _, ok := all["vendor/"+ip]; ok {
		return "vendor/" + ip
	}
	return ip
}

// discoverStdlib lists every buildable std package under srcRoot with
// its selected files and std imports. cmd/ is not part of the standard
// library (user code cannot import it); builtin and unsafe are
// pseudo-packages; testdata and _/. directories are ignored as the go
// command ignores them.
func discoverStdlib(bctx *build.Context, srcRoot string, allowed map[string]bool, res *StdlibResult) (map[string]*stdPkg, error) {
	pkgs := map[string]*stdPkg{}
	rawImports := map[string][]string{}
	err := filepath.WalkDir(srcRoot, func(dir string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(srcRoot, dir)
		if err != nil {
			return err
		}
		ip := filepath.ToSlash(rel)
		if ip == "." {
			return nil
		}
		base := d.Name()
		if base == "testdata" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") ||
			ip == "cmd" || ip == "builtin" || ip == "unsafe" {
			return filepath.SkipDir
		}
		files, imports, hasGo, err := selectStdFiles(bctx, dir, allowed)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			if hasGo {
				res.Skipped[ip] = "build constraints exclude all Go files"
			}
			return nil
		}
		p := &stdPkg{path: ip, files: files, done: make(chan struct{})}
		pkgs[ip] = p
		rawImports[ip] = imports
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("stdlib: scan %s: %w", srcRoot, err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("stdlib: no packages under %s", srcRoot)
	}
	for ip, p := range pkgs {
		seen := map[string]bool{}
		for _, imp := range rawImports[ip] {
			switch imp {
			case "unsafe":
				continue
			case "C":
				p.cgo = true
				continue
			}
			r := resolveStdImport(imp, pkgs)
			if _, ok := pkgs[r]; !ok {
				// Left to the type-checker, which reports it against
				// the importing file; the package still exports.
				continue
			}
			if r == ip || seen[r] {
				continue
			}
			seen[r] = true
			p.imports = append(p.imports, r)
		}
		sort.Strings(p.imports)
	}
	if cyc := findCycle(pkgs); cyc != "" {
		return nil, fmt.Errorf("stdlib: import cycle: %s", cyc)
	}
	return pkgs, nil
}

// selectStdFiles returns the non-test Go files in dir that build under
// bctx, plus their imports. hasGo reports whether dir held any
// non-test Go file at all (so an all-excluded package can be named).
// A non-nil allowed set restricts the files considered.
func selectStdFiles(bctx *build.Context, dir string, allowed map[string]bool) (files, imports []string, hasGo bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, false, err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if allowed != nil && !allowed[filepath.Join(dir, name)] {
			continue
		}
		hasGo = true
		ok, err := bctx.MatchFile(dir, name)
		if err != nil {
			return nil, nil, false, fmt.Errorf("stdlib: %s: %w", filepath.Join(dir, name), err)
		}
		if !ok {
			continue
		}
		full := filepath.Join(dir, name)
		f, err := parser.ParseFile(token.NewFileSet(), full, nil, parser.ImportsOnly)
		if err != nil {
			return nil, nil, false, fmt.Errorf("stdlib: parse imports of %s: %w", full, err)
		}
		isCgo := false
		var fileImports []string
		for _, spec := range f.Imports {
			ip, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			if ip == "C" {
				isCgo = true
			}
			fileImports = append(fileImports, ip)
		}
		if isCgo && !bctx.CgoEnabled {
			continue
		}
		files = append(files, full)
		for _, ip := range fileImports {
			if !seen[ip] {
				seen[ip] = true
				imports = append(imports, ip)
			}
		}
	}
	return files, imports, hasGo, nil
}

// findCycle returns a description of an import cycle among pkgs, or ""
// if the graph is acyclic. A cycle would deadlock the scheduler, so it
// is rejected up front.
func findCycle(pkgs map[string]*stdPkg) string {
	const (
		unvisited = iota
		active
		finished
	)
	state := map[string]int{}
	var stack []string
	var visit func(ip string) string
	visit = func(ip string) string {
		switch state[ip] {
		case active:
			return strings.Join(append(stack, ip), " -> ")
		case finished:
			return ""
		}
		state[ip] = active
		stack = append(stack, ip)
		for _, dep := range pkgs[ip].imports {
			if c := visit(dep); c != "" {
				return c
			}
		}
		stack = stack[:len(stack)-1]
		state[ip] = finished
		return ""
	}
	keys := make([]string, 0, len(pkgs))
	for ip := range pkgs {
		keys = append(keys, ip)
	}
	sort.Strings(keys)
	for _, ip := range keys {
		if c := visit(ip); c != "" {
			return c
		}
	}
	return ""
}

// sdkVersion reads the language version ("1.26") from GOROOT/VERSION,
// whose first line is "go1.26.3" (or a prerelease like "go1.27rc1").
func sdkVersion(goroot string) (string, error) {
	body, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("stdlib: read SDK version: %w", err)
	}
	line, _, _ := strings.Cut(string(body), "\n")
	v, ok := strings.CutPrefix(strings.TrimSpace(line), "go")
	if !ok {
		return "", fmt.Errorf("stdlib: unrecognized SDK VERSION %q", line)
	}
	return majorMinor(v), nil
}

// majorMinor trims a Go version to its language version:
// "1.26.3" -> "1.26", "1.27rc1" -> "1.27".
func majorMinor(v string) string {
	major, rest, ok := strings.Cut(v, ".")
	if !ok {
		return v
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	return major + "." + rest[:end]
}

// sameRelease reports whether the SDK language version matches the
// toolchain this binary was built with (runtime.Version, "go1.26.3").
func sameRelease(goVersion, runtimeVersion string) bool {
	v, ok := strings.CutPrefix(runtimeVersion, "go")
	if !ok {
		return false
	}
	return majorMinor(v) == goVersion
}

// stdlibPathOf is the export file for import path ip under a
// StdlibConfig.Out tree: the path exportDataImporter probes for
// DepsConfig.StdlibDir.
func stdlibPathOf(out, goos, goarch, ip string) string {
	return filepath.Join(out, goos+"_"+goarch, filepath.FromSlash(path.Clean(ip))+".a")
}
