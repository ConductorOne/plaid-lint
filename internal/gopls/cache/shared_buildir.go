// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cache

// M1 — workspace-scoped *ir.Program cache for honnef's buildir.
//
// Default behavior is unchanged: when PLAID_SHARED_BUILDIR is unset,
// empty, or "0", plaid dispatches buildir.Run as today. When set to
// "1", a typeCheckBatch-scoped workspaceBuildir intercepts the buildir
// dispatch seam (analysis.go around line 1876) and:
//
//   - allocates exactly one *ir.Program per batch, pinned to the batch's
//     own *token.FileSet so positions stay consistent;
//   - future-caches *ir.Package per *types.Package, so the 164+ importers
//     of a cascade-edited upstream share one IR construction instead of
//     each rebuilding it;
//   - serializes Program.CreatePackage via prog.creationMu (honnef writes
//     prog.imported / prog.packages with no locking in
//     internal/.../ir/create.go:261-263);
//   - returns a *buildir.IR built reflectively (see
//     shared_buildir_ir_reflect.go) so the analysis framework's
//     ResultType check succeeds.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"log"
	"os"
	"strconv"
	"sync"

	"honnef.co/go/tools/go/ir"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/ctrlflow"
)

// sharedBuildirEnv is the env-var name that gates M1. Default OFF.
const sharedBuildirEnv = "PLAID_SHARED_BUILDIR"

// sharedBuildirEnabledFlag is the parsed env-var value. Toggle via
// SetSharedBuildirEnabledForTest in tests; the env var is read once at
// init because Go's os.Getenv during package init is cheaper than a
// per-action sync.Once and a hot path doesn't need per-call lookups.
var sharedBuildirEnabledFlag = parseSharedBuildirEnabled()

func parseSharedBuildirEnabled() bool {
	v, ok := os.LookupEnv(sharedBuildirEnv)
	if !ok || v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("plaid-lint: %s=%q is not a number; treating as unset", sharedBuildirEnv, v)
		return false
	}
	return n != 0
}

// SetSharedBuildirEnabledForTest flips the M1 gate for the duration of a
// test. Preferred over t.Setenv because the env var is parsed once at
// init. The returned closure restores the previous value.
func SetSharedBuildirEnabledForTest(enabled bool) func() {
	prev := sharedBuildirEnabledFlag
	sharedBuildirEnabledFlag = enabled
	ResetSharedBuildirStats()
	return func() {
		sharedBuildirEnabledFlag = prev
		ResetSharedBuildirStats()
	}
}

// SharedBuildirEnabled reports whether M1's workspace-scoped *ir.Program
// cache is enabled. Tests pin this when asserting behavior.
func SharedBuildirEnabled() bool { return sharedBuildirEnabledFlag }

// SharedBuildirStats is the M1 instrumentation snapshot for tests.
type SharedBuildirStats struct {
	// Programs is the number of *ir.Program instances allocated since the
	// last reset. M1 contract: at most one per typeCheckBatch.
	Programs int64
	// PackageBuilds is the count of unique (program, package) pairs that
	// went through CreatePackage + Build. One per workspace package per
	// batch under the cache.
	PackageBuilds int64
	// CacheHits is the count of runShared calls that returned a cached
	// *ir.Package without recomputation. Each importer beyond the first
	// for a given upstream contributes one hit.
	CacheHits int64
	// Dispatches is the count of runShared calls (= the count of buildir
	// Run dispatches routed through M1).
	Dispatches int64
}

var (
	sharedBuildirStatsMu sync.Mutex
	sharedBuildirStats   SharedBuildirStats
)

func incSharedStats(field *int64) {
	sharedBuildirStatsMu.Lock()
	*field++
	sharedBuildirStatsMu.Unlock()
}

