// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"strings"
	"time"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/canonicalpath"
	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/engine"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/l0"
	"github.com/conductorone/plaid-lint/internal/memlimit"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/quiet"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/subproc"
)

// runRun executes the `plaid-lint run` subcommand. Returns the
// process exit code.
//
// Flow (per the dispatch spec):
//  1. Parse CLI args.
//  2. Load config via T2.1's LoadDirs/Load.
//  3. Apply CLI overlay via T2.1's Merge.
//  4. Validate via config.Validate.
//  5. Build registry via T2.3's BuildFromConfig (stubbed for T2.4).
//  6. Run the engine (stubbed; emits no diagnostics in T2.4).
//  7. Print diagnostics via T2.2's output.NewPrinter.
//  8. Return appropriate exit code.
func (a *app) runRun(args []string) int {
	fs := newRunFlagSet("run", a.stderr)
	g := bindGlobalFlags(fs)
	rf := bindRunFlags(fs)

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError prints its own diagnostic to fs.Output.
		return exitCLIError
	}
	if g.Help {
		printRunHelp(a.stdout)
		return exitSuccess
	}
	rf.recordSetFlags(fs)
	rf.Args = fs.Args()

	// Install the stderr filter before the engine spins up the
	// analyzer driver: that's where upstream `honnef.co/go/tools`'s
	// `unused/serialize.go:trace()` emits millions of debug lines per
	// cold trial. Either `--quiet` or `LOG_LEVEL=warn` (or "error" /
	// "warning") triggers the filter.
	if g.Quiet || quiet.FromEnv() {
		restore := quiet.Install()
		defer restore()
	}

	// Pin the runtime's soft memory ceiling at 75% of the cgroup's
	// memory limit before any cache opens or analyzer drivers spin up:
	// debug.SetMemoryLimit only affects subsequent allocations, and the
	// cold-seed IR graph on c1's workspace peaks at ~52 GB without a
	// ceiling. No-op if the user already set GOMEMLIMIT or set
	// PLAID_DISABLE_AUTO_GOMEMLIMIT=1.
	memlimit.Apply()

	// Auto-route the analyzer cache through gocacheprog when GOCACHEPROG
	// is set. Deployers who configured a side-car (typically an S3 wrapper)
	// almost always want plaid's L0/L2 caches sharing through the same
	// path. No-op if the user explicitly set PLAID_CACHE_BACKEND or set
	// PLAID_DISABLE_AUTO_CACHE_BACKEND=1. L1 stays local under the carve-out.
	memlimit.ApplyCacheBackend()

	// CPU + heap profile capture. Both flags are wired here so a single
	// `plaid-lint run --cpu-profile-path=foo --mem-profile-path=bar`
	// invocation produces standard pprof artifacts the operator can
	// load with `go tool pprof`. Engine-side no-op when neither flag is
	// set; otherwise we emit warnings (not errors) on failure so a
	// missing-directory typo doesn't fail an otherwise-valid lint run.
	if rf.CPUProfilePath != "" {
		cf, cerr := os.Create(rf.CPUProfilePath)
		if cerr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: warning: open cpu profile: %v\n", cerr)
		} else {
			if perr := pprof.StartCPUProfile(cf); perr != nil {
				fmt.Fprintf(a.stderr, "plaid-lint: warning: start cpu profile: %v\n", perr)
				_ = cf.Close()
			} else {
				defer func() {
					pprof.StopCPUProfile()
					_ = cf.Close()
				}()
			}
		}
	}
	if rf.TracePath != "" {
		tf, terr := os.Create(rf.TracePath)
		if terr != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: warning: open trace: %v\n", terr)
		} else {
			if serr := trace.Start(tf); serr != nil {
				fmt.Fprintf(a.stderr, "plaid-lint: warning: start trace: %v\n", serr)
				_ = tf.Close()
			} else {
				defer func() {
					trace.Stop()
					_ = tf.Close()
				}()
			}
		}
	}
	if rf.MemProfilePath != "" {
		defer func() {
			mf, merr := os.Create(rf.MemProfilePath)
			if merr != nil {
				fmt.Fprintf(a.stderr, "plaid-lint: warning: open mem profile: %v\n", merr)
				return
			}
			runtime.GC()
			if perr := pprof.WriteHeapProfile(mf); perr != nil {
				fmt.Fprintf(a.stderr, "plaid-lint: warning: write mem profile: %v\n", perr)
			}
			_ = mf.Close()
		}()
	}

	// Steps 2 + 3: load + overlay.
	cfg, warnings, cfgPath, err := loadConfig(rf)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitConfigError
	}
	for _, w := range warnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: %s: %s\n", w.Field, w.Message)
	}
	if g.Verbose && cfgPath != "" {
		fmt.Fprintf(a.stderr, "plaid-lint: using config %s\n", cfgPath)
	}
	cfg = rf.applyOverlay(cfg)

	// Step 4: validate.
	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(a.stderr, "plaid-lint: config error: %v\n", e)
		}
		return exitConfigError
	}

	// Step 5: build registry (T2.3 stub).
	reg, regWarnings, err := registry.BuildFromConfig(cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitInternalError
	}
	for _, w := range regWarnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: %s: %s\n", w.Field, w.Message)
	}

	// Step 6: build the exclusion filter and run the engine. The
	// filter is passed into engine.Run so the cold path applies
	// every stage per-package as the analyzer driver finishes — L0
	// stores the POST-FILTER stream, warm hits serve cached
	// diagnostics directly.
	moduleRoot, mrErr := resolveModuleRoot()
	if mrErr != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: resolve module root: %v\n", mrErr)
		return exitInternalError
	}
	filter, fErr := exclusion.NewFilter(cfg, moduleRoot, rf.Args)
	if fErr != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: exclusion filter: %v\n", fErr)
		return exitInternalError
	}
	runStart := time.Now()
	diags, engineWarnings, runMetrics, engineErr := runEngine(a, cfg, reg, rf.Args, filter)
	if engineErr != nil {
		if errors.Is(engineErr, context.DeadlineExceeded) {
			fmt.Fprintf(a.stderr, "plaid-lint: timeout exceeded after %s; raise --timeout or run.timeout\n", time.Duration(cfg.Run.Timeout))
		} else {
			fmt.Fprintf(a.stderr, "plaid-lint: engine: %v\n", engineErr)
		}
		return exitInternalError
	}
	for _, w := range engineWarnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: %s\n", w)
	}
	if rf.MetricsJSONPath != "" {
		if err := writeMetricsJSON(rf.MetricsJSONPath, runMetrics, diags, time.Since(runStart)); err != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: warning: write metrics json: %v\n", err)
		}
	}

	// Step 7: print diagnostics. Multiple printers when more than one
	// output.formats.<x>.path is configured.
	if err := emitDiagnostics(a.stdout, cfg, diags, rf); err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: print: %v\n", err)
		return exitInternalError
	}

	// Step 8: exit code.
	if len(diags) == 0 {
		return exitSuccess
	}
	exit := exitIssuesFound
	if v := rf.IssuesExitCode.Value(); v != nil {
		exit = *v
	} else if cfg.Run.ExitCodeIfIssuesFound != 0 {
		exit = cfg.Run.ExitCodeIfIssuesFound
	}
	return exit
}

