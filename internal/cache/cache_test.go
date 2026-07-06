package cache

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Await the GC goroutine before t.TempDir cleanup; the goroutine
	// stamps .last-gc on completion and racing that with RemoveAll
	// breaks the parent-dir unlink.
	t.Cleanup(c.WaitForGC)
	return c
}

func sampleL1() *L1Entry {
	return &L1Entry{
		Analyzer:        "ineffassign",
		PackageID:       "github.com/example/foo",
		InputDigest:     fillByte(1),
		DepFactsDigest:  fillByte(2),
		DepTypeDigest:   fillByte(3),
		AnalyzerVersion: "v0.0.1",
		ConfigSalt:      fillByte(4),
		ToolVersion:     "plaid-lint-0.1",
		Diagnostics: []json.RawMessage{
			json.RawMessage(`{"category":"ineffassign","message":"x","pos":42}`),
			json.RawMessage(`{"pos":7,"message":"y","category":"ineffassign"}`),
		},
		ObjectFacts:  []byte{0xde, 0xad, 0xbe, 0xef},
		PackageFacts: []byte{0xca, 0xfe},
	}
}

func sampleL2() *L2Entry {
	return &L2Entry{
		PackageID:     "github.com/example/foo",
		GoVersion:     "go1.26",
		BuildEnv:      "linux/arm64/cgo0",
		InputDigest:   fillByte(5),
		DepTypeDigest: fillByte(6),
		ToolVersion:   "plaid-lint-0.1",
		ExportData:    []byte("EXPORT-BLOB-PLACEHOLDER"),
		FactsBlob:     []byte("FACTS-BLOB-PLACEHOLDER"),
	}
}

func fillByte(b byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = b
	}
	return out
}

func TestL1RoundTrip(t *testing.T) {
	c := newTestCache(t)
	e := sampleL1()
	id := NewActionID([]byte(e.Analyzer), []byte(e.PackageID), e.InputDigest[:])

	if err := c.WriteL1(e, id); err != nil {
		t.Fatalf("WriteL1: %v", err)
	}
	got, err := c.ReadL1(e.Analyzer, id)
	if err != nil {
		t.Fatalf("ReadL1: %v", err)
	}
	if got.Analyzer != e.Analyzer || got.PackageID != e.PackageID {
		t.Errorf("identity mismatch: got %+v want %+v", got, e)
	}
	if got.InputDigest != e.InputDigest {
		t.Errorf("InputDigest mismatch")
	}
	if !bytes.Equal(got.ObjectFacts, e.ObjectFacts) {
		t.Errorf("ObjectFacts mismatch: got %x want %x", got.ObjectFacts, e.ObjectFacts)
	}
	if !bytes.Equal(got.PackageFacts, e.PackageFacts) {
		t.Errorf("PackageFacts mismatch")
	}
	if len(got.Diagnostics) != len(e.Diagnostics) {
		t.Fatalf("diag count: got %d want %d", len(got.Diagnostics), len(e.Diagnostics))
	}
}

func TestL2RoundTrip(t *testing.T) {
	c := newTestCache(t)
	e := sampleL2()
	id := NewActionID([]byte(e.PackageID), e.InputDigest[:])

	if err := c.WriteL2(e, id); err != nil {
		t.Fatalf("WriteL2: %v", err)
	}
	got, err := c.ReadL2(id)
	if err != nil {
		t.Fatalf("ReadL2: %v", err)
	}
	if got.PackageID != e.PackageID || got.GoVersion != e.GoVersion {
		t.Errorf("identity mismatch")
	}
	if !bytes.Equal(got.ExportData, e.ExportData) {
		t.Errorf("ExportData mismatch")
	}
	if !bytes.Equal(got.FactsBlob, e.FactsBlob) {
		t.Errorf("FactsBlob mismatch")
	}
}

func TestL1MissReturnsErrNotExist(t *testing.T) {
	c := newTestCache(t)
	id := NewActionID([]byte("nope"))
	_, err := c.ReadL1("nope-analyzer", id)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}

