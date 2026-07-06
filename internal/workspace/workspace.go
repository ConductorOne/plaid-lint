// Copyright 2026 The plaid-lint authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package workspace implements the CLI-shaped public surface that
// replaces gopls's Session/View/Workspace layer.
//
// A WorkspaceState owns one module root. Callers drive invalidation
// explicitly (no LSP didChange callback) and observe consistent state
// through refcounted Snapshots. The cache package's internal
// View/Folder/Session carrier types are an implementation detail of
// this package; downstream code should hold *workspace.Snapshot
// rather than *cache.Snapshot when it needs lifecycle semantics.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/file"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
)

// ErrReleased indicates a Snapshot whose refcount already reached zero
// is being reused. Returned by (*Snapshot).Release on over-release.
var ErrReleased = errors.New("workspace: snapshot already released")

// WorkspaceState is the CLI-shaped replacement for gopls's
// Session/View/Workspace layer. One WorkspaceState owns one module
// root. The CLI engine constructs one per `golangci-lint run`
// invocation; daemons hold one per watched directory.
//
// Invalidation is explicit: callers call Invalidate when files
// change, and the next Snapshot reflects the new state. The previous
// Snapshot remains valid for outstanding refs (via refcounted
// lifecycle inherited from cache.Snapshot).
type WorkspaceState struct {
	moduleRoot string

	mu sync.RWMutex
	// view holds the cache.View this workspace state owns. It is
	// constructed once at New and reused for every snapshot.
	view *cache.View
	// current is the most recently produced snapshot. New calls
	// to Snapshot() acquire a reference on this snapshot. After
	// Invalidate, current is replaced; outstanding refs on the
	// previous snapshot keep it alive until released.
	current *cache.Snapshot
	// overlays tracks the SetContent overlay for each path. The
	// authoritative store of overlay content is in the cache's
	// overlayFS (mutated via cache.ApplyOverlay); we keep a
	// shadow copy keyed by string path so Invalidate can produce
	// file.Modification entries with the right Action.
	overlays map[string]overlayEntry
	// closed is set by Close to fail subsequent operations.
	closed bool
}

// overlayEntry records the SetContent state for one path so that
// Invalidate can synthesize a file.Modification with the right Action.
type overlayEntry struct {
	uri     protocol.DocumentURI
	content []byte
	version int32
}

// Snapshot is the public, refcount-aware wrapper around the cache's
// internal *cache.Snapshot. The CLI engine and analyzers consume
// snapshots through this type so they don't have to reach into the
// cache package.
type Snapshot struct {
	// inner is the underlying cache snapshot. inner already
	// implements refcounted lifecycle; Snapshot wraps it so we
	// can expose a Release-or-error API instead of the
	// upstream-shaped Acquire()/release pair.
	inner *cache.Snapshot
	// release is the function returned by inner.Acquire(). Calling
	// it once decrements inner's refcount. We guard against
	// double-release with the released flag.
	release func()
	// mu protects released.
	mu       sync.Mutex
	released bool

	// id is the wire-format snapshot id.
	id string
}

// New constructs a WorkspaceState rooted at moduleRoot. moduleRoot
// is interpreted as a filesystem path; if it is not absolute it is
// resolved against the current working directory.
func New(moduleRoot string) *WorkspaceState {
	return NewWithCache(moduleRoot, nil)
}

// NewWithCache is like New but uses the supplied *cache.Cache instead
// of creating a fresh one. The Cache must already have any optional
// L1 / L2 attachments installed (AttachL1, AttachL2) before this call;
// those setters panic if invoked after a View is created. Pass nil to
// get the default behaviour (fresh cache, no L1/L2).
func NewWithCache(moduleRoot string, c *cache.Cache) *WorkspaceState {
	return NewWithCacheAndOptions(moduleRoot, c, nil)
}