// loadConfig resolves the config file according to --config /
// --no-config semantics and returns the parsed Config plus the path
// it was loaded from (empty when no file was found or --no-config).
func loadConfig(rf *runFlags) (*config.Config, []config.Warning, string, error) {
	if rf.NoConfig {
		return config.NewDefault(), nil, "", nil
	}
	if rf.ConfigPath != "" {
		cfg, warns, err := config.Load(rf.ConfigPath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("load config %q: %w", rf.ConfigPath, err)
		}
		return cfg, warns, rf.ConfigPath, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, "", fmt.Errorf("getwd: %w", err)
	}
	dirs := config.DiscoverDirs(cwd)
	cfg, warns, path, err := config.LoadDirs(dirs)
	if err != nil {
		return nil, nil, "", fmt.Errorf("discover config: %w", err)
	}
	return cfg, warns, path, nil
}

// emitDiagnostics prints diags through one or more printers based on
// cfg.Output.Formats. When no format paths are configured, defaults
// to the text printer on stdout (upstream's default).
func emitDiagnostics(stdout io.Writer, cfg *config.Config, diags []output.Diagnostic, rf *runFlags) error {
	type sink struct {
		format    output.Format
		path      string
		authority outputPathAuthority
	}
	var sinks []sink
	f := cfg.Output.Formats
	if f.Text.Path != "" {
		sinks = append(sinks, sink{output.FormatText, f.Text.Path, outputPathAuthorityForFlag(rf, "output.text.path")})
	}
	if f.JSON.Path != "" {
		sinks = append(sinks, sink{output.FormatJSON, f.JSON.Path, outputPathAuthorityForFlag(rf, "output.json.path")})
	}
	if f.Tab.Path != "" {
		sinks = append(sinks, sink{output.FormatTab, f.Tab.Path, outputPathAuthorityForFlag(rf, "output.tab.path")})
	}
	if f.HTML.Path != "" {
		sinks = append(sinks, sink{output.FormatHTML, f.HTML.Path, outputPathAuthorityForFlag(rf, "output.html.path")})
	}
	if f.Checkstyle.Path != "" {
		sinks = append(sinks, sink{output.FormatCheckstyle, f.Checkstyle.Path, outputPathAuthorityForFlag(rf, "output.checkstyle.path")})
	}
	if f.CodeClimate.Path != "" {
		sinks = append(sinks, sink{output.FormatCodeClimate, f.CodeClimate.Path, outputPathAuthorityForFlag(rf, "output.code-climate.path")})
	}
	if f.JUnitXML.Path != "" {
		sinks = append(sinks, sink{output.FormatJUnitXML, f.JUnitXML.Path, outputPathAuthorityForFlag(rf, "output.junit-xml.path")})
	}
	if f.TeamCity.Path != "" {
		sinks = append(sinks, sink{output.FormatTeamCity, f.TeamCity.Path, outputPathAuthorityForFlag(rf, "output.teamcity.path")})
	}
	if f.Sarif.Path != "" {
		sinks = append(sinks, sink{output.FormatSarif, f.Sarif.Path, outputPathAuthorityForFlag(rf, "output.sarif.path")})
	}
	if len(sinks) == 0 {
		sinks = append(sinks, sink{format: output.FormatText, path: "stdout", authority: outputPathOperator})
	}

	output.Sort(diags)

	for _, s := range sinks {
		w, closer, err := openSink(stdout, s.path, s.authority)
		if err != nil {
			return fmt.Errorf("open %s: %w", s.path, err)
		}
		printer, err := output.NewPrinter(s.format, w)
		if err != nil {
			if closer != nil {
				_ = closer.Close()
			}
			return fmt.Errorf("printer %s: %w", s.format, err)
		}
		if err := printer.Print(diags); err != nil {
			if closer != nil {
				_ = closer.Close()
			}
			return fmt.Errorf("print %s: %w", s.format, err)
		}
		if closer != nil {
			if err := closer.Close(); err != nil {
				return fmt.Errorf("close %s: %w", s.path, err)
			}
		}
	}
	return nil
}