func TestL2MissReturnsErrNotExist(t *testing.T) {
	c := newTestCache(t)
	id := NewActionID([]byte("nope"))
	_, err := c.ReadL2(id)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want fs.ErrNotExist, got %v", err)
	}
}

func TestActionIDStability(t *testing.T) {
	a := NewActionID([]byte("hello"), []byte("world"))
	b := NewActionID([]byte("hello"), []byte("world"))
	if a != b {
		t.Errorf("same inputs produced different IDs: %x vs %x", a, b)
	}
	// Length-prefixed: ("ab","cd") != ("abc","d") != ("abcd").
	id1 := NewActionID([]byte("ab"), []byte("cd"))
	id2 := NewActionID([]byte("abc"), []byte("d"))
	id3 := NewActionID([]byte("abcd"))
	if id1 == id2 || id1 == id3 || id2 == id3 {
		t.Errorf("ambiguous concatenation: id1=%x id2=%x id3=%x", id1, id2, id3)
	}
}

func TestShardPath(t *testing.T) {
	id := NewActionID([]byte("x"))
	p := ShardPath(id)
	hex := id.Hex()
	if p != hex[:2]+"/"+hex {
		t.Errorf("shard path mismatch: %q vs %q", p, hex[:2]+"/"+hex)
	}
}

// TestSameActionInvariant is the same-action fuzz check: two writers with the
// same inputs MUST land identical bytes on disk. If gob/JSON map iteration
// non-determinism leaks in, this fails — and the link(2) O_EXCL primitive
// still functions, but the content-addressed invariant degrades.
func TestSameActionInvariant(t *testing.T) {
	c := newTestCache(t)

	// L1: build the same entry twice, encode each, compare bytes.
	e1 := sampleL1()
	e2 := sampleL1()
	b1, err := e1.Encode()
	if err != nil {
		t.Fatalf("encode e1: %v", err)
	}
	b2, err := e2.Encode()
	if err != nil {
		t.Fatalf("encode e2: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Errorf("L1 same-action bytes diverge: len1=%d len2=%d", len(b1), len(b2))
	}

	// L2: same.
	l2a := sampleL2()
	l2b := sampleL2()
	bl2a, _ := l2a.Encode()
	bl2b, _ := l2b.Encode()
	if !bytes.Equal(bl2a, bl2b) {
		t.Errorf("L2 same-action bytes diverge")
	}

	// Also exercise the file-system side: two WriteL1 calls with the same id
	// from the same content must converge on a single inode with consistent
	// bytes.
	id := NewActionID([]byte(e1.Analyzer), []byte(e1.PackageID))
	if err := c.WriteL1(e1, id); err != nil {
		t.Fatalf("WriteL1 #1: %v", err)
	}
	if err := c.WriteL1(e2, id); err != nil {
		t.Fatalf("WriteL1 #2: %v", err)
	}
	got, err := c.ReadL1(e1.Analyzer, id)
	if err != nil {
		t.Fatalf("ReadL1: %v", err)
	}
	if got.PackageID != e1.PackageID {
		t.Errorf("post-double-write read returned wrong identity")
	}
}

// TestConcurrentWriteFirstWinsNoTear launches N goroutines writing the same
// content-addressed name, then verifies a single canonical byte sequence is
// readable afterward and no tmp files leaked.
func TestConcurrentWriteFirstWinsNoTear(t *testing.T) {
	const N = 8
	c := newTestCache(t)
	e := sampleL1()
	id := NewActionID([]byte("concurrent-test"))

	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.WriteL1(e, id); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent WriteL1 failure: %v", err)
	}

	got, err := c.ReadL1(e.Analyzer, id)
	if err != nil {
		t.Fatalf("ReadL1 after concurrent: %v", err)
	}
	if got.PackageID != e.PackageID {
		t.Errorf("post-concurrent identity drift")
	}

	// Verify no .tmp.* files leaked under the analyzer subtree.
	root := filepath.Join(c.Path(), "analyzer")
	leaks := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(p), ".tmp.") {
			leaks++
			t.Errorf("leaked tmp file: %s", p)
		}
		return nil
	})
	if leaks != 0 {
		t.Errorf("%d tmp file(s) leaked", leaks)
	}
}