// SharedBuildirSnapshot returns a copy of the M1 stats.
func SharedBuildirSnapshot() SharedBuildirStats {
	sharedBuildirStatsMu.Lock()
	defer sharedBuildirStatsMu.Unlock()
	return SharedBuildirStats{
		Programs:      sharedBuildirStats.Programs,
		PackageBuilds: sharedBuildirStats.PackageBuilds,
		CacheHits:     sharedBuildirStats.CacheHits,
		Dispatches:    sharedBuildirStats.Dispatches,
	}
}

// ResetSharedBuildirStats clears the M1 counters. Tests call this between
// engine.Run invocations to isolate measurements.
func ResetSharedBuildirStats() {
	sharedBuildirStatsMu.Lock()
	sharedBuildirStats = SharedBuildirStats{}
	sharedBuildirStatsMu.Unlock()
}

// workspaceBuildir owns one *ir.Program plus the per-batch future-cache
// of *ir.Package built against it. Lifetime is the typeCheckBatch — the
// program is GC-eligible once the batch tears down.
//
// Concurrency model:
//
//   - prog is allocated once under initOnce; after init, the Program
//     pointer is read-only.
//   - creationMu serializes Program.CreatePackage calls so the unguarded
//     map writes in honnef ir/create.go:261-263 do not race.
//   - futures is the per-*types.Package future-cache that ensures each
//     (program, package) pair is built exactly once even under N
//     concurrent runShared callers for that package. Outer mu guards the
//     map header; each future has its own ready channel for value
//     publication.
type workspaceBuildir struct {
	initOnce sync.Once
	initErr  error

	// prog is the shared *ir.Program for this batch. Pinned to the
	// batch's fset on first runShared.
	prog *ir.Program

	// progMu coordinates Program-level access. CreatePackage takes
	// progMu.Lock (writer): it mutates prog.imported and prog.packages
	// without locking (honnef ir/create.go:261-263). Package.Build
	// (and the methodSet builder it triggers) reads prog.packages
	// via Program.packageLevelValue and Program.Package without
	// locking, so concurrent Builds against a Program that another
	// goroutine is mid-CreatePackage on race the map write. progMu
	// taken as RLock around Build serializes CreatePackage against
	// any concurrent Build call.
	//
	// This was framed as a CreatePackage-vs-CreatePackage
	// race; race-detector evidence shows the actual hazard is
	// CreatePackage-write vs Build-read on prog.packages. RWMutex
	// covers both.
	progMu sync.RWMutex

	// mu guards the futures map.
	mu      sync.Mutex
	futures map[*types.Package]*buildirFuture

	// ctrlflows maps source packages to the control-flow analysis created for
	// that package. Program.SetNoReturn consults it while building the shared
	// IR, preserving buildir's per-package no-return semantics without the
	// removed buildir fact API.
	ctrlflows sync.Map // map[*types.Package]*ctrlflow.CFGs
}

// buildirFuture is the per-package result. Once `ready` is closed,
// `irpkg`, `srcFuncs`, and `err` are stable.
type buildirFuture struct {
	ready    chan struct{}
	irpkg    *ir.Package
	srcFuncs []*ir.Function
	err      error
}

// getOrCreateWorkspaceBuildir returns the batch's workspaceBuildir,
// allocating it lazily on first use. Called from the analysis dispatch
// seam under the M1 flag.
func getOrCreateWorkspaceBuildir(b *typeCheckBatch) *workspaceBuildir {
	b.sharedBuildirOnce.Do(func() {
		b.sharedBuildir = &workspaceBuildir{
			futures: make(map[*types.Package]*buildirFuture),
		}
	})
	return b.sharedBuildir
}

// initProgram pins the shared Program to the batch's fset on first call. The
// buildir analyzer's mode (ir.GlobalDebug) matches honnef's per-Run
// constructor. No-return state moved from buildir facts to ctrlflow in
// honnef.co/go/tools v0.8, so resolve it through the source package's
// completed ctrlflow result.
func (w *workspaceBuildir) initProgram(fset *token.FileSet) error {
	w.initOnce.Do(func() {
		w.prog = ir.NewProgram(fset, ir.GlobalDebug)
		w.prog.SetNoReturn(func(fn *types.Func) bool {
			cfg, ok := w.ctrlflows.Load(fn.Pkg())
			return ok && cfg.(*ctrlflow.CFGs).NoReturn(fn)
		})
		incSharedStats(&sharedBuildirStats.Programs)
	})
	return w.initErr
}