type outputPathAuthority int

const (
	outputPathConfined outputPathAuthority = iota
	outputPathOperator
)

func outputPathAuthorityForFlag(rf *runFlags, flag string) outputPathAuthority {
	if rf != nil && rf.wasSet(flag) {
		return outputPathOperator
	}
	return outputPathConfined
}

// openSink resolves a `path` value from `output.formats.<x>.path` to a
// writer under the caller-supplied authority. The special tokens "stdout"
// and "stderr" route to the process streams.
//
// Config-loaded file paths are repository data: an auto-discovered
// .golangci.yml can supply an output destination, and on a CI runner that
// config is attacker-controlled. Those paths are confined to the working
// tree before being opened. CLI-supplied paths are operator intent and use
// normal filesystem authority, preserving support for paths outside the
// working tree.
func openSink(stdout io.Writer, path string, authority outputPathAuthority) (io.Writer, io.Closer, error) {
	switch path {
	case "", "stdout":
		return stdout, nil, nil
	case "stderr":
		return os.Stderr, nil, nil
	default:
		if authority == outputPathOperator {
			f, err := openOperatorOutputFile(path)
			if err != nil {
				return nil, nil, err
			}
			return f, f, nil
		}
		resolved, err := resolveConfinedOutputPath(path)
		if err != nil {
			return nil, nil, err
		}
		f, err := openOutputFile(resolved)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
}

func openOperatorOutputFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.Create(path)
}

