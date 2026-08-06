// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/quiet"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/unit"
	"github.com/conductorone/plaid-lint/internal/unitcache"
)

// runUnit executes the `plaid-lint unit` subcommand: one hermetic
// single-package analysis action driven entirely by a unit.json
// config (see internal/unit).
//
// Exit codes differ from `run` deliberately: findings are results
// (recorded in the SARIF output), never an exit code. 0 = analysis
// completed; exitCLIError (2) = bad flags; exitInternalError (3) =
// unusable inputs or an internal fault; exitConfigError (7) = invalid
// .golangci config.
func (a *app) runUnit(args []string) int {
	fs := newRunFlagSet("unit", a.stderr)
	g := bindGlobalFlags(fs)
	cfgPath := fs.String("cfg", "", "path to the unit.json action config (required)")
	workerMode := fs.Bool("worker", false, "run as a Bazel persistent worker (JSON protocol on stdin/stdout)")
	persistentWorker := fs.Bool("persistent_worker", false, "alias for --worker; Bazel appends this flag when it launches a persistent worker process")
	cacheDir := fs.String("cache-dir", "", "reuse results across actions through a content-addressed cache in this directory (off when empty)")

	args, aerr := expandArgsFiles(args)
	if aerr != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: unit: %v\n", aerr)
		return exitCLIError
	}
	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		printUnitHelp(a.stdout)
		return exitSuccess
	}

	// Install the stderr filter before any analysis runs — the same
	// default-quiet behavior `run` has. The `unused` wrapper calls
	// upstream `honnef.co/go/tools`'s `SerializedGraph.Merge`, whose
	// `unused/serialize.go:trace()` writes "new node, remapping X -> Y"
	// and "deduplicating ..." to os.Stderr unconditionally — millions of
	// lines per uncached Bazel action. Installing here (before the
	// worker dispatch) covers both the one-shot and --worker persistent
	// modes; `--quiet=false` (or leaving LOG_LEVEL unset after passing
	// --quiet=false) is the escape hatch, exactly as in `run`.
	if g.Quiet || quiet.FromEnv() {
		restore := quiet.Install()
		defer restore()
	}
	sess := newUnitSession(*cacheDir, a.stderr)
	if *workerMode || *persistentWorker {
		return a.runUnitWorker(sess)
	}
	if *cfgPath == "" {
		fmt.Fprintln(a.stderr, "plaid-lint: unit: --cfg is required")
		return exitCLIError
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintf(a.stderr, "plaid-lint: unit: unexpected arguments %q\n", fs.Args())
		return exitCLIError
	}

	code, msgs := unitOnce(context.Background(), *cfgPath, sess)
	for _, m := range msgs {
		fmt.Fprintf(a.stderr, "plaid-lint: %s\n", m)
	}
	return code
}

// expandArgsFiles substitutes any `@file` argument with the file's
// newline-separated contents — the params-file convention Bazel uses
// when an action's argv exceeds the command-line limit and,
// mandatorily, when a target advertises worker support (the trailing
// @flagfile carries the per-request args for non-worker fallback
// execution).
func expandArgsFiles(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if !strings.HasPrefix(arg, "@") || len(arg) == 1 {
			out = append(out, arg)
			continue
		}
		body, err := os.ReadFile(arg[1:])
		if err != nil {
			return nil, fmt.Errorf("params file %s: %w", arg, err)
		}
		for line := range strings.Lines(string(body)) {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line != "" {
				out = append(out, line)
			}
		}
	}
	return out, nil
}

// unitSession memoizes the per-config state (parsed .golangci config,
// built registry, exclusion filter) across unit invocations in one
// process. The persistent worker reuses one session so identical
// configs — the common case, since every action in a Bazel invocation
// shares the workspace's .golangci.yml — skip the config parse and
// the full registry rebuild.
//
// Reuse is keyed on (config path, config content digest, enable_only
// set): a changed config rebuilds everything. NOT safe for concurrent
// use — registry.BuildFromConfig applies per-linter settings by
// mutating package-global analyzer FlagSets, so sessions must only be
// used from a serial loop (the worker is serial by design).
type unitSession struct {
	configPath string
	digest     [32]byte
	enableOnly string

	golangci *config.Config
	reg      *registry.Registry
	filter   *exclusion.Filter

	// cache is the optional content-addressed result cache
	// (`--cache-dir`), nil unless the operator asked for one. toolID
	// identifies this binary in every key it computes; an empty
	// toolID disables the cache, because an entry that cannot be
	// attributed to a specific tool must never be reused.
	cache  *unitcache.Store
	toolID string
}