func TestGCPrunesOldEntries(t *testing.T) {
	c := newTestCache(t)
	e := sampleL1()
	idFresh := NewActionID([]byte("fresh"))
	idStale := NewActionID([]byte("stale"))

	if err := c.WriteL1(e, idFresh); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteL1(e, idStale); err != nil {
		t.Fatal(err)
	}

	// Backdate the stale entry's mtime to 60 days ago.
	stalePath := c.l1Path(e.Analyzer, idStale)
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if err := c.GC(30 * 24 * time.Hour); err != nil {
		t.Fatalf("GC: %v", err)
	}

	if _, err := os.Stat(stalePath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale entry survived GC: %v", err)
	}
	if _, err := os.Stat(c.l1Path(e.Analyzer, idFresh)); err != nil {
		t.Errorf("fresh entry pruned by GC: %v", err)
	}
}

func TestGCOnOpen(t *testing.T) {
	// .last-gc gate would otherwise skip the re-Open's GC; force it.
	t.Setenv("PLAID_FORCE_GC", "1")
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := sampleL1()
	id := NewActionID([]byte("k"))
	if err := c.WriteL1(e, id); err != nil {
		t.Fatal(err)
	}
	// Backdate.
	p := c.l1Path(e.Analyzer, id)
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	// Re-open: GC runs with the default 30d threshold. GC is async
	// (see Cache.Open godoc); wait for it explicitly so the assertion
	// below isn't racing the background goroutine.
	c2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c2.WaitForGC()
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open did not GC stale entry: %v", err)
	}
}

// TestOpenGCIsAsync verifies that Open returns before the GC pass
// completes — the GC walk is a `filepath.WalkDir` that can take seconds
// on a populated cache, so it runs in a background goroutine.
// The test fills a directory with enough entries to make the walk
// observably non-instant, then asserts Open returns while at least one
// entry has yet to be visited (i.e. the stale entry survives until
// WaitForGC).
func TestOpenGCIsAsync(t *testing.T) {
	// .last-gc gate would otherwise skip the second Open's GC.
	t.Setenv("PLAID_FORCE_GC", "1")
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c.WaitForGC()

	// Seed enough entries to make the GC walk slow enough that we can
	// observe Open returning before it completes. 200 backdated files is
	// enough for the WalkDir + Stat + Remove loop to take >1ms.
	e := sampleL1()
	old := time.Now().Add(-60 * 24 * time.Hour)
	var paths []string
	for i := 0; i < 200; i++ {
		id := NewActionID([]byte("async-gc"), []byte{byte(i), byte(i >> 8)})
		if err := c.WriteL1(e, id); err != nil {
			t.Fatalf("WriteL1: %v", err)
		}
		p := c.l1Path(e.Analyzer, id)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
		paths = append(paths, p)
	}

	// Re-open: the new Cache kicks off async GC and returns immediately.
	// Survivors after Open() returns but before WaitForGC() implies the
	// goroutine hasn't finished — proves async.
	c2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// At the moment Open returns, some entries should still exist. This is
	// a race-tolerant assertion: a fast machine can complete GC before we
	// stat, but on the CI/dev fleet 200 entries gives a wide margin. If
	// EVERY entry is already gone, GC was effectively synchronous and the
	// async fix is not actually deferring work.
	survivors := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			survivors++
		}
	}
	if survivors == 0 {
		t.Errorf("Open's GC ran synchronously: 0/%d stale entries survived to the moment Open returned", len(paths))
	}

	// After WaitForGC, every stale entry must be pruned — correctness
	// invariant: async GC still does its job, just off the critical path.
	c2.WaitForGC()
	for _, p := range paths {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("entry survived async GC after WaitForGC: %s", p)
			break
		}
	}
}

