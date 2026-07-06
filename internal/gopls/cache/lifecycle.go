// Copyright 2026 The plaid-lint authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Upstream Go: BSD-3-Clause. See internal/gopls/LICENSE.upstream.

// This file holds the construction-time entry points the
// internal/workspace package needs to spin up a View and its initial
// Snapshot, and to clone an existing Snapshot in response to file
// modifications.
//
// Upstream gopls put this logic in session.go / view.go (deleted at
// fork time (Category C). The CLI-shaped surface lives in
// internal/workspace; this file is the package-internal API the
// workspace package calls.

package cache

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/typerefs"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/gopls/util/immutable"
	"github.com/conductorone/plaid-lint/internal/gopls/util/memoize"
	"github.com/conductorone/plaid-lint/internal/gopls/util/persistent"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/gocommand"
)

// parseCacheExpiry is the TTL the parseCache GC goroutine uses for
// evicting unused parsed files. Mirrors upstream gopls's
// cache.parseCacheExpiry (60s); see internal/gopls/cache/parse_cache.go.
const parseCacheExpiry = 60 * time.Second

var nextViewID atomic.Int64

// NewView constructs an empty View rooted at moduleRoot, plus its
// initial Snapshot. The caller owns one reference to the returned
// Snapshot and must release it via Snapshot.Acquire's released
// function (or the Snapshot will leak the parseCache goroutine).
//
// The Cache is the shared content-addressable store; pass nil to get
// a fresh one. fs may be nil, in which case the View uses an empty
// overlayFS over the cache's memoized disk fs.
func NewView(c *Cache, moduleRoot protocol.DocumentURI, opts *settings.Options) (*View, *Snapshot) {
	if c == nil {
		c = New(nil)
	}
	if opts == nil {
		opts = settings.DefaultOptions()
	}
	// Mark the Cache as having a live View so AttachL2 panics if called
	// after this point.
	c.viewCount.Add(1)

	folder := &Folder{
		Dir:     moduleRoot,
		Name:    moduleRoot.Path(),
		Options: opts,
		Env:     ambientGoEnv(),
	}

	// Detect whether the root contains a go.mod. If so, treat the view
	// as a GoModView so the initial workspace load walks ./..., not
	// just the root directory, and seed workspaceModFiles so the load
	// path's isWorkspacePackageLocked check admits packages from this
	// module. Without this the W6 pipeline test (and any CLI user
	// pointing plaid-lint at a real module) gets only the root
	// package and the analyzer set never sees sub-packages. W3 left
	// view.typ pinned to AdHocView; this is the minimal
	// productionization we need for W6 to drive a multi-package fixture.
	viewType := AdHocView
	workspaceModFiles := make(map[protocol.DocumentURI]struct{})
	goModPath := filepath.Join(moduleRoot.Path(), "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		viewType = GoModView
		workspaceModFiles[protocol.URIFromPath(goModPath)] = struct{}{}
	}

	v := &View{
		id:                   strconv.FormatInt(nextViewID.Add(1), 10),
		folder:               folder,
		gocmdRunner:          &gocommand.Runner{},
		c:                    c,
		pkgIndex:             typerefs.NewPackageIndex(),
		parseCache:           newParseCache(parseCacheExpiry),
		fs:                   newOverlayFS(c.memoizedFS),
		initialWorkspaceLoad: make(chan struct{}),
		initializationSema:   make(chan struct{}, 1),
		typ:                  viewType,
		root:                 moduleRoot,
		workspaceModFiles:    workspaceModFiles,
	}

	bgCtx, cancel := context.WithCancel(context.Background())
	v.cancelInitialWorkspaceLoad = cancel

	snap := newInitialSnapshot(v, c.store, bgCtx)
	return v, snap
}

// Shutdown releases View-level resources. WorkspaceState owns the
// snapshot lifecycle independently and must release its snapshot
// references before calling Shutdown.
//
// Shutdown stops the parseCache GC goroutine; any Snapshot still
// outstanding at this point will see its parseCache become inert,
// which is fine because outstanding snapshots only read through
// parseCache, never start it again.
func (v *View) Shutdown() {
	if v.cancelInitialWorkspaceLoad != nil {
		v.cancelInitialWorkspaceLoad()
	}
	if v.parseCache != nil {
		v.parseCache.stop()
		v.parseCache = nil
	}
}

