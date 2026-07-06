// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package l0 implements the per-(package, analyzer-set) diagnostic
// cache that sits above the engine's analysis driver.
//
// L0 is the highest tier in plaid-lint's cache stack. It is consulted
// before the gopls action graph is built; on a hit the engine returns
// the cached diagnostic stream and never calls Snapshot.Analyze for
// that package. On a miss the engine analyses the package normally
// and writes the L0 entry post-Analyze.
//
// The L0 layer mirrors golangci-lint's runners_cache.go: a coarse
// (package, analyzer-set) → []Diagnostic cache keyed off file hashes
// rather than the gopls reachability key.
//
// Storage routes through the internal/cache backend seam: bytes are
// encoded above the seam and the
// (namespace, id) → bytes traffic dispatches through whichever
// backend PLAID_CACHE_BACKEND selects (local FS or gocacheprog).
// The L0 namespace string is "l0"; on the localBackend that maps to
// <root>/l0/<shard>/<hex>.
package l0

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/output"
)

// CacheVersion is the L0 format-version tag. Bumped manually when the
// on-disk encoding or the diagnostic-emission contract changes in a way
// that invalidates previously-written L0 entries.
//
// Intentionally NOT auto-derived from the binary version: a plaid-lint
// release should not auto-invalidate L0 unless wrappers actually
// changed.
//
// v2 (Option A): Entry now carries per-analyzer Facts blobs
// alongside Diagnostics, so an L0 hit can synthesise an analyzeSummary
// that the gopls action graph short-circuits at runCached. The
// on-disk schema change requires a version bump.
//
// v3: Entry's Diagnostic stream now stores canonical
// <importPath>/<basename> filenames rather than absolute paths.
// Existing v2 entries hold host-local paths that the post-engine
// reverse-mapper would mis-resolve on a different machine; the bump
// invalidates them outright.
//
// v4: the L0 key now folds in KeyParts.FilterConfigHash (a digest of
// the effective suppression/exclusion configuration). L0 caches the
// POST-filter diagnostic stream, so entries written before this bump
// were keyed without any filter input and could replay diagnostics
// filtered under a stale suppression config. The bump invalidates them
// so the first warm run after the upgrade re-filters under the current
// config.
const CacheVersion = 4

// nsL0 is the backend-namespace string for L0 entries. See package
// docstring for the on-disk shape.
const nsL0 = "l0"

// KeyParts is the structured input to ComputeKey. Callers assemble it
// in the engine; the cache layer only stipulates the digest derivation.
//
// Every field is folded into a SHA-256 digest in a length-prefixed,
// deterministic order. A change to any input changes the digest.
type KeyParts struct {
	// PackageID is the metadata.PackageID for the workspace package.
	PackageID string

	// PackagePath is the metadata.PackagePath. Folded alongside ID so a
	// rename that preserves the path still invalidates the entry.
	PackagePath string

	// SourceHash is a digest of the package's own compiled-source files
	// (URI + content hash, sorted by URI). Mirrors golangci's
	// HashModeNeedAllDeps for the package itself.
	SourceHash [32]byte

	// DepHash is a digest of every transitive dep's (PackageID +
	// SourceHash), sorted by PackageID. Mirrors HashModeNeedAllDeps for
	// the dependency closure.
	DepHash [32]byte

	// AnalyzerSetHash is sha256 over the sorted (name, version,
	// configSalt) list of enabled analyzers.
	AnalyzerSetHash [32]byte

	// FilterConfigHash is a digest of the effective suppression /
	// exclusion configuration (exclude-rules, exclude-paths,
	// paths-except, target patterns, staticcheck-default-disabled set,
	// uniq-by-line, generated-file mode, nolint). L0 stores the
	// POST-filter diagnostic stream, so this MUST be folded in: a change
	// to suppression rules with byte-identical source would otherwise
	// replay diagnostics filtered under the stale config, silently
	// hiding findings that should now surface (or vice versa).
	FilterConfigHash [32]byte

	// ToolVersion is the engine-tool-version key (CacheToolVersion).
	ToolVersion string

	// BuildEnv folds GOOS/GOARCH/cgo so a cross-platform run doesn't
	// hit the wrong cache entry.
	BuildEnv string

	// GoVersion is the runtime Go version.
	GoVersion string
}

// ComputeKey derives the content-addressed L0 key from parts. The
// CacheVersion is folded in as the first input so a version bump
// invalidates all prior entries.
func ComputeKey(parts KeyParts) clcache.ActionID {
	var verBuf [4]byte
	binary.LittleEndian.PutUint32(verBuf[:], uint32(CacheVersion))
	return clcache.NewActionID(
		verBuf[:],
		[]byte("l0/v1"), // domain separator
		[]byte(parts.PackageID),
		[]byte(parts.PackagePath),
		parts.SourceHash[:],
		parts.DepHash[:],
		parts.AnalyzerSetHash[:],
		parts.FilterConfigHash[:],
		[]byte(parts.ToolVersion),
		[]byte(parts.BuildEnv),
		[]byte(parts.GoVersion),
	)
}

