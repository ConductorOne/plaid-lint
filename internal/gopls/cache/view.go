// Copyright 2026 The plaid-lint authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Upstream Go: BSD-3-Clause. See internal/gopls/LICENSE.upstream.

// This file holds View/Folder/Session — package-internal carrier
// types that the forked cache/snapshot code reads. The public
// CLI-shaped surface lives in internal/workspace and wires instances
// of these types as implementation detail.
//
// These types were dropped from upstream gopls's exported
// Session/View surface at fork time (Category C); the pieces
// preserved here are only the fields/methods
// the cache fork reads.

package cache

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/conductorone/plaid-lint/internal/gopls/cache/typerefs"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/gocommand"
	"github.com/conductorone/plaid-lint/internal/gopls/internal/modindex"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
)

// ViewType identifies the kind of build configuration backing a View.
type ViewType int

const (
	GoPackagesDriverView ViewType = iota
	GOPATHView
	GoModView
	GoWorkView
	AdHocView
)

func (t ViewType) String() string {
	switch t {
	case GoPackagesDriverView:
		return "GoPackagesDriver"
	case GOPATHView:
		return "GOPATH"
	case GoModView:
		return "GoMod"
	case GoWorkView:
		return "GoWork"
	case AdHocView:
		return "AdHoc"
	default:
		return "Unknown"
	}
}

func (typ ViewType) usesModules() bool {
	switch typ {
	case GoModView, GoWorkView:
		return true
	default:
		return false
	}
}

// GoEnv is a stubbed bag of Go-environment values that the cache code
// reads during load/check.
type GoEnv struct {
	GOOS                      string
	GOARCH                    string
	GOCACHE                   string
	GOMODCACHE                string
	GOPATH                    string
	GOPRIVATE                 string
	GOFLAGS                   string
	GO111MODULE               string
	GOTOOLCHAIN               string
	GOROOT                    string
	GoVersion                 int
	GoVersionOutput           string
	ExplicitGOWORK            string
	EffectiveGOPACKAGESDRIVER string
}

// Folder carries the per-workspace configuration.
type Folder struct {
	Dir     protocol.DocumentURI
	Name    string
	Options *settings.Options
	Env     GoEnv
}

// View is a package-internal carrier for fields snapshot.go reads
// from `s.view.*`. The public WorkspaceState in internal/workspace
// owns one of these per module root and drives its lifecycle.
type View struct {
	id          string
	folder      *Folder
	gocmdRunner *gocommand.Runner

	// c is the back-reference to the shared Cache. Held so the type-check
	// path can reach Cache.l2 (the plaid-lint content-addressed L2)
	// without threading the *Cache through every typeCheckBatch caller.
	c *Cache

	pkgIndex   *typerefs.PackageIndex
	parseCache *parseCache
	fs         *overlayFS

	cancelInitialWorkspaceLoad context.CancelFunc

	initialWorkspaceLoad chan struct{}
	initializationSema   chan struct{}

	filterFuncOnce sync.Once
	_filterFunc    func(protocol.DocumentURI) bool

	// Inlined viewDefinition fields (the upstream View embeds *viewDefinition).
	typ                  ViewType
	root                 protocol.DocumentURI
	gomod                protocol.DocumentURI
	gowork               protocol.DocumentURI
	workspaceModFiles    map[protocol.DocumentURI]struct{}
	workspaceModFilesErr error
	envOverlay           map[string]string
}

// ID returns the unique ID of this View.
func (v *View) ID() string { return v.id }

// Env returns the environment variables, as a slice of "K=V" strings,
// implied by the carrier folder.
//
// The Phase 1 CLI engine does not run go commands that need a full
// resolved env, so this returns nil. If a later phase adds invocations
// that require env, the WorkspaceState can populate folder.Env and
// expose it here.
// Env returns the environment variables to use when invoking the go
// command. The W3 stub returned nil (forcing the go-tooling into an
// unconfigured state); we now pass through the current process's
// environment so that GOCACHE / GOPATH / GOFLAGS etc. flow through to
// the subprocess. The CLI engine layer is responsible for any
// additional injection (e.g. GOPROXY=off, GOFLAGS=-mod=...) before
// this point.
func (v *View) Env() []string { return os.Environ() }

// Folder returns the underlying folder for this view.
func (v *View) Folder() *Folder { return v.folder }