// newInitialSnapshot constructs a fully zero-valued Snapshot for v.
// The Snapshot is born with one outstanding reference (held by the
// View itself, transferred to the WorkspaceState caller); its
// `done` callback stops the parseCache GC goroutine when the last
// reference is released.
//
// We invoke this once per View; subsequent snapshots come from
// cloneSnapshot, which copies the persistent maps with the right
// invalidations.
func newInitialSnapshot(v *View, store *memoize.Store, bgCtx context.Context) *Snapshot {
	bgCtx, cancel := context.WithCancel(bgCtx)

	// done runs after the last refcount drop on this snapshot.
	// The parseCache is owned by the View and stopped in
	// View.Shutdown, so done is a no-op here; if a future phase
	// needs per-snapshot teardown, hook it here.
	done := func() {}

	return &Snapshot{
		sequenceID:        0,
		view:              v,
		store:             store,
		refcount:          1,
		done:              done,
		backgroundCtx:     bgCtx,
		cancel:            cancel,
		meta:              &metadata.Graph{},
		files:             newFileMap(),
		packages:          new(persistent.Map[PackageID, *packageHandle]),
		fullAnalysisKeys:  new(persistent.Map[PackageID, file.Hash]),
		factyAnalysisKeys: new(persistent.Map[PackageID, file.Hash]),
		workspacePackages: immutable.MapOf[PackageID, PackagePath](nil),
		shouldLoad:        new(persistent.Map[PackageID, []PackagePath]),
		unloadableFiles:   new(persistent.Set[protocol.DocumentURI]),
		parseModHandles:   new(persistent.Map[protocol.DocumentURI, *memoize.Promise]),
		parseWorkHandles:  new(persistent.Map[protocol.DocumentURI, *memoize.Promise]),
		modTidyHandles:    new(persistent.Map[protocol.DocumentURI, *memoize.Promise]),
		modWhyHandles:     new(persistent.Map[protocol.DocumentURI, *memoize.Promise]),
		modVulnHandles:    new(persistent.Map[protocol.DocumentURI, *memoize.Promise]),
		moduleUpgrades:    new(persistent.Map[protocol.DocumentURI, map[string]string]),
	}
}

// CloneSnapshot produces a successor to base reflecting the given
// file modifications. The caller owns one reference to the returned
// Snapshot; base is unaffected (its outstanding refs may still use
// it). The returned bool is the upstream `needsDiagnosis` flag —
// callers can ignore it if they only care about the new snapshot.
//
// done is invoked when the returned Snapshot's refcount hits zero;
// callers typically pass a closure that releases any view-level
// resources tied to this specific clone.
func CloneSnapshot(ctx context.Context, base *Snapshot, changed StateChange, done func()) (*Snapshot, bool) {
	if done == nil {
		done = func() {}
	}
	// The view's parseCache must outlive every snapshot derived
	// from it. We chain `done` so the parseCache is only stopped
	// when the *initial* snapshot's refcount hits zero, which is
	// guarded by the original done in newInitialSnapshot. Clones
	// just call the caller's done.
	return base.clone(ctx, base.backgroundCtx, changed, done)
}

// ApplyOverlay updates v.fs to reflect a SetContent call from the
// workspace API. The returned file.Handle is the overlay that
// CloneSnapshot will pick up when called with the corresponding
// StateChange.Files entry.
//
// content == nil clears the overlay (the next ReadFile falls
// through to disk).
func ApplyOverlay(v *View, uri protocol.DocumentURI, content []byte, version int32, kind file.Kind) file.Handle {
	v.fs.mu.Lock()
	defer v.fs.mu.Unlock()
	if content == nil {
		delete(v.fs.overlays, uri)
		return nil
	}
	o := &overlay{
		uri:     uri,
		content: content,
		hash:    file.HashOf(content),
		version: version,
		kind:    kind,
		saved:   false,
	}
	v.fs.overlays[uri] = o
	return o
}