// resolveConfinedOutputPath validates and resolves a config-supplied
// output file path, returning an absolute path safe to open.
//
// Output destinations can come from an auto-discovered .golangci.yml,
// which on a CI runner is attacker-controlled (a pull request ships its
// own config). Such a path must never be allowed to escape the working
// tree, or it could truncate/overwrite any file writable by the CI user
// (e.g. ~/.ssh/authorized_keys). The path is resolved against the working
// directory and rejected when it climbs out via an absolute path or "..".
// After the parent directory is created it is re-checked with symlinks
// resolved, so a symlinked directory shipped in the checkout cannot
// redirect the write outside the tree. The final open (see openOutputFile)
// additionally refuses to follow a symlink at the leaf component.
func resolveConfinedOutputPath(path string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Canonicalize the root so the prefix comparisons below are sound
	// even when the working directory is itself reached via a symlink.
	root, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return "", err
	}

	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)
	if !withinRoot(root, abs) {
		return "", fmt.Errorf("output path %q escapes the working directory", path)
	}

	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if !withinRoot(root, realDir) {
		return "", fmt.Errorf("output path %q escapes the working directory via a symlinked directory", path)
	}
	return abs, nil
}

// withinRoot reports whether p is root itself or lives beneath it. Both
// arguments must be cleaned absolute paths.
func withinRoot(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(os.PathSeparator))
}

// contextWithRunTimeout wraps parent with a deadline derived from the
// effective `run.timeout` value (either the --timeout flag or the
// config file). When zero (the default — "disabled by default" per
// --help), returns parent unchanged plus a no-op cancel function so
// callers can always `defer cancel()`.
func contextWithRunTimeout(parent context.Context, t config.Duration) (context.Context, context.CancelFunc) {
	if d := time.Duration(t); d > 0 {
		return context.WithTimeout(parent, d)
	}
	return parent, func() {}
}