// future returns the buildirFuture for pkg, allocating it lazily under
// the outer mu. The returned future may be unfinished (ready not yet
// closed); callers must wait on f.ready before reading the result.
//
// Returns (future, isFirstCaller). The first caller is responsible for
// computing the result and closing ready; later callers wait on ready.
func (w *workspaceBuildir) future(pkg *types.Package) (*buildirFuture, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if f, ok := w.futures[pkg]; ok {
		return f, false
	}
	f := &buildirFuture{ready: make(chan struct{})}
	w.futures[pkg] = f
	return f, true
}

// runShared is the M1 entry point. It returns a *buildir.IR (typed as
// any so the analysis framework's reflect-type check sees the exact
// type honnef registered).
//
// On cache hit, this is a fast path that bypasses Program.CreatePackage
// and Package.Build entirely; the cached *ir.Package and SrcFuncs come
// from a sibling pass that already built them under the shared Program.
//
// On cache miss for pass.Pkg, this:
//
//  1. registers the completed ctrlflow result for pass.Pkg;
//  2. recursively ensures every transitive import has been CreatePackage'd
//     against w.prog (honnef's Build precondition);
//  3. CreatePackage's pass.Pkg with its files+TypesInfo (under creationMu);
//  4. calls Package.Build (idempotent via sync.Once);
//  5. computes the AnonFuncs-extended SrcFuncs list;
//  6. caches the result and returns.
func (w *workspaceBuildir) runShared(pass *analysis.Pass) (any, error) {
	incSharedStats(&sharedBuildirStats.Dispatches)

	if err := w.initProgram(pass.Fset); err != nil {
		return nil, err
	}
	cfg, ok := pass.ResultOf[ctrlflow.Analyzer].(*ctrlflow.CFGs)
	if !ok {
		return nil, fmt.Errorf("plaid-lint M1: buildir ctrlflow result has type %T", pass.ResultOf[ctrlflow.Analyzer])
	}
	w.ctrlflows.Store(pass.Pkg, cfg)

	// Build (or wait for) pass.Pkg's *ir.Package. importable=false
	// matches honnef's buildir.go:85 for the primary package; we pass
	// the full pass info so the build phase has files+TypesInfo.
	irpkg, srcFuncs, err := w.ensurePackage(pass, pass.Pkg, false /* importable */, pass.Files, pass.TypesInfo)
	if err != nil {
		return nil, err
	}

	// The v0.8 buildir analyzer delegates no-return analysis to ctrlflow.
	// Each package's completed ctrlflow result is available through the shared
	// Program callback installed by initProgram.

	return newBuildirIR(irpkg, srcFuncs), nil
}

// ensurePackage looks up pkg in the future-cache. If absent, it builds
// the IR package against w.prog under a sync-once-style guard.
//
// importable governs honnef's CreatePackage "importable" parameter:
// transitive imports use true (so they appear in prog.imported by
// path); the primary package uses false.
//
// files / info may be nil — when nil, CreatePackage falls back to the
// gc-compiled binary-package path that walks Scope, mirroring honnef
// buildir.go:69 for imports.
func (w *workspaceBuildir) ensurePackage(pass *analysis.Pass, pkg *types.Package, importable bool, files []*ast.File, info *types.Info) (*ir.Package, []*ir.Function, error) {
	f, first := w.future(pkg)
	if !first {
		<-f.ready
		// Cache hit accounting: only count when the future was
		// already resolved by another caller. This is the metric the
		// prediction targets — 163 of 164 importers should hit
		// the cache for a cascade-shared upstream.
		incSharedStats(&sharedBuildirStats.CacheHits)
		return f.irpkg, f.srcFuncs, f.err
	}

	// First caller: build the IR package and publish the result via
	// f.ready close. Capture panics so a single failed package doesn't
	// permanently wedge sibling waiters with a hung future.
	defer close(f.ready)

	irpkg, srcFuncs, err := w.buildPackage(pass, pkg, importable, files, info)
	f.irpkg = irpkg
	f.srcFuncs = srcFuncs
	f.err = err
	return irpkg, srcFuncs, err
}