func TestTouchRefreshesMtime(t *testing.T) {
	c := newTestCache(t)
	e := sampleL1()
	id := NewActionID([]byte("touch"))
	if err := c.WriteL1(e, id); err != nil {
		t.Fatal(err)
	}
	p := c.l1Path(e.Analyzer, id)
	old := time.Now().Add(-10 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if err := c.Touch(p); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	info, _ := os.Stat(p)
	if time.Since(info.ModTime()) > 1*time.Minute {
		t.Errorf("Touch did not refresh mtime (still %v old)", time.Since(info.ModTime()))
	}
}

func TestCanonicalJSONSortsKeys(t *testing.T) {
	// Two diagnostics with the same content but different key order should
	// produce identical canonical bytes.
	a := json.RawMessage(`{"b":2,"a":1}`)
	b := json.RawMessage(`{"a":1,"b":2}`)
	ca, err := canonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := canonicalJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca, cb) {
		t.Errorf("canonicalJSON not deterministic: %q vs %q", ca, cb)
	}
}

func TestEnvelopeKindMismatch(t *testing.T) {
	e := sampleL1()
	b, err := e.Encode()
	if err != nil {
		t.Fatal(err)
	}
	// Decode L1 bytes as L2 — must fail with kind mismatch.
	if _, err := DecodeL2(b); err == nil {
		t.Errorf("expected kind mismatch error decoding L1 as L2")
	}
}

func TestParseActionIDRoundTrip(t *testing.T) {
	id := NewActionID([]byte("xyz"))
	parsed, ok := ParseActionID(id.Hex())
	if !ok {
		t.Fatal("parse failed on valid hex")
	}
	if parsed != id {
		t.Errorf("parsed != original")
	}
	if _, ok := ParseActionID("not-hex"); ok {
		t.Errorf("parse claimed success on garbage")
	}
}

func TestDefaultRootHonorsXDG(t *testing.T) {
	t.Setenv("PLAID_CACHE_DIR", "")
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	t.Setenv("XDG_CACHE_HOME", "/some/where")
	r, err := DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	if r != "/some/where/plaid-lint" {
		t.Errorf("DefaultRoot: %q", r)
	}
}

func TestDefaultRootPrecedence(t *testing.T) {
	// Pin every step's input so the precedence order is unambiguous, then
	// shed steps one-by-one (highest-priority first) to confirm each
	// fallback fires when its predecessor is empty.
	const (
		explicit = "/explicit/path"
		gciCache = "/gci/path"
		xdg      = "/xdg/path"
	)
	// 1. PLAID_CACHE_DIR wins outright (raw path, no suffix).
	t.Setenv("PLAID_CACHE_DIR", explicit)
	t.Setenv("GOLANGCI_LINT_CACHE", gciCache)
	t.Setenv("XDG_CACHE_HOME", xdg)
	if r, _ := DefaultRoot(); r != explicit {
		t.Errorf("PLAID_CACHE_DIR did not win: got %q", r)
	}
	// 2. GOLANGCI_LINT_CACHE wins when PLAID_CACHE_DIR is empty (raw path).
	t.Setenv("PLAID_CACHE_DIR", "")
	if r, _ := DefaultRoot(); r != gciCache {
		t.Errorf("GOLANGCI_LINT_CACHE did not win: got %q", r)
	}
	// 3. XDG_CACHE_HOME wins next (suffixed).
	t.Setenv("GOLANGCI_LINT_CACHE", "")
	if r, _ := DefaultRoot(); r != "/xdg/path/plaid-lint" {
		t.Errorf("XDG_CACHE_HOME did not win: got %q", r)
	}
}