// runEngine drives engine.Run end-to-end: resolves the module root,
// opens the persistent L1/L2 caches under $XDG_CACHE_HOME, opens
// the subproc cache, and translates the engine result into the
// (diagnostics, warnings, error) shape the caller expects.
//
// args carries the positional package patterns from the CLI. When
// non-empty, the engine narrows its initial workspace load to those
// patterns rather than loading the full module dep graph; when
// empty, the engine loads everything under the module root.
func runEngine(a *app, cfg *config.Config, reg *registry.Registry, args []string, filter *exclusion.Filter) ([]output.Diagnostic, []string, *engine.RunOutput, error) {
	moduleRoot, err := resolveModuleRoot()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve module root: %w", err)
	}

	l1, err := openL1Cache()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open L1 cache: %w", err)
	}
	// Close terminates per-cache backend resources (e.g. the
	// gocacheprog helper subprocess) so plaid-lint can exit
	// cleanly. The local backend's Close is a no-op. With
	// PLAID_CACHE_BACKEND=gocacheprog each cache owns its own
	// helper child; without these defers the children sit in
	// futex_wait_queue after the linter work completes and the
	// parent never returns from main.
	defer func() { _ = l1.Close() }()
	l2, err := openL2Cache()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open L2 cache: %w", err)
	}
	defer func() { _ = l2.Close() }()

	// L0 is the per-(package, analyzer-set) diagnostic cache that
	// short-circuits the action graph on a warm hit. Failure to open
	// degrades to "no L0" rather than aborting; the engine handles a
	// nil L0 by routing every package through Snapshot.Analyze.
	// PLAID_DISABLE_L0_CACHE=1 forces the L1-only path; used by
	// the W6 three-way digest equivalence assertion in tests.
	var l0Cache *l0.Cache
	if os.Getenv("PLAID_DISABLE_L0_CACHE") != "1" {
		if root, lerr := clcache.DefaultRoot(); lerr == nil {
			if c, lerr := l0.Open(root); lerr == nil {
				l0Cache = c
				defer func() { _ = l0Cache.Close() }()
			}
		}
	}

	// Subproc cache is best-effort; surface the error as a warning
	// rather than failing the run (per cache.go's design — missed
	// writes degrade to "re-run next time," not "abort lint").
	var warnings []string
	subCache, scErr := subproc.OpenCache("")
	if scErr != nil {
		warnings = append(warnings, fmt.Sprintf("subproc cache disabled: %v", scErr))
		subCache = nil
	}

	in := engine.RunInput{
		Config:   cfg,
		Registry: reg,
		Workspace: subproc.WorkspaceRef{
			ModuleRoot: moduleRoot,
			BuildTags:  cfg.Run.BuildTags,
		},
		L1:             l1,
		L2:             l2,
		L0:             l0Cache,
		SubprocCache:   subCache,
		TargetPatterns: args,
		Filter:         filter,
	}

	ctx, cancel := contextWithRunTimeout(context.Background(), cfg.Run.Timeout)
	defer cancel()
	res, err := engine.Run(ctx, in)
	if err != nil {
		return nil, warnings, nil, err
	}
	warnings = append(warnings, res.Warnings...)
	// Reverse the engine's canonical Pos.Filename encoding so the
	// printers (and the user-facing exit-code logic) see absolute
	// paths. Diagnostics whose owning pkgPath isn't in PkgDirs
	// (cgo / vendored / external-test) keep the canonical form.
	output.ResolveDiagnostics(canonicalpath.NewResolver(res.PkgDirs), res.Diagnostics)
	return res.Diagnostics, warnings, res, nil
}

// resolveModuleRoot returns the absolute path engine.Run drives.
// Today this is just the process cwd; future flags (e.g. an
// explicit --working-dir, walking up to find a go.mod) plug in
// here.
func resolveModuleRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// defaultL1Path returns the persistent L1 cache directory.
// $XDG_CACHE_HOME/plaid-lint/l1 (or $HOME/.cache/... when XDG
// is unset). The bench harness deliberately uses MkdirTemp per
// invocation to isolate consecutive runs (LEARN-FGL-004); the
// production CLI wants the opposite — a persistent cache that
// survives across invocations.
func defaultL1Path() (string, error) {
	return cacheSubdir("l1")
}

// defaultL2Path returns the persistent L2 cache directory.
// Sibling of L1 so a single cache-clean call wipes both.
func defaultL2Path() (string, error) {
	return cacheSubdir("l2")
}

// cacheSubdir returns $XDG_CACHE_HOME/plaid-lint/<sub> (or the
// $HOME fallback) and ensures the directory exists.
func cacheSubdir(sub string) (string, error) {
	root, err := clcache.DefaultRoot()
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, sub)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", p, err)
	}
	return p, nil
}

// openL1Cache opens (creating if necessary) the persistent L1
// cache. Failures are returned to the caller; the engine cannot
// proceed without L1. Backend selection honours
// PLAID_L1_CACHE_BACKEND first, then PLAID_CACHE_BACKEND.
func openL1Cache() (*clcache.Cache, error) {
	p, err := defaultL1Path()
	if err != nil {
		return nil, err
	}
	return clcache.OpenForTier(p, clcache.TierL1)
}

