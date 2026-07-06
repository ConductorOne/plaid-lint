// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/conductorone/plaid-lint/internal/output"
)

// Cache is the content-addressed store for subprocess wrappers.
//
// Layout (parallel to internal/cache's L1 location, distinct subtree):
//
//	${ROOT}/subproc/<shard>/<key-hex>      # gob-ish JSON-encoded entry
//
// The cache is best-effort: write failures are surfaced to the
// caller (so tests can assert them) but the engine treats a Store
// error as non-fatal — a missed cache write degrades to "re-run next
// time," not "abort lint."
//
// Concurrency: Cache is safe for concurrent use by any number of
// goroutines. The file-system layer uses atomic write-then-rename
// (via a temp file in the same shard dir + os.Rename) which is
// atomic on POSIX. The in-process mu guards no shared state today;
// it exists so future read-through coalescing can be added without
// breaking the public API.
type Cache interface {
	// Lookup returns the cached diagnostics for key, a found flag,
	// and an error. A miss is (nil, false, nil). A corrupt entry is
	// (nil, false, err) — corrupt entries are also unlinked so the
	// next Store will succeed.
	Lookup(key string) ([]output.Diagnostic, bool, error)

	// Store writes diags under key. Idempotent — repeated Stores
	// with the same key overwrite. Returns an error on write
	// failure but does not panic.
	Store(key string, diags []output.Diagnostic) error
}

// fsCache is the on-disk implementation of Cache.
type fsCache struct {
	mu   sync.Mutex
	root string
}

// OpenCache opens (creating if necessary) a Cache rooted at root. If
// root is empty, [DefaultCacheRoot] is used.
func OpenCache(root string) (Cache, error) {
	if root == "" {
		r, err := DefaultCacheRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("subproc cache: mkdir %s: %w", root, err)
	}
	return &fsCache{root: root}, nil
}

// DefaultCacheRoot returns
// ${XDG_CACHE_HOME:-$HOME/.cache}/plaid-lint/subproc, parallel to
// the L1 cache's location.
func DefaultCacheRoot() (string, error) {
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "plaid-lint", "subproc"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("subproc cache: resolve home dir: %w", err)
	}
	return filepath.Join(home, ".cache", "plaid-lint", "subproc"), nil
}

// cacheFormatVersion is stamped into every entry so a later
// incompatible change can ignore-and-replace old entries instead of
// crashing on a decode error.
const cacheFormatVersion = 1

type cacheEntry struct {
	Version int                 `json:"v"`
	Diags   []output.Diagnostic `json:"diags"`
}

func (c *fsCache) entryPath(key string) string {
	// Two-char sharding mirrors internal/cache.
	shard := key
	if len(shard) > 2 {
		shard = shard[:2]
	}
	return filepath.Join(c.root, shard, key)
}

// Lookup implements Cache.
func (c *fsCache) Lookup(key string) ([]output.Diagnostic, bool, error) {
	if key == "" {
		return nil, false, errors.New("subproc cache: empty key")
	}
	path := c.entryPath(key)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("subproc cache: read %s: %w", key, err)
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		// Corrupt entry — unlink and report a miss-with-error so the
		// caller can decide whether to log. Best-effort cleanup.
		_ = os.Remove(path)
		return nil, false, fmt.Errorf("subproc cache: decode %s: %w", key, err)
	}
	if entry.Version != cacheFormatVersion {
		_ = os.Remove(path)
		return nil, false, nil
	}
	return entry.Diags, true, nil
}