// NewWithCacheAndOptions is like NewWithCache but uses the supplied
// *settings.Options for the underlying View. Pass nil to get the
// default Options (settings.DefaultOptions()). Used by the engine to
// thread `run.tests` (and any other build-time toggle) into the
// workspace loader.
func NewWithCacheAndOptions(moduleRoot string, c *cache.Cache, opts *settings.Options) *WorkspaceState {
	if !filepath.IsAbs(moduleRoot) {
		if abs, err := filepath.Abs(moduleRoot); err == nil {
			moduleRoot = abs
		}
	}
	if opts == nil {
		opts = settings.DefaultOptions()
	}
	rootURI := protocol.URIFromPath(moduleRoot)
	view, snap := cache.NewView(c, rootURI, opts)
	ws := &WorkspaceState{
		moduleRoot: moduleRoot,
		view:       view,
		current:    snap,
		overlays:   make(map[string]overlayEntry),
	}
	return ws
}

// ModuleRoot returns the absolute path the WorkspaceState was
// constructed against.
func (ws *WorkspaceState) ModuleRoot() string { return ws.moduleRoot }

// SetContent records an overlay for the given absolute file path.
// Subsequent Snapshot() calls will return a snapshot whose ReadFile
// reflects this content, but only after an Invalidate call covering
// path — SetContent itself does not produce a new snapshot. (This
// mirrors gopls's "modifications stage, then DidModifyFiles flushes
// them" model: it keeps the steady-state snapshot stable across
// editor keystrokes and only allocates a new snapshot when the CLI
// driver decides to refresh.)
//
// Passing content == nil clears the overlay, restoring the on-disk
// view.
func (ws *WorkspaceState) SetContent(path string, content []byte) {
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	uri := protocol.URIFromPath(path)
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.closed {
		return
	}
	if content == nil {
		delete(ws.overlays, path)
		cache.ApplyOverlay(ws.view, uri, nil, 0, file.UnknownKind)
		return
	}
	prev, ok := ws.overlays[path]
	var version int32
	if ok {
		version = prev.version + 1
	} else {
		version = 1
	}
	ws.overlays[path] = overlayEntry{
		uri:     uri,
		content: content,
		version: version,
	}
	cache.ApplyOverlay(ws.view, uri, content, version, kindFor(path))
}

// Invalidate produces a new snapshot whose state reflects the given
// changed paths. The previous snapshot remains valid for callers
// holding outstanding refs.
//
// Each path in paths must be an absolute filesystem path. Paths
// that have a SetContent overlay are treated as Change modifications;
// paths without an overlay are treated as on-disk Change events
// (Save), which triggers the cache to re-read them from disk on next
// access.
//
// The returned string is the snapshot's ID (format:
// "<viewID>-<sequenceID>"), useful for log lines and W4 cache keys.
//
// If WorkspaceState is closed, the returned id is the empty string.
func (ws *WorkspaceState) Invalidate(paths []string) string {
	ws.mu.Lock()
	if ws.closed {
		ws.mu.Unlock()
		return ""
	}

	// Build StateChange.Files: for overlaid paths, ApplyOverlay
	// has already updated the view's overlay map, so a fresh
	// ReadFile through view.fs returns the overlay handle. For
	// non-overlaid paths, ReadFile falls through to the disk fs.
	changedFiles := make(map[protocol.DocumentURI]file.Handle, len(paths))
	modifications := make([]file.Modification, 0, len(paths))
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
		}
		uri := protocol.URIFromPath(p)
		fh, err := ws.view.ReadFile(context.Background(), uri)
		if err != nil {
			// view.ReadFile signals missing files in-band on the
			// returned handle, so a function-level error here is
			// exceptional (e.g. context cancellation). With no
			// overlay covering the path, emit a Delete so the
			// snapshot's file map drops it rather than retaining
			// stale state.
			if ws.hasOverlay(p) {
				continue
			}
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("workspace: Invalidate(%q): %v (treating as deletion)", p, err)
			}
			changedFiles[uri] = cache.MissingFileHandle(uri, err)
			modifications = append(modifications, file.Modification{
				URI:    uri,
				Action: file.Delete,
				OnDisk: true,
			})
			continue
		}
		changedFiles[uri] = fh

		action := actionFor(p, fh)
		modifications = append(modifications, file.Modification{
			URI:    uri,
			Action: action,
			OnDisk: !ws.hasOverlay(p),
		})
	}

	change := cache.StateChange{
		Modifications: modifications,
		Files:         changedFiles,
	}

	// CloneSnapshot returns a fresh snapshot with refcount=1
	// (the "born referenced" ref the WorkspaceState now owns).
	// We pass a no-op done — the parseCache lifecycle is anchored
	// to the View's initial snapshot, not its clones.
	prev := ws.current
	next, _ := cache.CloneSnapshot(context.Background(), prev, change, func() {})
	ws.current = next
	ws.mu.Unlock()

	// Transfer of ownership: drop the WS-owned ref on prev.
	// Outstanding refs acquired via ws.Snapshot() keep prev alive.
	cache.ReleaseInitialRef(prev)

	viewID, seq := cache.SnapshotIDComponents(next)
	return fmt.Sprintf("%s-%d", viewID, seq)
}