// newUnitSession builds the per-process unit state, opening the
// optional result cache when cacheDir names one.
//
// Cache setup is best-effort by design: a cache that cannot be opened
// (unwritable directory, unreadable executable) degrades to an
// uncached run — the analysis is unaffected, so failing the action
// would trade a correct build for a housekeeping problem.
func newUnitSession(cacheDir string, stderr io.Writer) *unitSession {
	sess := &unitSession{}
	if cacheDir == "" {
		return sess
	}
	store, err := unitcache.Open(cacheDir)
	if err != nil {
		fmt.Fprintf(stderr, "plaid-lint: warning: unit cache disabled: %v\n", err)
		return sess
	}
	toolID, err := unitToolID()
	if err != nil {
		fmt.Fprintf(stderr, "plaid-lint: warning: unit cache disabled: %v\n", err)
		return sess
	}
	sess.cache, sess.toolID = store, toolID
	return sess
}

// unitToolIDOnce memoizes the tool identity: it is fixed for the
// process, and computing it reads the whole executable.
var unitToolIDOnce = sync.OnceValues(computeUnitToolID)

// unitToolID returns the identity of the analyzing binary, folded into
// every unit cache key.
func unitToolID() (string, error) { return unitToolIDOnce() }

// computeUnitToolID derives that identity from the executable's own
// bytes, plus the version metadata for legibility.
//
// The content digest — rather than the version string alone — is what
// makes the key honest: two binaries built from different trees both
// report "v0-dev" until a release stamps them, and serving one's
// results for the other is exactly the stale-findings failure this
// cache must not have. The executable is the action's own declared
// tool input, so reading it consults nothing ambient.
func computeUnitToolID() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", fmt.Errorf("open executable: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("read executable: %w", err)
	}
	v := resolveVersion()
	return fmt.Sprintf("%s %s %s/%s sha256:%s",
		v.Version, v.Go, v.OS, v.Arch, hex.EncodeToString(h.Sum(nil))), nil
}

// unitOnce runs a single unit action. Returned messages go to stderr
// regardless of outcome; the exit code follows the unit contract.
//
// Findings never influence the exit code: the SARIF output is the
// findings channel, and a separate consumer (a Bazel validation
// action, a CI collector) decides what fails.
//
// When the session carries a cache, the lookup happens before the
// config is even parsed: a hit replays the declared outputs and the
// filling run's messages, skipping the config load, the registry
// build and the analysis entirely. cacheMsgs stays separate from msgs
// throughout, so a cache-specific note (a skipped key, a failed write)
// never becomes part of what a later hit reports.
func unitOnce(ctx context.Context, cfgPath string, sess *unitSession) (int, []string) {
	var msgs, cacheMsgs []string

	ucfg, err := unit.LoadConfig(cfgPath)
	if err != nil {
		return exitInternalError, append(msgs, err.Error())
	}

	attempt, cached, note := unitCacheLookup(ucfg, cfgPath, sess)
	cacheMsgs = append(cacheMsgs, note...)
	if cached != nil {
		return exitSuccess, append(cached.Warnings, cacheMsgs...)
	}

	var digest [32]byte
	if ucfg.Analysis.Config != "" {
		body, rerr := os.ReadFile(ucfg.Analysis.Config)
		if rerr != nil {
			return exitConfigError, append(msgs, fmt.Sprintf("read config: %v", rerr))
		}
		digest = sha256.Sum256(body)
	}
	enableOnly := strings.Join(ucfg.Analysis.EnableOnly, ",")

	if sess.golangci == nil || sess.configPath != ucfg.Analysis.Config ||
		sess.digest != digest || sess.enableOnly != enableOnly {
		golangci, cfgWarnings, err := loadUnitGolangciConfig(ucfg)
		if err != nil {
			return exitConfigError, append(msgs, err.Error())
		}
		for _, w := range cfgWarnings {
			msgs = append(msgs, fmt.Sprintf("warning: %s: %s", w.Field, w.Message))
		}
		if errs := config.Validate(golangci); len(errs) > 0 {
			for _, e := range errs {
				msgs = append(msgs, fmt.Sprintf("config error: %v", e))
			}
			return exitConfigError, msgs
		}

		reg, regWarnings, err := registry.BuildFromConfig(golangci)
		if err != nil {
			return exitInternalError, append(msgs, err.Error())
		}
		for _, w := range regWarnings {
			msgs = append(msgs, fmt.Sprintf("warning: %s: %s", w.Field, w.Message))
		}
		if len(ucfg.Analysis.EnableOnly) > 0 {
			reg, err = reg.SelectAnalyzers(ucfg.Analysis.EnableOnly)
			if err != nil {
				return exitInternalError, append(msgs, err.Error())
			}
		}

		// The exclusion filter anchors path-relative rules at the
		// process working directory — the execroot under Bazel —
		// matching how the declared source paths are spelled.
		filter, err := exclusion.NewFilter(golangci, mustGetwd(), nil)
		if err != nil {
			return exitInternalError, append(msgs, fmt.Sprintf("exclusion filter: %v", err))
		}

		sess.configPath = ucfg.Analysis.Config
		sess.digest = digest
		sess.enableOnly = enableOnly
		sess.golangci = golangci
		sess.reg = reg
		sess.filter = filter
	}

	res, err := unit.Run(ctx, ucfg, sess.golangci, sess.reg, sess.filter)
	if err != nil {
		return exitInternalError, append(msgs, err.Error())
	}
	for _, w := range res.Warnings {
		msgs = append(msgs, "warning: "+w)
	}
	cacheMsgs = append(cacheMsgs, attempt.store(ucfg, msgs)...)
	return exitSuccess, append(msgs, cacheMsgs...)
}