// openL2Cache opens (creating if necessary) the persistent L2
// cache. Backend selection honours PLAID_L2_CACHE_BACKEND first,
// then PLAID_CACHE_BACKEND.
func openL2Cache() (*clcache.Cache, error) {
	p, err := defaultL2Path()
	if err != nil {
		return nil, err
	}
	return clcache.OpenForTier(p, clcache.TierL2)
}

// printRunHelp writes the `run` help text to w. Mirrors upstream's
// `golangci-lint run --help`.
func printRunHelp(w io.Writer) {
	fmt.Fprintln(w, strings.TrimSpace(`
Lint the code.

Usage:
  plaid-lint run [flags] [package patterns]

Flags:
  -c, --config PATH                       Read config from file path PATH
      --no-config                         Don't read config file
      --default string                    Default set of linters to enable (default "standard")
  -D, --disable strings                   Disable specific linter (repeatable)
  -E, --enable strings                    Enable specific linter (repeatable)
      --enable-only strings               Override config to only run the specific linter(s)
      --fast-only                         Filter enabled linters to only fast linters
  -j, --concurrency int                   Number of CPUs to use (0 = auto)
      --modules-download-mode string      Modules download mode (mod|readonly|vendor)
      --issues-exit-code int              Exit code when issues were found (default 1)
      --build-tags strings                Build tags (repeatable)
      --timeout duration                  Timeout for total work (disabled by default)
      --tests                             Analyze tests (*_test.go) (default true)
      --allow-parallel-runners            Allow multiple parallel plaid-lint instances running
      --allow-serial-runners              Allow multiple serial plaid-lint instances running
      --path-prefix string                Path prefix to add to output
      --path-mode string                  Path mode ("" or "abs")
      --show-stats                        Show statistics per linter (default true)
      --output.text.path string           text printer output path (stdout/stderr/<file>)
      --output.text.print-linter-name     text printer: print linter name (default true)
      --output.text.print-issued-lines    text printer: print issued lines (default true)
      --output.text.colors                text printer: use colors (default true)
      --output.json.path string           json printer output path
      --output.tab.path string            tab printer output path
      --output.tab.print-linter-name      tab printer: print linter name (default true)
      --output.tab.colors                 tab printer: use colors (default true)
      --output.html.path string           html printer output path
      --output.checkstyle.path string     checkstyle printer output path
      --output.code-climate.path string   code-climate printer output path
      --output.junit-xml.path string      junit-xml printer output path
      --output.junit-xml.extended         junit-xml printer: extended fields
      --output.teamcity.path string       teamcity printer output path
      --output.sarif.path string          sarif printer output path
      --max-issues-per-linter int         Max issues per linter (0 disables) (default 50)
      --max-same-issues int               Max issues with the same text (0 disables) (default 3)
      --uniq-by-line                      Make issues output unique by line (default true)
  -n, --new                               Show only new issues
      --new-from-rev REV                  Show only new issues after git revision REV
      --new-from-patch PATH               Show only new issues in git patch with path PATH
      --new-from-merge-base string        Show only new issues after best common ancestor against HEAD
      --whole-files                       Show issues in any part of updated files
      --fix                               Apply fixes detected by linters
      --cpu-profile-path string           Path to CPU profile output file (pprof format)
      --mem-profile-path string           Path to memory profile output file (pprof heap, captured at run exit after a forced GC)
      --trace-path string                 Path to runtime/trace output file (load with go tool trace)
      --metrics-json string               Path to write a JSON snapshot of cache/run metrics on run completion

Global Flags:
      --color string   Use color when printing; one of 'always', 'auto', 'never' (default "auto")
  -v, --verbose        Verbose output
      --quiet          Suppress upstream debug-trace output on stderr (default true; pass --quiet=false to opt back in)
  -h, --help           Help for a command`))
}