// hasOverlay reports whether path has a SetContent overlay. Caller
// must hold ws.mu (read or write).
func (ws *WorkspaceState) hasOverlay(path string) bool {
	_, ok := ws.overlays[path]
	return ok
}

// Snapshot returns a refcounted handle to the current snapshot. The
// caller must call Release on the returned Snapshot when done.
//
// If WorkspaceState is closed, Snapshot returns nil.
func (ws *WorkspaceState) Snapshot() *Snapshot {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	if ws.closed || ws.current == nil {
		return nil
	}
	release := ws.current.Acquire()
	viewID, seq := cache.SnapshotIDComponents(ws.current)
	return &Snapshot{
		inner:   ws.current,
		release: release,
		id:      fmt.Sprintf("%s-%d", viewID, seq),
	}
}

// Close releases resources held by the WorkspaceState. After Close,
// SetContent and Invalidate are no-ops and Snapshot returns nil.
// Outstanding Snapshot refs remain valid until their callers Release.
func (ws *WorkspaceState) Close() {
	ws.mu.Lock()
	if ws.closed {
		ws.mu.Unlock()
		return
	}
	ws.closed = true
	prev := ws.current
	view := ws.view
	ws.current = nil
	ws.view = nil
	ws.mu.Unlock()

	// Drop the WS-owned initial ref on the current snapshot.
	// Outstanding external refs keep prev alive; the parseCache
	// goroutine stops when the last ref is released.
	if prev != nil {
		cache.ReleaseInitialRef(prev)
	}
	if view != nil {
		view.Shutdown()
	}
}

// Inner returns the underlying *cache.Snapshot. Most callers should
// use the higher-level methods this Snapshot exposes; Inner is the
// escape hatch for code that needs direct access during the W4–W6
// build-out.
func (s *Snapshot) Inner() *cache.Snapshot { return s.inner }

// ID returns the wire-format snapshot ID.
func (s *Snapshot) ID() string { return s.id }

// Release decrements the snapshot's refcount. When the refcount hits
// zero the underlying snapshot's resources are freed. Subsequent
// Release calls on the same Snapshot return ErrReleased.
func (s *Snapshot) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return ErrReleased
	}
	s.released = true
	if s.release != nil {
		s.release()
	}
	return nil
}

// kindFor picks a file.Kind from a path's extension. The cache
// fork's FileKind machinery applies template extensions on top; for
// the overlay we only need the basic kind.
func kindFor(path string) file.Kind {
	switch filepath.Ext(path) {
	case ".go":
		return file.Go
	case ".mod":
		return file.Mod
	case ".sum":
		return file.Sum
	case ".work":
		return file.Work
	case ".s":
		return file.Asm
	default:
		return file.UnknownKind
	}
}

// actionFor maps an on-disk + handle pair to a file.Action. We can't
// always recover the right action (Create vs Change vs Delete)
// without the prior state, so we approximate: if the file is
// readable, it's a Change; if it errors, it's a Delete.
func actionFor(path string, fh file.Handle) file.Action {
	if fh == nil {
		return file.Delete
	}
	if _, err := fh.Content(); err != nil {
		return file.Delete
	}
	if _, err := os.Stat(path); err != nil {
		// File only exists as an overlay (not yet on disk):
		// treat as Create from the cache's perspective.
		return file.Create
	}
	return file.Change
}
