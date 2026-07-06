// Package cache implements the plaid-lint content-addressed cache.
//
// This package is the L1/L2 storage primitive layer.
// It is intentionally separate from internal/gopls/cache (the vendored gopls
// incremental type-checker). This cache stores:
//
//   - L1 entries: per-analyzer-per-package results (diagnostics + facts).
//   - L2 entries: per-package type-checking results (export data + facts blob).
//
// On-disk layout (sharded by the first 2 hex chars of the action ID):
//
//	${ROOT}/
//	  typecheck/<shard>/<actionID-hex>           # L2
//	  analyzer/<analyzer>/<shard>/<actionID-hex> # L1
//	  meta/cache-version
//
// Writes are content-addressed and parallel-safe: first-writer-wins via
// link(2) O_EXCL semantics (no rename overwrite, no global lock).
package cache

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheVersion is the format-version tag stamped into meta/cache-version.
// Bump when the on-disk encoding of L1Entry or L2Entry changes in a way
// that invalidates previously-written bytes.
//
// Version 2 (W5, 2026-05-17): adds L2Entry.FileSetSnapshot section.
// Version 3 (W7, 2026-05-17): adds L1Entry.Result section.
const CacheVersion = "3"

// DefaultGCThreshold is the default mtime-based pruning horizon.
// Entries with time.Since(mtime) > DefaultGCThreshold are removed by GC.
const DefaultGCThreshold = 30 * 24 * time.Hour

// GCInterval is the minimum spacing between background GC passes. The
// Open path consults <root>/.last-gc to decide whether to launch a new
// pass; if the file's mtime is within GCInterval the pass is skipped.
// Entries still expire on the next eligible Open.
const GCInterval = 6 * time.Hour

// lastGCMarker is the filename inside Cache.root whose mtime records
// when the last GC pass completed. Read at Open, written at GC end.
const lastGCMarker = ".last-gc"

// Cache is a handle to an on-disk content-addressed cache rooted at Root.
// A Cache is safe for concurrent use by any number of goroutines and any
// number of processes; correctness comes from content-addressed file names
// plus the link(2) O_EXCL atomic-write primitive.
type Cache struct {
	root string

	// backend is the storage seam introduced in Stage 1.
	// All hot-path reads/writes/probes dispatch through it; the L1/L2
	// methods (ReadL1, WriteL1, HasL1, ReadL2, WriteL2, HasL2) are
	// envelope-encoding wrappers that call backend.{Get,Put,Has} with
	// the namespace strings defined in backend.go. GC and init still
	// operate on c.root directly because they are layout-aware rather
	// than (namespace, id)-keyed.
	backend backend

	// gcWG tracks the background GC goroutine kicked off by Open. Tests
	// that need a deterministic GC pass (e.g. assertions on which entries
	// remain) wait on this via WaitForGC. Production callers don't wait —
	// the goroutine completes asynchronously without blocking the CLI.
	gcWG sync.WaitGroup

	// gcLaunched is true iff Open's shouldRunGC check passed and the
	// goroutine was started. Test-only; exposed via GCWasLaunched.
	gcLaunched bool

	// closeOnce guards Close so it is safe to call from multiple defer
	// sites (e.g. a top-level defer plus a signal handler).
	closeOnce sync.Once
	closeErr  error
}

// GCWasLaunched reports whether the most recent Open call started the
// background GC goroutine. Test-only. Production callers must not
// rely on this.
func (c *Cache) GCWasLaunched() bool { return c.gcLaunched }

// Open opens (creating if necessary) a cache rooted at root. If root is
// empty, DefaultRoot() is used.
//
// Open creates the directory skeleton, writes meta/cache-version if absent,
// and — if the previous pass completed more than GCInterval ago — kicks
// off a best-effort GC pass with the default threshold in a background
// goroutine. GC errors are suppressed (the cache is best-effort by
// design). On a populated cache the GC walk is a `filepath.WalkDir` over
// every shard which can take seconds; keeping it off the CLI's critical
// path matters more than running it synchronously, since stale entries
// expire on the next eligible Open regardless. Tests that need a
// deterministic GC pass can wait via WaitForGC.
//
// Two env vars override the gating:
//
//   - PLAID_FORCE_GC=1 — run GC regardless of the .last-gc timestamp.
//   - PLAID_DISABLE_GC=1 — skip the GC launch entirely (test/diagnosis).
func Open(root string) (*Cache, error) {
	return openWithTier(root, "")
}