// Cache is a disk-backed cache of per-package diagnostic streams. It is
// content-addressed by ComputeKey and structurally independent of the
// L1/L2 caches (separate namespace, separate version).
//
// Cache is safe for concurrent use across goroutines and processes.
// Concurrent safety comes from the underlying backend's first-writer-
// wins contract (link(2) O_EXCL on the localBackend; the gocacheprog
// helper enforces its own equivalent).
type Cache struct {
	// root is the parent dir under which the L0 namespace lives; kept
	// for Path() reporting and for the version-stamp file. NOT used
	// for entry IO — that goes through backend.
	root string

	// backend is the (namespace, id) → bytes seam introduced in
	// Stage 1. L0's Stage 1.5 routes its Get/Put/Has
	// through it so a PLAID_CACHE_BACKEND=gocacheprog selection
	// covers L0 along with L1/L2.
	backend clcache.Backend

	metrics Metrics

	// closeOnce guards Close so it is safe to call from multiple defer
	// sites.
	closeOnce sync.Once
	closeErr  error
}

// Metrics is the counter set Cache maintains per process. Loads are
// atomic; tests compare these to assert hit/miss behaviour.
type Metrics struct {
	Hits   atomic.Int64
	Misses atomic.Int64
	Stores atomic.Int64
	Errors atomic.Int64
}

// Snapshot returns a point-in-time copy of the counters.
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		Hits:   m.Hits.Load(),
		Misses: m.Misses.Load(),
		Stores: m.Stores.Load(),
		Errors: m.Errors.Load(),
	}
}

// MetricsSnapshot is the value-type companion to Metrics.
type MetricsSnapshot struct {
	Hits, Misses, Stores, Errors int64
}

