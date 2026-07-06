// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package harness is the shared test-only helper for the W11
// internal/test/{golden,safety,parallel,l3} suites. It centralises
// the common Snapshot.Analyze drive logic and the on-disk L1
// observation primitives so the four suites don't each carry their
// own copy of the gopls/cache wiring.
//
// This package is non-test (no _test.go suffix) so it can be imported
// by the test packages under internal/test/. It does NOT depend on
// internal/pipelinetest — that one is also test-only and would induce
// a non-test → test import cycle.
//
// W11 scope: golden + safety + parallel + L3 streaming.
package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache/metadata"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// LeakyTempDir is the GOPLSCACHE-friendly t.TempDir replacement
// duplicated from internal/pipelinetest. The gopls filecache and
// parseCache GC goroutines hold open files past test return; the
// standard t.TempDir cleanup hook races against them and emits
// sporadic "directory not empty" errors. We allocate a tmpdir and
// register a best-effort RemoveAll on cleanup; the OS reaps any
// straggler on the next tmpfs sweep.
func LeakyTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatalf("MkdirTemp(%q): %v", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// CanonicalDiag is the stable subset of cache.Diagnostic the W11
// suites compare on. Positions are normalised to base-filename plus
// line/column so different FileSets that span the same on-disk layout
// compare equal across runs.
//
// The fields are deliberately a verbatim copy of pipelinetest's
// canonicalDiag (the W6 contract) so cross-suite diagnostic equality
// is byte-identical.
type CanonicalDiag struct {
	Source   string `json:"source"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Filename string `json:"filename"`
	Line     uint32 `json:"line"`
	Column   uint32 `json:"column"`
}

// canonicalize converts a cache.Diagnostic to a CanonicalDiag. The
// helper is package-private; callers consume Analyze results through
// AnalyzeOnce, which returns the canonical map already.
func canonicalize(d *cache.Diagnostic) CanonicalDiag {
	return CanonicalDiag{
		Source:   string(d.Source),
		Code:     d.Code,
		Message:  d.Message,
		Filename: filepath.Base(d.URI.Path()),
		Line:     d.Range.Start.Line,
		Column:   d.Range.Start.Character,
	}
}

// SortDiags is the in-place sort used by every caller before
// constructing a digest. Exported so test packages that build their
// own canonical slices can match the wire format.
func SortDiags(d []CanonicalDiag) {
	sort.Slice(d, func(i, j int) bool {
		if d[i].Source != d[j].Source {
			return d[i].Source < d[j].Source
		}
		if d[i].Filename != d[j].Filename {
			return d[i].Filename < d[j].Filename
		}
		if d[i].Line != d[j].Line {
			return d[i].Line < d[j].Line
		}
		if d[i].Column != d[j].Column {
			return d[i].Column < d[j].Column
		}
		return d[i].Message < d[j].Message
	})
}

// Digest returns a deterministic sha256 hex over the per-analyzer map
// of canonical diagnostics. Two runs whose diagnostic streams sort to
// the same byte-form produce equal digests.
//
// Why a digest and not the raw map: the parallel-safety test (W11
// 1.41) needs to compare 8 invocations' outputs cheaply; digests are
// O(1) to compare and trivially serialisable for log lines.
func Digest(by map[string][]CanonicalDiag) string {
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	type pair struct {
		Analyzer string          `json:"analyzer"`
		Diags    []CanonicalDiag `json:"diags"`
	}
	out := make([]pair, 0, len(names))
	for _, n := range names {
		out = append(out, pair{Analyzer: n, Diags: by[n]})
	}
	b, _ := json.Marshal(out)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// InstallAnalyzers replaces settings.AllAnalyzers with the given set
// for the duration of the test and registers a cleanup to restore the
// previous value. Mirrors pipelinetest's installPipelineAnalyzers.
//
// Tests that want a tiny stable analyzer set pass it explicitly; tests
// that want the full W7+W8 102-analyzer workload pass the result of
// analyzers.AllPhase1RootAnalyzers().
func InstallAnalyzers(t *testing.T, set []*analysis.Analyzer) {
	t.Helper()
	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = nil
	for _, a := range set {
		settings.AllAnalyzers = append(settings.AllAnalyzers, settings.NewAnalyzer(a))
	}
}

// SmallW7Set returns the 8-analyzer W7 root set. Used by the parallel
// and L3 streaming tests where the 102-analyzer set is overkill and
// just inflates the run time of each invocation.
//
// This counter assertion (len = 8) is the W7 root set the W10
// regression test pinned. If the W7 root set grows, this length will
// climb in lockstep with that test's pin.
func SmallW7Set() []*analysis.Analyzer {
	return analyzers.AllBundledAnalyzers()
}

// FullPhase1Set returns the 102-analyzer W7+W8 workload. Used by the
// L3 streaming test (the load-bearing 1.5 GB ceiling targets the
// production workload, not a synthetic subset) and any golden test
// whose fixture wants production-shape coverage.
//
// This counter assertion (len = 102) is the W10 pin: 7 non-SA W7
// roots + 95 SA-* checks (SA1000 deduped). Reading this set when it
// reports a different count is the bug to watch for.
func FullPhase1Set() []*analysis.Analyzer {
	return analyzers.AllPhase1RootAnalyzers()
}

// Config is the per-invocation knob set the test packages thread
// through AnalyzeOnce. Zero-value Config means: fresh L1 + L2 + IR
// manager per call, default analyzer set unchanged from whatever the
// caller installed.
type Config struct {
	// ModuleRoot is the absolute path of the module to analyze.
	// Required.
	ModuleRoot string

	// L1Dir / L2Dir are absolute paths to the L1 / L2 cache roots.
	// When both are empty AnalyzeOnce constructs fresh tempdirs (so
	// the run is cold). Pass stable paths across invocations to
	// exercise the warm hit path.
	L1Dir string
	L2Dir string

	// GoplsCacheDir is the GOPLSCACHE env var the call will set. Empty
	// means "use the existing GOPLSCACHE", which is fine when the
	// caller has already set one — most callers do via t.Setenv on a
	// LeakyTempDir.
	GoplsCacheDir string

	// ToolVersion is the cache tool-version key folded into L1/L2
	// action IDs. Empty selects "plaid-lint-w11".
	ToolVersion string

	// L2BuildEnv / L2GoVersion mirror cache.Cache.AttachL2 inputs.
	// Empty selects "linux/arm64/cgo0" / "go1.22" — the same defaults
	// the bench harness uses for synthetic fixtures.
	L2BuildEnv  string
	L2GoVersion string

	// InvalidatePaths is the absolute-path list passed to
	// WorkspaceState.Invalidate before the Analyze call. Empty means
	// no Invalidate is issued — useful for cold and clean-warm runs.
	InvalidatePaths []string

	// AttachIRManager controls whether the call wires a fresh
	// l3.SequentialIRManager onto the cache. When true, the returned
	// Result.IRManager is non-nil and the test can assert pin-state
	// invariants. When false, no IRManager is attached (the W7
	// baseline path).
	AttachIRManager bool

	// CacheMutate is an optional callback invoked after the *cache.Cache
	// has had its L1 / L2 attached but before workspace.NewWithCache is
	// called. Tests use this to install a custom registry, attach a
	// scheduler, or any other post-Attach setup that must happen
	// before the View is created.
	CacheMutate func(*cache.Cache)
}

// Result is the bundle AnalyzeOnce returns to its caller. Counters
// are absolute on the run's own cache — there is no "previous
// scenario" delta to subtract, because every AnalyzeOnce call opens
// fresh *cache.Cache, *cache.Snapshot, and (when requested) a fresh
// IRManager.
type Result struct {
	// Diagnostics is the canonical-form diagnostic map keyed by
	// analyzer name. Two runs that produce equal diagnostics will
	// have equal Digest values.
	Diagnostics map[string][]CanonicalDiag

	// Digest is sha256(Diagnostics) in hex. Stable across runs that
	// produce equal diagnostic streams.
	Digest string

	// L1Metrics is the snapshot of c.L1Metrics() taken at end of run.
	// Hits/Stores/Misses are absolute on the per-call cache.
	L1Metrics cache.L1Metrics

	// L2Metrics mirrors L1Metrics for L2 (gcexportdata) — absolute on
	// the per-call cache.
	L2Metrics cache.L2Metrics

	// IRManager is the manager attached on this run (nil when
	// Config.AttachIRManager was false). Tests assert on
	// IRManager.TotalPins() and IRManager.Snapshot().
	IRManager *l3.SequentialIRManager
}

// AnalyzeOnce drives one Snapshot.Analyze invocation under cfg. The
// gopls cache wiring is identical to internal/pipelinetest's runOnce
// (the W6 contract) — fresh *cache.Cache per call, with L1 / L2 /
// IRManager attached before workspace.NewWithCache.
//
// The function assumes the caller has already installed the analyzer
// set via InstallAnalyzers and set GOPLSCACHE (or supplied cfg.GoplsCacheDir).
func AnalyzeOnce(t *testing.T, ctx context.Context, cfg Config) Result {
	t.Helper()
	if cfg.ModuleRoot == "" {
		t.Fatalf("AnalyzeOnce: ModuleRoot is required")
	}
	if cfg.ToolVersion == "" {
		cfg.ToolVersion = "plaid-lint-w11"
	}
	if cfg.L2BuildEnv == "" {
		cfg.L2BuildEnv = "linux/arm64/cgo0"
	}
	if cfg.L2GoVersion == "" {
		cfg.L2GoVersion = "go1.22"
	}
	if cfg.L1Dir == "" {
		cfg.L1Dir = LeakyTempDir(t, "plaid-w11-l1-")
	}
	if cfg.L2Dir == "" {
		cfg.L2Dir = LeakyTempDir(t, "plaid-w11-l2-")
	}
	if cfg.GoplsCacheDir != "" {
		t.Setenv("GOPLSCACHE", cfg.GoplsCacheDir)
	}

	l1, err := clcache.Open(cfg.L1Dir)
	if err != nil {
		t.Fatalf("Open L1 (%s): %v", cfg.L1Dir, err)
	}
	l2, err := clcache.Open(cfg.L2Dir)
	if err != nil {
		t.Fatalf("Open L2 (%s): %v", cfg.L2Dir, err)
	}
	c := cache.New(nil)
	c.AttachL1(l1, cfg.ToolVersion)
	c.AttachL2(l2, cfg.L2BuildEnv, cfg.L2GoVersion, cfg.ToolVersion)

	var mgr *l3.SequentialIRManager
	if cfg.AttachIRManager {
		mgr = l3.NewSequentialIRManager()
		c.AttachIRManager(mgr)
	}
	if cfg.CacheMutate != nil {
		cfg.CacheMutate(c)
	}

	ws := workspace.NewWithCache(cfg.ModuleRoot, c)
	defer ws.Close()

	if len(cfg.InvalidatePaths) > 0 {
		_ = ws.Invalidate(cfg.InvalidatePaths)
	}

	snap := ws.Snapshot()
	if snap == nil {
		t.Fatalf("AnalyzeOnce: ws.Snapshot returned nil")
	}
	defer snap.Release()
	inner := snap.Inner()
	if err := inner.InitializeWorkspace(ctx); err != nil {
		t.Fatalf("InitializeWorkspace: %v", err)
	}

	wsPkgs := inner.WorkspacePackages()
	pkgs := map[metadata.PackageID]*metadata.Package{}
	for id := range wsPkgs.All() {
		if mp := inner.Metadata(id); mp != nil {
			pkgs[mp.ID] = mp
		}
	}
	if len(pkgs) == 0 {
		t.Fatalf("AnalyzeOnce: no workspace packages discovered for %s", cfg.ModuleRoot)
	}

	diags, err := inner.Analyze(ctx, pkgs, nil)
	if err != nil {
		t.Fatalf("Snapshot.Analyze: %v", err)
	}

	by := make(map[string][]CanonicalDiag)
	for _, d := range diags {
		by[string(d.Source)] = append(by[string(d.Source)], canonicalize(d))
	}
	for k := range by {
		SortDiags(by[k])
	}

	return Result{
		Diagnostics: by,
		Digest:      Digest(by),
		L1Metrics:   c.L1Metrics(),
		L2Metrics:   c.L2Metrics(),
		IRManager:   mgr,
	}
}

// L1FileSet is the set of on-disk L1 entry file paths observed at one
// instant. Used by the safety harness to compute the "packages
// re-analyzed" set as the symmetric-difference of two snapshots: any
// file that exists post-edit but not pre-edit must have been stored
// during the re-analysis.
//
// We compare via *paths*, not action-id hex, because the path layout
// is analyzer/<name>/<shard>/<id> and we want to be able to map a
// new path back to its PackageID by reading the entry. The L1
// content-addressed scheme guarantees: same inputs → same path, so
// any new path corresponds to inputs that differed from the pre-edit
// run.
type L1FileSet map[string]struct{}

// SnapshotL1 walks the L1 cache rooted at l1Dir and returns the set
// of every file under analyzer/. Files outside analyzer/ (e.g.
// meta/cache-version, typecheck/...) are ignored.
//
// This is the load-bearing primitive for the safety harness: it
// captures the "what's on disk" view at a moment in time. The
// difference between two snapshots is the set of files written or
// deleted between them.
func SnapshotL1(t *testing.T, l1Dir string) L1FileSet {
	t.Helper()
	out := make(L1FileSet)
	root := filepath.Join(l1Dir, "analyzer")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		out[p] = struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("SnapshotL1(%s): %v", l1Dir, err)
	}
	return out
}

// L1NewFiles returns the set of paths in after but not in before.
// Each such path is an entry that was written during the run between
// the two snapshots; reading it (via clcache.DecodeL1) yields the
// (Analyzer, PackageID) that re-ran.
func L1NewFiles(before, after L1FileSet) []string {
	out := make([]string, 0)
	for p := range after {
		if _, ok := before[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// PackagesFromL1Files reads each file under paths, decodes the L1
// entry, and returns the deduplicated set of PackageIDs that wrote.
// Files that can't be decoded are skipped with a t.Logf — the safety
// harness wants to know about decode errors but a single corrupt
// entry should not fail the test.
//
// This is the safety-harness primitive that turns "files written" into
// "packages re-analyzed" — the unit the expected_cascade.json
// fixtures express their ground truth in.
func PackagesFromL1Files(t *testing.T, paths []string) map[string]struct{} {
	t.Helper()
	out := make(map[string]struct{})
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Logf("PackagesFromL1Files: read %s: %v (skipping)", p, err)
			continue
		}
		entry, err := clcache.DecodeL1(data)
		if err != nil {
			t.Logf("PackagesFromL1Files: decode %s: %v (skipping)", p, err)
			continue
		}
		if entry.PackageID != "" {
			out[entry.PackageID] = struct{}{}
		}
	}
	return out
}

// CopyTree copies every file under src into dst, creating directories
// as needed. Used by the safety harness and golden tests to materialise
// a writable fixture from a testdata/ tree.
//
// The implementation mirrors pipelinetest's copyTestdataFixture: walk
// src, for every file create the parallel file under dst and io.Copy
// the body. Permissions are copied for files only — directories are
// always 0o755 (we don't ship sensitive-perm testdata).
func CopyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	}); err != nil {
		t.Fatalf("CopyTree(%s -> %s): %v", src, dst, err)
	}
}

// WriteFile is a thin os.WriteFile wrapper that creates parent
// directories and t.Fatals on failure. Reduces boilerplate in
// in-test fixture generators.
func WriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

// RemoveFile is a thin os.Remove wrapper that ignores fs.ErrNotExist.
// Used by golden tests to verify the file-delete flow.
func RemoveFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("Remove(%s): %v", path, err)
	}
}

// CompareStringSets returns (missingFromActual, extraInActual). Used
// by the safety harness to produce a diff between the expected and
// observed cascade sets.
func CompareStringSets(expected, actual map[string]struct{}) (missing, extra []string) {
	for k := range expected {
		if _, ok := actual[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range actual {
		if _, ok := expected[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

// Update is a tiny atomic-ish helper for golden tests using a
// -update flag: it rewrites path with new bytes, sync'd to disk,
// using a tmp+rename so a panic mid-write doesn't truncate the
// existing golden. Used only by the golden-update path; the read
// path uses os.ReadFile directly.
func Update(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadJSON reads path and unmarshals into v. Returns os.IsNotExist
// callable errors for the "no golden yet, run with -update" path.
func ReadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// MarshalJSONIndent is the canonical pretty-print golden-tests use.
// 2-space indent, a trailing newline. Stable across runs.
func MarshalJSONIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Synthetic counter assertions that read more clearly inline than a
// raw int compare. The W11 brief calls out "every test that asserts
// on a counter must include a comment naming the counter being
// asserted and what production behavior it proves" — these helpers
// embed that intent in their name + the doc.

// MustL1HitsGT fails t.Errorf if got <= want. The semantic claim is
// "the warm-path L1 lookup succeeded enough times to demonstrate the
// hit path is wired". On a fresh-cache cold run, this counter must be
// 0; on a warm run after a successful cold, it must be > 0 — that's
// the incremental contract.
func MustL1HitsGT(t *testing.T, m cache.L1Metrics, want int64) {
	t.Helper()
	if m.Hits <= want {
		t.Errorf("L1 hits = %d, want > %d (warm-path L1 lookup must succeed)", m.Hits, want)
	}
}

// MustL1StoresGT fails t.Errorf if got <= want. The semantic claim is
// "the cold-path L1 write succeeded enough times to demonstrate
// analyzers ran end-to-end". A cold run with stores=0 means every
// analyzer skipped (the known bug family: stdlib type-check failed,
// compiles=false, analyzers bail).
func MustL1StoresGT(t *testing.T, m cache.L1Metrics, want int64) {
	t.Helper()
	if m.Stores <= want {
		t.Errorf("L1 stores = %d, want > %d (cold-path analyzer run must produce L1 entries)", m.Stores, want)
	}
}

// MustL1HitsEQ fails t.Errorf if got != want. Used on the cold run
// assertion "L1 hits == 0" — a non-zero count there means a stale
// host cache leaked into the test, which would mask real cache-key
// regressions.
func MustL1HitsEQ(t *testing.T, m cache.L1Metrics, want int64) {
	t.Helper()
	if m.Hits != want {
		t.Errorf("L1 hits = %d, want %d (cold-cache invariant; non-zero hits = leaked host cache)", m.Hits, want)
	}
}

// JoinSorted returns sorted keys of m as a stable string list. Used
// by safety-harness error messages.
func JoinSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// guardSetupOnce serialises the analyzer-installation hot path. The
// settings.AllAnalyzers slice is process-global; tests in the same
// binary that swap it concurrently would race. Tests that call
// InstallAnalyzers from inside a t.Parallel'd subtest must serialise
// across that boundary themselves — we deliberately do NOT bake the
// lock into InstallAnalyzers because the natural pattern is to
// install in the test's TestMain or the parent test before fanning
// out subtests.
var guardSetupOnce sync.Mutex

// Fmt is a one-line fmt.Sprintf wrapper for error messages so the
// callers can avoid pulling in fmt every time.
func Fmt(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Compile-time check the import set above isn't dead.
var _ = (*sync.Mutex)(&guardSetupOnce)