// Store implements Cache.
func (c *fsCache) Store(key string, diags []output.Diagnostic) error {
	if key == "" {
		return errors.New("subproc cache: empty key")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := cacheEntry{Version: cacheFormatVersion, Diags: diags}
	data, err := json.Marshal(&entry)
	if err != nil {
		return fmt.Errorf("subproc cache: encode %s: %w", key, err)
	}

	path := c.entryPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("subproc cache: mkdir shard for %s: %w", key, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+key+".*")
	if err != nil {
		return fmt.Errorf("subproc cache: create temp for %s: %w", key, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("subproc cache: write temp for %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("subproc cache: close temp for %s: %w", key, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("subproc cache: rename for %s: %w", key, err)
	}
	return nil
}

// CacheKey computes the deterministic content-hash key for a
// subprocess run. The inputs are exactly:
//
//   - linterName: the [Runner.Name] value. Different linters never
//     share a cache row.
//
//   - linterVersion: an opaque version string the wrapper supplies
//     (typically the linter binary's `-version` output or, for
//     embedded analyzers, the staticcheck honnef.co/go/tools module
//     version). A version bump invalidates cached entries.
//
//   - settingsHash: a hash of the per-linter settings sub-block
//     (config.LintersSettings.<name>) that the wrapper has already
//     reduced to a stable byte slice. The framework cannot compute
//     this for the wrapper because the typed settings shape is
//     per-linter; the wrapper hashes a JSON re-encoding of its
//     concrete settings struct in a fixed key order.
//
//   - workspace: hashed via [WorkspaceContentHash]. The transitive
//     Go-file content hash captures any source edit; BuildTags and
//     Env are included to invalidate when the build environment
//     itself changes (e.g. enabling `//go:build integration` files).
//
// The returned key is a 64-character lower-hex sha256, suitable as
// both filename and cache index.
func CacheKey(linterName, linterVersion, settingsHash string, workspace WorkspaceRef) (string, error) {
	wsHash, err := WorkspaceContentHash(workspace)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	// Length-prefix each input so concatenation can't collide.
	writeField(h, "linter", linterName)
	writeField(h, "version", linterVersion)
	writeField(h, "settings", settingsHash)
	writeField(h, "workspace", wsHash)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeField writes a "<tag>:<len>:<value>" length-prefixed segment
// to h. The fixed framing prevents inputs like
// ("ab", "c") and ("a", "bc") from colliding.
func writeField(h interface{ Write([]byte) (int, error) }, tag, value string) {
	var hdr bytes.Buffer
	fmt.Fprintf(&hdr, "%s:%d:", tag, len(value))
	_, _ = h.Write(hdr.Bytes())
	_, _ = h.Write([]byte(value))
}

// WorkspaceContentHash computes a sha256 over the sorted set of
// (relative path, sha256-of-content) pairs for every .go file
// rooted at workspace.ModuleRoot, plus the sorted build tags and
// env slice. The hash invalidates on any source edit, build-tag
// change, or env change. It does NOT walk into testdata/ or
// directories named vendor/ to keep the hash bounded for large
// modules; those are explicitly out of scope for the unused /
// unparam / plugin linters.
//
// The function reads the filesystem; expect O(files) work. Callers
// that re-Run inside a single invocation should hold onto the
// computed key rather than re-deriving it.
func WorkspaceContentHash(workspace WorkspaceRef) (string, error) {
	if workspace.ModuleRoot == "" {
		return "", errors.New("subproc cache: WorkspaceRef.ModuleRoot is required")
	}
	if !filepath.IsAbs(workspace.ModuleRoot) {
		return "", fmt.Errorf("subproc cache: WorkspaceRef.ModuleRoot must be absolute, got %q", workspace.ModuleRoot)
	}

	type fileHash struct {
		rel  string
		hash [32]byte
	}
	var files []fileHash
	err := filepath.WalkDir(workspace.ModuleRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "testdata" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return fmt.Errorf("read %s: %w", p, err)
		}
		rel, err := filepath.Rel(workspace.ModuleRoot, p)
		if err != nil {
			return fmt.Errorf("rel %s: %w", p, err)
		}
		files = append(files, fileHash{rel: filepath.ToSlash(rel), hash: sha256.Sum256(data)})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("subproc cache: walk %s: %w", workspace.ModuleRoot, err)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	h := sha256.New()
	for _, f := range files {
		writeField(h, "file", f.rel)
		_, _ = h.Write(f.hash[:])
	}

	tags := append([]string(nil), workspace.BuildTags...)
	sort.Strings(tags)
	for _, t := range tags {
		writeField(h, "tag", t)
	}
	env := append([]string(nil), workspace.Env...)
	sort.Strings(env)
	for _, e := range env {
		writeField(h, "env", e)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