// ReadFile reads a file through the view's overlay-aware FS. This is
// the right entry point for workspace-level code that needs the
// current effective view of a file (overlay-first, disk-fallback).
func (v *View) ReadFile(ctx context.Context, uri protocol.DocumentURI) (file.Handle, error) {
	return v.fs.ReadFile(ctx, uri)
}

// snapshotIDComponents returns the pair (viewID, sequenceID) that
// the workspace package composes into its global snapshot ID.
func snapshotIDComponents(s *Snapshot) (string, uint64) {
	return s.view.id, s.SequenceID()
}

// SnapshotIDComponents is the exported accessor for the (viewID,
// sequenceID) pair. Returning two values keeps the wire format
// concern in the workspace package.
func SnapshotIDComponents(s *Snapshot) (string, uint64) { return snapshotIDComponents(s) }

// ReleaseInitialRef drops the implicit "born referenced" refcount on
// s. The workspace package calls this when it transfers ownership of
// a freshly-constructed Snapshot away from itself (e.g. when
// replacing the current snapshot with a successor).
//
// This is the only sanctioned way for code outside the cache package
// to drop a ref it did not acquire via Acquire().
func ReleaseInitialRef(s *Snapshot) { s.decref() }

// MissingFileHandle returns a file.Handle that represents a path
// known to be absent (or unreadable) at uri. Its Content/ModTime
// return err. The handle satisfies the cache's existential check
// (fileExists is false) so passing it into a StateChange.Files map
// causes the snapshot clone path to drop the URI from its file map.
func MissingFileHandle(uri protocol.DocumentURI, err error) file.Handle {
	return &diskFile{uri: uri, err: err}
}

// ambientGoEnv populates the subset of GoEnv that the cache fork
// actually reads. The W3 stub returned a zero-value GoEnv, so
// folder.Env.GoVersion was 0 and the per-package goVersion fallback
// in check.go's typeCheckInputs (which fires whenever
// metadata.Package.Module is nil — i.e. for every stdlib package)
// produced "go1.0". The linked-in go/types then rejected every
// stdlib source file that uses generics, "any", or "comparable" with
// "type parameter requires go1.18 or later", which cascaded
// upward as compiles=false through vdep transitivity and skipped
// analyzers on any workspace package that imports the stdlib.
//
// The fix populates GoVersion from runtime.Version() of the
// plaid-lint binary itself. This is the same toolchain whose
// go/types implementation we link in, so the language version we
// announce is the one the type-checker can actually accept. The
// other GoEnv fields are reachable via os.Getenv (GOCACHE etc.) but
// only matter for code paths the W3 stub still gates off; setting
// them from os.Getenv preserves the existing "process env" passthrough
// the View.Env() accessor already provides.
func ambientGoEnv() GoEnv {
	return GoEnv{
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GOCACHE:    os.Getenv("GOCACHE"),
		GOMODCACHE: os.Getenv("GOMODCACHE"),
		GOPATH:     os.Getenv("GOPATH"),
		GOFLAGS:    os.Getenv("GOFLAGS"),
		GOROOT:     runtime.GOROOT(),
		GoVersion:  runtimeMinorVersion(),
	}
}

// runtimeMinorVersion parses "go1.NN(.X)?(rc/beta/devel ...)?" from
// runtime.Version() and returns NN. Falls back to a conservative
// modern value (24) on parse failure so generics-era stdlib still
// type-checks; an outright zero here would re-trigger the
// "go1.0" symptom.
func runtimeMinorVersion() int {
	v := runtime.Version()
	const prefix = "go1."
	if len(v) < len(prefix) || v[:len(prefix)] != prefix {
		return 24
	}
	rest := v[len(prefix):]
	var minor int
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c < '0' || c > '9' {
			break
		}
		minor = minor*10 + int(c-'0')
	}
	if minor == 0 {
		return 24
	}
	return minor
}