// OpenForTier opens a Cache and selects its backend with per-tier
// override resolution. Pass TierL1 or TierL2 from the cache package so
// PLAID_L1_CACHE_BACKEND / PLAID_L2_CACHE_BACKEND apply; an empty
// tier behaves like Open (global env var only).
//
// Backwards compatibility: Open continues to honour PLAID_CACHE_BACKEND
// alone, so existing callers (and existing single-backend deployments)
// are unaffected. New CLI code paths should prefer this entry point.
func OpenForTier(root, tier string) (*Cache, error) {
	return openWithTier(root, tier)
}

func openWithTier(root, tier string) (*Cache, error) {
	if root == "" {
		r, err := DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	c := &Cache{root: root}
	b, err := selectBackendForTier(root, tier)
	if err != nil {
		return nil, err
	}
	c.backend = b
	if err := c.init(); err != nil {
		return nil, err
	}
	if c.shouldRunGC() {
		c.gcLaunched = true
		c.gcWG.Add(1)
		go func() {
			defer c.gcWG.Done()
			_ = c.GC(DefaultGCThreshold)
			c.stampLastGC()
		}()
	}
	return c, nil
}

// shouldRunGC reports whether Open should launch the background GC
// goroutine. PLAID_DISABLE_GC=1 forces false; PLAID_FORCE_GC=1
// forces true; otherwise it returns true iff time.Since(.last-gc.mtime)
// > GCInterval or the marker file is missing/unreadable.
func (c *Cache) shouldRunGC() bool {
	if os.Getenv("PLAID_DISABLE_GC") == "1" {
		return false
	}
	if os.Getenv("PLAID_FORCE_GC") == "1" {
		return true
	}
	info, err := os.Stat(filepath.Join(c.root, lastGCMarker))
	if err != nil {
		// Missing or unreadable marker: prefer an extra GC pass over
		// silently skipping. Covers fresh-cache (first Open) and IO
		// errors uniformly.
		return true
	}
	return time.Since(info.ModTime()) > GCInterval
}

// stampLastGC writes (or touches) the .last-gc marker at the cache
// root. Errors are suppressed: a failed stamp costs at worst one
// extra GC pass on the next Open.
func (c *Cache) stampLastGC() {
	path := filepath.Join(c.root, lastGCMarker)
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644); err == nil {
		_ = f.Close()
	}
}

// WaitForGC blocks until the background GC pass launched by Open has
// completed. Test-only: production callers don't need to synchronise on
// GC, since stale entries are pruned opportunistically across runs.
func (c *Cache) WaitForGC() {
	c.gcWG.Wait()
}