// GoVersion returns the X in "Go 1.X" for this view.
func (v *View) GoVersion() int {
	if v.folder == nil {
		return 0
	}
	return v.folder.Env.GoVersion
}

// GoCommandRunner returns the go command runner used by this view.
func (v *View) GoCommandRunner() *gocommand.Runner { return v.gocmdRunner }

// ModFiles returns the go.mod URIs for this view's workspace modules
// in iteration order. The caller is responsible for sorting if a
// stable order is required.
func (v *View) ModFiles() []protocol.DocumentURI {
	out := make([]protocol.DocumentURI, 0, len(v.workspaceModFiles))
	for uri := range v.workspaceModFiles {
		out = append(out, uri)
	}
	return out
}

// ModcacheIndex returns the modcache index for this view, or nil.
// goimports-based import suggestions are out of scope for the fork,
// so this always returns nil.
func (v *View) ModcacheIndex() (*modindex.Index, error) { return nil, nil }

// filterFunc returns the directory-filter function for this view.
// The default implementation accepts every URI; a future phase can
// thread a filter from the CLI config through Folder.Options.
func (v *View) filterFunc() func(protocol.DocumentURI) bool {
	v.filterFuncOnce.Do(func() {
		v._filterFunc = func(protocol.DocumentURI) bool { return false }
	})
	return v._filterFunc
}

// Session is a package-internal carrier whose only role is to satisfy
// keys.go's SessionKey type identity. No Session methods are
// exercised in the cache fork; the public CLI surface is
// WorkspaceState in internal/workspace.
type Session struct {
	id string
}

// ID returns the session id.
func (s *Session) ID() string { return s.id }

// StateChange describes the file modifications that produced a new
// snapshot. Carried over from the deleted view.go so snapshot.go's
// clone() can compile.
type StateChange struct {
	Modifications      []file.Modification
	Files              map[protocol.DocumentURI]file.Handle
	ModuleUpgrades     map[protocol.DocumentURI]map[string]string
	CompilerOptDetails map[protocol.DocumentURI]bool
}

// initialize performs the first-time workspace load for this
// snapshot. The CLI engine does not yet drive a metadata load through
// this path (load.go is invoked directly when callers need metadata);
// this method only flips `initialized` and signals the view's
// one-shot initialWorkspaceLoad channel so callers blocked in
// awaitLoaded() unblock. A later phase can fold a real
// driver-managed load here.
func (s *Snapshot) initialize(ctx context.Context, firstAttempt bool) {
	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()
	if firstAttempt && s.view != nil && s.view.initialWorkspaceLoad != nil {
		select {
		case <-s.view.initialWorkspaceLoad:
		default:
			close(s.view.initialWorkspaceLoad)
		}
	}
}

// InitializeWorkspace performs the first-time workspace load. It
// drives s.load over the view's root scope so that the metadata graph
// is populated before any caller blocks in awaitLoaded(). The
// initialWorkspaceLoad channel is closed on completion (whether or not
// the load succeeded), so subsequent calls become no-ops.
//
// Production callers — the plaid-lint CLI engine and the test
// pipeline driver — are expected to call this exactly once after
// constructing the workspace, before invoking Analyze / TypeCheck /
// MetadataForFile.
func (s *Snapshot) InitializeWorkspace(ctx context.Context) error {
	return s.InitializeWorkspaceWithPatterns(ctx, nil)
}

// InitializeWorkspaceWithPatterns is InitializeWorkspace narrowed to
// the given go/packages query patterns. When patterns is empty the
// load falls back to the view-wide scope (`./...` under the module
// root, matching InitializeWorkspace). When patterns is non-empty
// each query is loaded as a packageLoadScope; the analysis surface
// (Snapshot.WorkspacePackages) is the union of every matched
// metadata.Package that survives the workspace-package heuristic
// (isWorkspacePackageLocked), so transitive deps reachable via
// imports populate the metadata graph but are not themselves driven
// as analysis roots — same shape as upstream gopls's per-view load.
//
// The narrowing exists so a `plaid-lint run pkg/foo/...` against
// a multi-thousand-package module does not pay the wall-time and
// RSS cost of loading every transitively-importable package in the
// repo when the user only asked about one subtree.
func (s *Snapshot) InitializeWorkspaceWithPatterns(ctx context.Context, patterns []string) error {
	s.mu.Lock()
	already := s.initialized
	s.mu.Unlock()
	if already {
		return nil
	}
	scopes := scopesForPatterns(s.view.typ, patterns)
	err := s.load(ctx, NoNetwork, scopes...)
	s.initialize(ctx, true)
	return err
}