// unitCacheOutputs names the action's declared outputs for the cache.
func unitCacheOutputs(ucfg *unit.Config) unitcache.Outputs {
	return unitcache.Outputs{Sarif: ucfg.Out.Sarif, Facts: ucfg.Out.Facts}
}

// unitCacheAttempt carries the outcome of key computation from the
// lookup to the store side of one action. A zero attempt (no cache
// configured, or a key that could not be computed) stores nothing.
type unitCacheAttempt struct {
	cache *unitcache.Store
	key   unitcache.Key
}

// unitCacheLookup computes the action's cache key and, on a hit,
// materializes the cached outputs. It returns the attempt to store
// under, the entry that satisfied the action (nil to run it), and any
// operator-facing notes.
//
// Every failure here is non-fatal and lands in the notes: an
// unreadable declared input, a corrupt entry, or an entry whose shape
// does not match the action's declared outputs all fall through to a
// normal run. The cache is an accelerator, never a source of truth.
func unitCacheLookup(ucfg *unit.Config, cfgPath string, sess *unitSession) (unitCacheAttempt, *unitcache.Entry, []string) {
	if sess.cache == nil {
		return unitCacheAttempt{}, nil, nil
	}
	key, err := unitcache.ComputeKey(cfgPath, ucfg, sess.toolID)
	if err != nil {
		return unitCacheAttempt{}, nil, []string{fmt.Sprintf("warning: unit cache skipped for this action: %v", err)}
	}
	attempt := unitCacheAttempt{cache: sess.cache, key: key}
	entry, err := sess.cache.Get(key)
	if err != nil {
		return attempt, nil, []string{fmt.Sprintf("warning: unit cache read failed: %v", err)}
	}
	if entry == nil {
		return attempt, nil, nil
	}
	if err := entry.Write(unitCacheOutputs(ucfg)); err != nil {
		return attempt, nil, []string{fmt.Sprintf("warning: unit cache hit discarded: %v", err)}
	}
	return attempt, entry, nil
}

// store records a completed action's declared outputs under the key.
func (a unitCacheAttempt) store(ucfg *unit.Config, msgs []string) []string {
	if a.cache == nil {
		return nil
	}
	entry, err := unitcache.ReadOutputs(unitCacheOutputs(ucfg), msgs)
	if err == nil {
		err = a.cache.Put(a.key, entry)
	}
	if err != nil {
		return []string{fmt.Sprintf("warning: unit cache write failed: %v", err)}
	}
	return nil
}

// loadUnitGolangciConfig loads the .golangci config named by the unit
// config, or plaid-lint defaults when none is named. Unlike `run`,
// there is NO directory discovery: unit actions declare every input,
// so an undeclared config file must not influence the result.
func loadUnitGolangciConfig(ucfg *unit.Config) (*config.Config, []config.Warning, error) {
	if ucfg.Analysis.Config == "" {
		return config.NewDefault(), nil, nil
	}
	return config.Load(ucfg.Analysis.Config)
}

// printUnitHelp writes the `unit` help text.
func printUnitHelp(w io.Writer) {
	fmt.Fprintln(w, `plaid-lint unit — analyze exactly one package from declared inputs.

Usage:
  plaid-lint unit --cfg unit.json
  plaid-lint unit --worker

The unit.json config names the package sources, an importcfg mapping
import paths to compiler export data, dependency fact files, the
.golangci config, and the output paths (SARIF diagnostics + a
.plaidfacts fact file). Nothing is discovered: no go list, no module
resolution, no Go toolchain.

Findings are written to the SARIF output and never affect the exit
code. Exit codes: 0 analysis completed (with or without findings),
2 bad flags, 3 unusable inputs or internal error, 7 invalid
.golangci config.

Flags:
      --cfg string         path to the unit.json action config
      --worker             run as a Bazel persistent worker: read one
                           JSON WorkRequest per line on stdin
                           (arguments: ["--cfg", <path>]), write one
                           JSON WorkResponse per line on stdout
      --cache-dir string   reuse results across actions through a
                           content-addressed cache rooted here. Off
                           unless set. The key is derived only from
                           the declared inputs (unit.json and every
                           file it names) plus this binary's identity
                           — no environment variable participates —
                           so a hit reproduces a cold run's outputs
                           byte for byte. Safe to share between
                           checkouts and, when the declared paths are
                           relative, between machines
      --quiet              suppress upstream debug-trace output on
                           stderr (default true; pass --quiet=false to
                           see honnef's 'new node, remapping' /
                           'deduplicating' lines)`)
}

// mustGetwd returns the working directory; the empty string on
// failure keeps the exclusion filter operating on absolute paths.
func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