// Close releases any resources held by the backend. For the local
// backend this is a no-op; for the gocacheprog backend it terminates
// the helper subprocess so the parent process can exit cleanly. Safe
// to call multiple times.
//
// Close does NOT wait for the background GC goroutine — GC is
// best-effort and may outlive the cache handle. Tests that need
// determinism continue to use WaitForGC.
func (c *Cache) Close() error {
	c.closeOnce.Do(func() {
		if closer, ok := c.backend.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// Path returns the absolute path to the cache root.
func (c *Cache) Path() string { return c.root }

// DefaultRoot returns the resolved on-disk cache root. Resolution order
// (first match wins):
//
//  1. PLAID_CACHE_DIR — explicit plaid-specific override. Used as a
//     raw path (no "plaid-lint" suffix appended); the user picked the
//     exact location.
//  2. GOLANGCI_LINT_CACHE — compat with deployments that already pin
//     golangci-lint's cache to a specific path. Raw path, same shape.
//  3. XDG_CACHE_HOME — standard Linux convention. Suffixed with
//     "plaid-lint".
//  4. os.UserCacheDir — Go's portable fallback (~/.cache on Linux,
//     ~/Library/Caches on macOS, %LocalAppData% on Windows). Suffixed
//     with "plaid-lint".
//  5. os.TempDir / "plaid-lint-cache" — last-resort fallback for
//     environments where home-dir resolution fails (e.g. headless
//     containers with no $HOME).
//
// The (string, error) signature is preserved for caller compatibility,
// but step 5 makes the error path unreachable in practice.
func DefaultRoot() (string, error) {
	if v := os.Getenv("PLAID_CACHE_DIR"); v != "" {
		return v, nil
	}
	if v := os.Getenv("GOLANGCI_LINT_CACHE"); v != "" {
		return v, nil
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "plaid-lint"), nil
	}
	if home, err := os.UserCacheDir(); err == nil {
		return filepath.Join(home, "plaid-lint"), nil
	}
	return filepath.Join(os.TempDir(), "plaid-lint-cache"), nil
}

func (c *Cache) init() error {
	for _, sub := range []string{"typecheck", "analyzer", "meta"} {
		if err := os.MkdirAll(filepath.Join(c.root, sub), 0o755); err != nil {
			return fmt.Errorf("plaid-lint cache: mkdir %s: %w", sub, err)
		}
	}
	// Bumping CacheVersion is the engine's only mechanism for invalidating
	// the entire cache: a mismatch here purges typecheck/ and analyzer/
	// before stamping the new version. Absence is treated as "fresh cache".
	verPath := filepath.Join(c.root, "meta", "cache-version")
	existing, err := os.ReadFile(verPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// Fresh cache: just stamp the version.
	case err != nil:
		return fmt.Errorf("plaid-lint cache: read cache-version: %w", err)
	case bytes.Equal(bytes.TrimRight(existing, "\n"), []byte(CacheVersion)):
		return nil
	default:
		if err := c.purgeEntries(); err != nil {
			return fmt.Errorf("plaid-lint cache: purge on version bump: %w", err)
		}
		// writeFileAtomic uses link(2) with EEXIST-as-success semantics,
		// so the stale version file must be unlinked before the new stamp.
		if err := os.Remove(verPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("plaid-lint cache: remove stale cache-version: %w", err)
		}
	}
	if err := writeFileAtomic(verPath, []byte(CacheVersion+"\n"), 0o644); err != nil {
		return fmt.Errorf("plaid-lint cache: write cache-version: %w", err)
	}
	return nil
}

// purgeEntries recursively removes and recreates the typecheck/ and analyzer/
// subtrees. Called on CacheVersion mismatch.
func (c *Cache) purgeEntries() error {
	for _, sub := range []string{"typecheck", "analyzer"} {
		p := filepath.Join(c.root, sub)
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("remove %s: %w", sub, err)
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return fmt.Errorf("recreate %s: %w", sub, err)
		}
	}
	return nil
}

// l2Path returns the on-disk path for an L2 (typecheck) entry.
func (c *Cache) l2Path(id ActionID) string {
	hex := id.Hex()
	return filepath.Join(c.root, "typecheck", hex[:2], hex)
}

// l1Path returns the on-disk path for an L1 (analyzer) entry. analyzer is
// the analyzer name (e.g. "ineffassign"); it is used as a path segment so
// it must be a safe identifier — callers are expected to pass plain ASCII
// analyzer names from a registry.
func (c *Cache) l1Path(analyzer string, id ActionID) string {
	hex := id.Hex()
	return filepath.Join(c.root, "analyzer", analyzer, hex[:2], hex)
}

// Touch updates the mtime on a hit path to "now", refreshing GC eligibility.
// Best-effort; errors (e.g. missing file) are returned but not actionable.
func (c *Cache) Touch(path string) error {
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// GC walks the cache root and unlinks any file under typecheck/ or
// analyzer/ whose mtime is older than threshold. Errors on individual
// files are accumulated and returned via errors.Join.
func (c *Cache) GC(threshold time.Duration) error {
	cutoff := time.Now().Add(-threshold)
	var errs []error
	for _, sub := range []string{"typecheck", "analyzer"} {
		root := filepath.Join(c.root, sub)
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Don't bail on a missing directory — the cache may be partially populated.
				if errors.Is(walkErr, fs.ErrNotExist) {
					return nil
				}
				errs = append(errs, walkErr)
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				errs = append(errs, err)
				return nil
			}
			if info.ModTime().Before(cutoff) {
				if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
			}
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