// TestCacheVersionMismatchPurges verifies that Open() detects a mismatched
// meta/cache-version and purges typecheck/ and analyzer/ before stamping
// the current version. Bumping CacheVersion is the only mechanism the
// engine has to invalidate the whole cache, so silent reuse is unsafe.
func TestCacheVersionMismatchPurges(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Populate an L1 entry and an L2 entry.
	e1 := sampleL1()
	id1 := NewActionID([]byte("v-bump-l1"))
	if err := c.WriteL1(e1, id1); err != nil {
		t.Fatalf("WriteL1: %v", err)
	}
	e2 := sampleL2()
	id2 := NewActionID([]byte("v-bump-l2"))
	if err := c.WriteL2(e2, id2); err != nil {
		t.Fatalf("WriteL2: %v", err)
	}

	// Forge an older version on disk.
	verPath := filepath.Join(dir, "meta", "cache-version")
	if err := os.WriteFile(verPath, []byte("0\n"), 0o644); err != nil {
		t.Fatalf("forge cache-version: %v", err)
	}

	// Re-open: mismatch must purge typecheck/ and analyzer/.
	if _, err := Open(dir); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	// Subdirs must still exist but be empty.
	for _, sub := range []string{"typecheck", "analyzer"} {
		p := filepath.Join(dir, sub)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s missing after purge: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
			continue
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			t.Errorf("ReadDir %s: %v", sub, err)
			continue
		}
		if len(ents) != 0 {
			t.Errorf("%s not empty after version-bump purge: %v", sub, ents)
		}
	}

	// And the version file must now reflect CacheVersion.
	got, err := os.ReadFile(verPath)
	if err != nil {
		t.Fatalf("read cache-version: %v", err)
	}
	if strings.TrimRight(string(got), "\n") != CacheVersion {
		t.Errorf("cache-version after re-Open = %q, want %q", got, CacheVersion)
	}
}

// TestCacheVersionMatchPreservesEntries is the matched-version no-op case:
// reopening a cache whose stamped version matches CacheVersion must NOT
// touch existing entries.
func TestCacheVersionMatchPreservesEntries(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	e := sampleL1()
	id := NewActionID([]byte("v-match"))
	if err := c.WriteL1(e, id); err != nil {
		t.Fatalf("WriteL1: %v", err)
	}
	p := c.l1Path(e.Analyzer, id)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("entry missing before re-Open: %v", err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("entry pruned by same-version Open: %v", err)
	}
}

// TestWriteL1RejectsBadAnalyzerNames is the path-traversal guard. The
// analyzer name is used as a single directory component under analyzer/,
// so traversal patterns and control bytes must be refused at the boundary.
func TestWriteL1RejectsBadAnalyzerNames(t *testing.T) {
	bad := []string{
		"",
		"..",
		".",
		"../bad",
		"good/bad",
		"a/b",
		"a\\b",
		"\x00bad",
		"bad\x01",
		"bad\x7f",
		" leading",
		"trailing ",
		"with:colon",
		"a/../b",
	}
	c := newTestCache(t)
	e := sampleL1()
	id := NewActionID([]byte("traversal"))
	for _, name := range bad {
		entry := *e
		entry.Analyzer = name
		err := c.WriteL1(&entry, id)
		if err == nil {
			t.Errorf("WriteL1(%q): want error, got nil", name)
			continue
		}
		var ian *ErrInvalidAnalyzerName
		if !errors.As(err, &ian) {
			t.Errorf("WriteL1(%q): want *ErrInvalidAnalyzerName, got %T: %v", name, err, err)
		}
		// ReadL1 must reject identically.
		if _, rerr := c.ReadL1(name, id); rerr == nil {
			t.Errorf("ReadL1(%q): want error, got nil", name)
		}
	}
}