// buildPackage runs the actual CreatePackage + Build + SrcFuncs work. Called
// exactly once per (workspaceBuildir, pkg) from the future-cache first caller.
func (w *workspaceBuildir) buildPackage(pass *analysis.Pass, pkg *types.Package, importable bool, files []*ast.File, info *types.Info) (*ir.Package, []*ir.Function, error) {
	// Recurse on direct imports first so honnef's Build precondition
	// holds (each direct import must be in prog.packages before Build
	// fires). We don't pass files/info for imports — they take the
	// binary-package path inside CreatePackage.
	for _, imp := range pkg.Imports() {
		if _, _, err := w.ensurePackage(pass, imp, true /* importable */, nil, nil); err != nil {
			return nil, nil, fmt.Errorf("ensure import %q for %q: %w", imp.Path(), pkg.Path(), err)
		}
	}

	// CreatePackage under the writer lock. honnef's CreatePackage
	// writes prog.imported / prog.packages without locking
	// (ir/create.go:261-263). The writer lock excludes both
	// concurrent CreatePackage callers and concurrent Build readers
	// (Build reads prog.packages via packageLevelValue).
	w.progMu.Lock()
	// Re-check the future cache under the lock to handle the case
	// where another caller raced us into the import-recursion above
	// and beat us to ensurePackage for pkg. We only got here because
	// future() said we were first for pkg, but the same package can
	// reach ensurePackage as both a primary (from runShared) and as
	// an import (from a sibling primary's recursion). The future()
	// guard already coordinates this — but defensively, if
	// prog.packages already has pkg, skip CreatePackage.
	var irpkg *ir.Package
	if existing, ok := w.lookupExistingPackage(pkg); ok {
		irpkg = existing
	} else {
		irpkg = w.prog.CreatePackage(pkg, files, info, importable)
		incSharedStats(&sharedBuildirStats.PackageBuilds)
	}
	w.progMu.Unlock()

	// Build under the reader lock. Build reads prog.packages (via
	// packageLevelValue inside the methodSet path); RLock excludes
	// concurrent CreatePackage writers while still allowing
	// sibling Build calls to proceed in parallel. Package.Build is
	// idempotent (sync.Once internally) and thread-safe by upstream
	// contract; the RLock only protects against the unguarded map
	// read inside packageLevelValue.
	w.progMu.RLock()
	irpkg.Build()
	w.progMu.RUnlock()

	// Compute the SrcFuncs list including anonymous functions
	// (mirrors buildir.go:74-88).
	funcs := make([]*ir.Function, len(irpkg.Functions))
	copy(funcs, irpkg.Functions)
	var addAnons func(f *ir.Function)
	addAnons = func(f *ir.Function) {
		for _, anon := range f.AnonFuncs {
			funcs = append(funcs, anon)
			addAnons(anon)
		}
	}
	for _, fn := range irpkg.Functions {
		addAnons(fn)
	}

	return irpkg, funcs, nil
}

// lookupExistingPackage checks whether prog already has an entry for
// pkg. Called under creationMu so the map read is safe even though
// honnef does not provide a public accessor.
//
// honnef's Program.packages is unexported, so we use the public
// (*Program).Package accessor when available, or fall back to
// ImportedPackage by path.
func (w *workspaceBuildir) lookupExistingPackage(pkg *types.Package) (*ir.Package, bool) {
	// (*Program).Package(pkg) is the canonical accessor.
	if p := w.prog.Package(pkg); p != nil {
		return p, true
	}
	return nil, false
}