// Open opens (creating if necessary) an L0 cache rooted at <root>. If
// root is empty, clcache.DefaultRoot() is used.
//
// Open constructs a backend rooted at <root> via
// clcache.OpenBackendForTier(root, TierL0) so PLAID_L0_CACHE_BACKEND
// — falling back to PLAID_CACHE_BACKEND, then "local" — selects the
// L0 storage independently of L1/L2. The L0 namespace ("l0") places
// localBackend entries at <root>/l0/<shard>/<hex>, mirroring today's
// layout under the same parent dir.
func Open(root string) (*Cache, error) {
	if root == "" {
		r, err := clcache.DefaultRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	// Ensure the parent dir exists so the version stamp + per-write
	// backend mkdirs don't race the first writer.
	if err := os.MkdirAll(filepath.Join(root, nsL0), 0o755); err != nil {
		return nil, fmt.Errorf("plaid-lint L0: mkdir: %w", err)
	}
	// Stamp a version file so a future migration can detect mismatch.
	verPath := filepath.Join(root, nsL0, "version")
	if _, err := os.Stat(verPath); errors.Is(err, fs.ErrNotExist) {
		// writeFile is intentionally non-atomic here: it's a single-
		// writer informational stamp, not a content-addressed entry.
		_ = os.WriteFile(verPath, []byte(fmt.Sprintf("%d\n", CacheVersion)), 0o644)
	}

	b, err := clcache.OpenBackendForTier(root, clcache.TierL0)
	if err != nil {
		return nil, fmt.Errorf("plaid-lint L0: open backend: %w", err)
	}
	return &Cache{root: filepath.Join(root, nsL0), backend: b}, nil
}

// Path returns the absolute path of the L0 cache root.
func (c *Cache) Path() string { return c.root }

// Close releases any resources held by the backend. For the local
// backend this is a no-op; for the gocacheprog backend it terminates
// the helper subprocess so the parent process can exit cleanly. Safe
// to call multiple times.
func (c *Cache) Close() error {
	c.closeOnce.Do(func() {
		if closer, ok := c.backend.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// NewWithBackendForTest constructs a Cache wired to an arbitrary
// Backend. Test-only: the dispatch test injects a mock backend to
// pin the seam invariant.
func NewWithBackendForTest(b clcache.Backend) *Cache {
	return &Cache{backend: b}
}

// MetricsPtr returns a pointer to the cache's metrics counters. The
// pointer is stable for the lifetime of the cache.
func (c *Cache) MetricsPtr() *Metrics { return &c.metrics }

// Entry is the gob-encoded payload of one L0 cache entry. It carries
// the diagnostic stream the engine would otherwise produce for the
// package, plus (since v2) the per-analyzer fact blobs so that
// dep overrides can synthesise an analyzeSummary at runCached.
//
// PackageID is mirrored from the key for debug observability; it is
// NOT part of the content addressing (the key already covers it).
//
// Actions is keyed by an opaque per-analyzer identifier the engine
// chooses (the gopls cross-process stableName). Each ActionFacts entry
// carries the gob-encoded facts.Set blob plus its hash so the
// synthetic actionSummary is byte-identical to one produced by
// action.exec on the cold path. A package that produced zero
// diagnostics still gets a non-nil Actions map so dep overrides fire
// on warm runs.
//
// Compiles mirrors analyzeSummary.Compiles — true when the package
// (and its transitive deps) had no list/parse/type errors. Folded in
// so synthetic summaries can be used for both root and dep packages.
type Entry struct {
	PackageID   string
	Diagnostics []output.Diagnostic
	Actions     map[string]ActionFacts // analyzer stableName -> facts
	Compiles    bool
}

// ActionFacts is the per-analyzer payload reconstructed into a synthetic
// actionSummary on warm runs. Facts is the gob-encoded facts.Set blob;
// FactsHash is sha256(Facts). Diagnostics are NOT duplicated here — the
// per-package output.Diagnostic stream on Entry already carries them
// (and is post-filter, which the per-action gobDiagnostic stream is
// not). Err mirrors actionSummary.Err so a synthetic summary for a
// failed action stays observable.
type ActionFacts struct {
	Facts     []byte
	FactsHash [32]byte
	Err       string
}

// encodeEntry deterministically gob-encodes e for backend storage.
// Above-seam pure transform: the backend sees opaque bytes.
func encodeEntry(e *Entry) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeEntry is the inverse of encodeEntry.
func decodeEntry(data []byte) (*Entry, error) {
	var e Entry
	dec := gob.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&e); err != nil {
		return nil, err
	}
	return &e, nil
}

// Get reads and decodes the L0 entry for id. Returns an error wrapping
// fs.ErrNotExist on miss. Bumps Misses on a missing entry, Errors on a
// decode failure, and Hits on a successful decode.
func (c *Cache) Get(id clcache.ActionID) (*Entry, error) {
	data, err := c.backend.Get(nsL0, id)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			c.metrics.Misses.Add(1)
		} else {
			c.metrics.Errors.Add(1)
		}
		return nil, err
	}
	e, err := decodeEntry(data)
	if err != nil {
		c.metrics.Errors.Add(1)
		return nil, fmt.Errorf("L0 decode %s: %w", id.Hex(), err)
	}
	c.metrics.Hits.Add(1)
	return e, nil
}

// Put writes e to the L0 cache under id. The write is atomic
// (first-writer-wins via the backend's own contract).
func (c *Cache) Put(id clcache.ActionID, e *Entry) error {
	if e == nil {
		return errors.New("L0 Put: nil entry")
	}
	body, err := encodeEntry(e)
	if err != nil {
		c.metrics.Errors.Add(1)
		return fmt.Errorf("L0 encode %s: %w", id.Hex(), err)
	}
	if err := c.backend.Put(nsL0, id, body); err != nil {
		c.metrics.Errors.Add(1)
		return err
	}
	c.metrics.Stores.Add(1)
	return nil
}

// HashFiles computes a deterministic sha256 over a sorted (uri, hash)
// list. uris and hashes must have the same length.
func HashFiles(uris []string, hashes [][32]byte) [32]byte {
	if len(uris) != len(hashes) {
		return [32]byte{}
	}
	type pair struct {
		uri  string
		hash [32]byte
	}
	pairs := make([]pair, len(uris))
	for i := range uris {
		pairs[i] = pair{uri: uris[i], hash: hashes[i]}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].uri < pairs[j].uri })

	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range pairs {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p.uri)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(p.uri))
		_, _ = h.Write(p.hash[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// HashAnalyzerSet returns the deterministic sha256 over a list of
// (name, version, configSalt) triples, sorted by name.
func HashAnalyzerSet(names []string, versions []string, configSalts [][32]byte) [32]byte {
	if len(names) != len(versions) || len(names) != len(configSalts) {
		return [32]byte{}
	}
	type row struct {
		name    string
		version string
		salt    [32]byte
	}
	rows := make([]row, len(names))
	for i := range names {
		rows[i] = row{name: names[i], version: versions[i], salt: configSalts[i]}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })

	h := sha256.New()
	var lenBuf [8]byte
	for _, r := range rows {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(r.name)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(r.name))
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(r.version)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(r.version))
		_, _ = h.Write(r.salt[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// HashDepClosure returns the deterministic sha256 over the dep closure's
// (PackageID, SourceHash) pairs, sorted by PackageID.
func HashDepClosure(depIDs []string, depSourceHashes [][32]byte) [32]byte {
	if len(depIDs) != len(depSourceHashes) {
		return [32]byte{}
	}
	type pair struct {
		id   string
		hash [32]byte
	}
	pairs := make([]pair, len(depIDs))
	for i := range depIDs {
		pairs[i] = pair{id: depIDs[i], hash: depSourceHashes[i]}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].id < pairs[j].id })

	h := sha256.New()
	var lenBuf [8]byte
	for _, p := range pairs {
		binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(p.id)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write([]byte(p.id))
		_, _ = h.Write(p.hash[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