// TestWriteL1AcceptsGoodAnalyzerNames is the positive control for
// validateAnalyzerName: real analyzer names from the upstream registry
// (with dots, dashes, and underscores) must round-trip cleanly.
func TestWriteL1AcceptsGoodAnalyzerNames(t *testing.T) {
	good := []string{
		"errcheck",
		"staticcheck",
		"go-vet",
		"my_analyzer",
		"analyzer.v2",
		"a",
	}
	c := newTestCache(t)
	for _, name := range good {
		e := sampleL1()
		e.Analyzer = name
		id := NewActionID([]byte("good"), []byte(name))
		if err := c.WriteL1(e, id); err != nil {
			t.Errorf("WriteL1(%q): unexpected error: %v", name, err)
			continue
		}
		if _, err := c.ReadL1(name, id); err != nil {
			t.Errorf("ReadL1(%q): unexpected error: %v", name, err)
		}
	}
}

// TestCacheGC_SkippedWhenRecent verifies the .last-gc gate: if the
// marker is fresh, Open must not launch the background GC goroutine.
// Asserted via gcLaunched=false plus the survival of a stale entry.
func TestCacheGC_SkippedWhenRecent(t *testing.T) {
	// Clear FORCE/DISABLE so the gate is the only thing deciding.
	t.Setenv("PLAID_FORCE_GC", "")
	t.Setenv("PLAID_DISABLE_GC", "")
	dir := t.TempDir()
	// Pre-populate .last-gc with now() so the gate refuses GC.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, ".last-gc")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatalf("seed .last-gc: %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(marker, now, now); err != nil {
		t.Fatalf("Chtimes .last-gc: %v", err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if c.GCWasLaunched() {
		t.Errorf("GC launched despite fresh .last-gc; expected skip")
	}
	// Drop a stale entry to belt-and-suspenders the assertion.
	e := sampleL1()
	id := NewActionID([]byte("recent-skip"))
	if err := c.WriteL1(e, id); err != nil {
		t.Fatal(err)
	}
	p := c.l1Path(e.Analyzer, id)
	old := time.Now().Add(-60 * 24 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	// Re-Open: still recent, still skipped, stale entry survives.
	c2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c2.GCWasLaunched() {
		t.Errorf("GC launched on second Open despite fresh .last-gc")
	}
	c2.WaitForGC() // no-op since goroutine never started
	if _, err := os.Stat(p); err != nil {
		t.Errorf("stale entry pruned by a GC that should have been skipped: %v", err)
	}
}

// TestCacheGC_RunsWhenStale verifies the gate opens when .last-gc is
// older than GCInterval, and that the goroutine re-stamps the marker
// on completion.
func TestCacheGC_RunsWhenStale(t *testing.T) {
	t.Setenv("PLAID_FORCE_GC", "")
	t.Setenv("PLAID_DISABLE_GC", "")
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, ".last-gc")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	staleAgo := time.Now().Add(-24 * time.Hour) // > GCInterval (6h)
	if err := os.Chtimes(marker, staleAgo, staleAgo); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.GCWasLaunched() {
		t.Errorf("GC not launched despite stale .last-gc (mtime 24h ago > %v interval)", GCInterval)
	}
	c.WaitForGC()
	// Marker mtime must have advanced.
	info, err := os.Stat(marker)
	if err != nil {
		t.Fatalf("Stat .last-gc after GC: %v", err)
	}
	if time.Since(info.ModTime()) > 1*time.Minute {
		t.Errorf(".last-gc not stamped post-GC: mtime is %v old", time.Since(info.ModTime()))
	}
}

// TestCacheGC_RunsOnFirstOpen verifies a brand-new cache (no .last-gc)
// runs GC.
func TestCacheGC_RunsOnFirstOpen(t *testing.T) {
	t.Setenv("PLAID_FORCE_GC", "")
	t.Setenv("PLAID_DISABLE_GC", "")
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.GCWasLaunched() {
		t.Errorf("GC not launched on first Open of fresh cache dir")
	}
	c.WaitForGC()
	// Marker must now exist with a recent mtime.
	info, err := os.Stat(filepath.Join(dir, ".last-gc"))
	if err != nil {
		t.Fatalf(".last-gc missing after first-Open GC: %v", err)
	}
	if time.Since(info.ModTime()) > 1*time.Minute {
		t.Errorf(".last-gc has unexpectedly old mtime: %v", time.Since(info.ModTime()))
	}
}

// TestCacheGC_ForceGCEnv verifies PLAID_FORCE_GC=1 bypasses the gate.
func TestCacheGC_ForceGCEnv(t *testing.T) {
	t.Setenv("PLAID_DISABLE_GC", "")
	t.Setenv("PLAID_FORCE_GC", "1")
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Stamp a fresh .last-gc so the unforced gate WOULD skip.
	marker := filepath.Join(dir, ".last-gc")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(marker, now, now); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !c.GCWasLaunched() {
		t.Errorf("PLAID_FORCE_GC=1 did not force the GC pass")
	}
	c.WaitForGC()
}

// TestOpen_BackendSelection exercises Open's switch on
// PLAID_CACHE_BACKEND: default → local, gocacheprog without
// GOCACHEPROG → error, unknown value → error, gocacheprog with a
// stub script doing the handshake → success.
func TestOpen_BackendSelection(t *testing.T) {
	// Avoid GC noise.
	t.Setenv("PLAID_DISABLE_GC", "1")

	// Default: empty env → localBackend.
	t.Setenv("PLAID_CACHE_BACKEND", "")
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("default Open: %v", err)
	}
	if _, ok := c.backend.(*localBackend); !ok {
		t.Errorf("default backend type = %T, want *localBackend", c.backend)
	}

	// Explicit "local" → localBackend.
	t.Setenv("PLAID_CACHE_BACKEND", "local")
	c, err = Open(t.TempDir())
	if err != nil {
		t.Fatalf("local Open: %v", err)
	}
	if _, ok := c.backend.(*localBackend); !ok {
		t.Errorf("local backend type = %T", c.backend)
	}

	// gocacheprog without GOCACHEPROG → error.
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", "")
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatalf("gocacheprog without GOCACHEPROG: want error, got nil")
	} else if !strings.Contains(err.Error(), "GOCACHEPROG") {
		t.Errorf("error should mention GOCACHEPROG: %v", err)
	}

	// Unknown backend → error.
	t.Setenv("PLAID_CACHE_BACKEND", "s3-direct")
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatalf("unknown backend: want error, got nil")
	}

	// gocacheprog with a stub script doing the handshake → success.
	stub := writeStubHelper(t)
	t.Setenv("PLAID_CACHE_BACKEND", "gocacheprog")
	t.Setenv("GOCACHEPROG", stub)
	c, err = Open(t.TempDir())
	if err != nil {
		t.Fatalf("gocacheprog stub Open: %v", err)
	}
	if _, ok := c.backend.(*gocacheprogBackend); !ok {
		t.Errorf("gocacheprog backend type = %T", c.backend)
	}
	// Tear down the helper so the test goroutine reaps cleanly.
	if gb, ok := c.backend.(*gocacheprogBackend); ok {
		_ = gb.Close()
	}
}

// writeStubHelper drops a /bin/sh script that emits the capability
// handshake and exits on EOF. Returns its absolute path.
func writeStubHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "stub.sh")
	// Print the handshake, then read until stdin closes.
	body := "#!/bin/sh\n" +
		"printf '%s\\n' '{\"ID\":0,\"KnownCommands\":[\"get\",\"put\",\"close\"]}'\n" +
		"while IFS= read -r _; do :; done\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// TestCacheGC_DisableGCEnv verifies PLAID_DISABLE_GC=1 skips GC
// even when the gate would otherwise allow (and even when FORCE_GC
// is also set — DISABLE wins).
func TestCacheGC_DisableGCEnv(t *testing.T) {
	t.Setenv("PLAID_FORCE_GC", "1") // would normally force
	t.Setenv("PLAID_DISABLE_GC", "1")
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.GCWasLaunched() {
		t.Errorf("PLAID_DISABLE_GC=1 did not suppress GC (even with FORCE_GC set)")
	}
}