// scopesForPatterns turns a list of go/packages query patterns into
// the loadScope set s.load consumes. An empty patterns slice produces
// the view-wide scope so callers that don't care about narrowing get
// the historical behavior. An AdHocView ignores user patterns and
// always loads its single directory — go/packages's ad-hoc handling
// expects exactly the view-wide scope.
//
// Bare relative directory patterns (`pkg/foo`, `pkg/foo/...`) are
// rewritten to `./pkg/foo` before reaching packages.Load. Without the
// `./` prefix, `go list` treats them as import-path patterns and, in
// a single-module repo whose import root doesn't match the working
// directory, fails to resolve any package — packages.Load returns a
// synthetic placeholder with no GoFiles, the loader drops it, and
// the workspace ends up with zero packages. Patterns that already
// look path-shaped (`./...`, `../foo`, `/abs/path`) or that look
// like import paths (a `.` before the first `/`, e.g.
// `github.com/foo/bar`) are passed through unchanged.
func scopesForPatterns(viewTyp ViewType, patterns []string) []loadScope {
	if len(patterns) == 0 || viewTyp == AdHocView {
		return []loadScope{viewLoadScope{}}
	}
	scopes := make([]loadScope, 0, len(patterns))
	for _, p := range patterns {
		if p == "" {
			continue
		}
		scopes = append(scopes, packageLoadScope(normalizePackagePattern(p)))
	}
	if len(scopes) == 0 {
		return []loadScope{viewLoadScope{}}
	}
	return scopes
}

// normalizePackagePattern prefixes bare relative directory patterns
// with `./` so packages.Load treats them as filesystem paths rather
// than import-path patterns. Returns p unchanged when it already has
// a path-like prefix (`./`, `../`, `.`, absolute `/`) or looks like
// a module-style import path (a dot in the first path segment, e.g.
// `github.com/foo/bar`).
func normalizePackagePattern(p string) string {
	if p == "" || p == "." {
		return p
	}
	// Absolute paths and explicit relative paths pass through.
	if p[0] == '/' || p[0] == '.' {
		return p
	}
	// Import paths typically have a domain-like first segment
	// (`gopkg.in/...`, `github.com/...`, `gitlab.com/...`). The "."
	// has to appear before the first "/" to count; "pkg/foo.bar/baz"
	// is a directory with a dotted component, not an import path.
	if i := strings.Index(p, "/"); i > 0 && strings.Contains(p[:i], ".") {
		return p
	}
	// Single-segment bare names — `pkg`, `cmd`, `pkg/...` — and any
	// other bare relative form become `./<p>`.
	return "./" + p
}

// isWorkspaceFile reports whether uri matches any of the workspace
// file globs. Carrier uses path/filepath.Match instead of the upstream
// glob package; this is sufficient for the common cases the cache
// fork exercises (single-segment patterns like "go.work*").
func isWorkspaceFile(uri protocol.DocumentURI, workspaceFiles []string) bool {
	path := uri.Path()
	for _, pat := range workspaceFiles {
		if ok, _ := filepath.Match(pat, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

// fileExists reports whether the file has Content (overlay-aware).
func fileExists(fh file.Handle) bool {
	_, err := fh.Content()
	return err == nil
}

// findRootPattern walks up the directory tree looking for a file named
// basename, returning its URI or "" if not found.
func findRootPattern(ctx context.Context, dirURI protocol.DocumentURI, basename string, fs file.Source) (protocol.DocumentURI, error) {
	dir := dirURI.Path()
	for dir != "" {
		target := filepath.Join(dir, basename)
		uri := protocol.URIFromPath(target)
		fh, err := fs.ReadFile(ctx, uri)
		if err != nil {
			return "", err
		}
		if fileExists(fh) {
			return uri, nil
		}
		next, _ := filepath.Split(strings.TrimRight(dir, string(filepath.Separator)))
		if next == dir {
			break
		}
		dir = next
	}
	return "", nil
}

// allFilesExcluded reports whether every file is rejected by filterFunc.
func allFilesExcluded(files []string, filterFunc func(protocol.DocumentURI) bool) bool {
	for _, f := range files {
		uri := protocol.URIFromPath(f)
		if !filterFunc(uri) {
			return false
		}
	}
	return true
}
