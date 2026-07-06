// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// l2_entry_size_test.go — regression test for the L2 FileSetSnapshot
// quadratic-disk-growth defect.
//
// Before the fix, every L2 entry serialized the batch-wide token.FileSet
// at the moment of write. As the batch progressed, the FileSet
// accumulated *token.File entries for every parsed or imported file,
// so the N-th L2 store wrote roughly N files' worth of bytes — net L2
// disk grew O(packages²). On c1's 5,026-package closure this filled
// 184 GB before SIGKILL.
//
// Post-fix, each L2 entry carries only its own package's *token.File
// entries.
//
// This test drives the bench_medium fixture (19 packages) through a
// cold Snapshot.Analyze and asserts:
//
//   - Every entry under <l2Dir>/typecheck/ is bounded in size.
//   - The total typecheck/ directory is bounded.
//
// On the pre-fix HEAD the synthetic fixture is small enough that
// quadratic growth is modest (~tens of KB per entry), but the
// per-entry max + the total both grow with batch state in a way the
// fixed code does not. The bounds below are calibrated to FAIL on the
// pre-fix encoding and PASS post-fix with a comfortable safety margin.

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// copyTree recursively copies srcDir → dstDir. Files inherit 0o644
// permissions and directories inherit 0o755 — sufficient for the
// generated bench fixture, which has no executable files.
func copyTree(t *testing.T, srcDir, dstDir string) {
	t.Helper()
	err := filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copyTree(%s → %s): %v", srcDir, dstDir, err)
	}
}

// l2Stats walks <l2Dir>/typecheck/ and returns the per-file sizes plus
// totals.
type l2Stats struct {
	count    int
	maxBytes int64
	sumBytes int64
}

func collectL2Stats(t *testing.T, l2Dir string) l2Stats {
	t.Helper()
	root := filepath.Join(l2Dir, "typecheck")
	var stats l2Stats
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				// no L2 stores happened
				return filepath.SkipAll
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		stats.count++
		stats.sumBytes += size
		if size > stats.maxBytes {
			stats.maxBytes = size
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return stats
}

// TestL2EntrySizeBounded is the W10-fix regression test.
//
// Drives bench_medium (19 packages) through cold Snapshot.Analyze and
// asserts:
//
//   - count > 0: L2 actually stored at least one entry (sanity).
//   - max < 1 MB per entry: each entry's FileSetSnapshot is bounded by
//     the producing package's own files, not the cumulative batch.
//   - total < 10 MB across all entries: total disk usage scales
//     linearly with package count, not quadratically.
//
// Empirical numbers on the pre-fix vs post-fix code paths against the
// bench_medium 19-package fixture:
//
//	            pre-fix    post-fix
//	  count       19         19
//	  max       4,043 B      752 B
//	  total    46,444 B   13,834 B
//
// The 1 MB / 10 MB ceilings provide headroom for fixture growth while
// still flagging any regression that brings batch-wide-FileSet
// serialization back. The companion size-scaling assertion that fails
// hard on pre-fix HEAD lives in the cache layer
// (TestFileSetSnapshotIsPerPackage in internal/cache).
func TestL2EntrySizeBounded(t *testing.T) {
	requireGo(t)
	installPipelineAnalyzers(t)
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	modDir := leakyTempDir(t, "plaid-l2size-mod-")
	src := filepath.Join("testdata", "bench_medium")
	copyTree(t, src, modDir)

	l1Dir := leakyTempDir(t, "plaid-l2size-l1-")
	l2Dir := leakyTempDir(t, "plaid-l2size-l2-")
	const toolVer = "plaid-lint-l2size-test"

	// Cold run only; we want to measure what cold stored.
	l1, err := clcache.Open(l1Dir)
	if err != nil {
		t.Fatalf("Open L1: %v", err)
	}
	l2, err := clcache.Open(l2Dir)
	if err != nil {
		t.Fatalf("Open L2: %v", err)
	}
	c := cache.New(nil)
	c.AttachL1(l1, toolVer)
	c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
	ws := workspace.NewWithCache(modDir, c)
	defer ws.Close()

	_ = runAnalyzePipeline(t, ws)

	stats := collectL2Stats(t, l2Dir)
	t.Logf("L2 entries: count=%d max=%d total=%d", stats.count, stats.maxBytes, stats.sumBytes)
	if stats.count == 0 {
		t.Fatalf("no L2 entries stored under %s/typecheck/; bench_medium pipeline should produce at least one", l2Dir)
	}

	// Headroom ceiling: 1 MB per entry / 10 MB total. The post-fix
	// numbers above are 3-4 orders of magnitude under this, so the
	// ceiling will flag any new regression that re-introduces
	// batch-wide-FileSet growth.
	const maxPerEntry int64 = 1 << 20 // 1 MB
	if stats.maxBytes >= maxPerEntry {
		t.Errorf("max L2 entry size = %d bytes, want < %d (%d KB)", stats.maxBytes, maxPerEntry, maxPerEntry/1024)
	}
	const maxTotal int64 = 10 * (1 << 20) // 10 MB
	if stats.sumBytes >= maxTotal {
		t.Errorf("total L2 disk = %d bytes, want < %d (%d MB)", stats.sumBytes, maxTotal, maxTotal/(1<<20))
	}

	// Tight bound calibrated against the post-fix encoding on
	// bench_medium: max < 1500 B / total < 25000 B. The pre-fix code
	// path produces max ≈ 4 KB and total ≈ 46 KB on the same fixture,
	// so violating either inequality is the signal that batch-wide
	// FileSet serialization has returned. These bounds were chosen
	// with ~2× headroom over the observed post-fix values so they
	// don't false-fire on noise but catch any meaningful regression.
	const maxPerEntryTight int64 = 1500
	if stats.maxBytes >= maxPerEntryTight {
		t.Errorf("max L2 entry size = %d bytes, want < %d (post-fix observed ~750 B on bench_medium; "+
			"the pre-fix code path produced ~4 KB — has FileSetSnapshot regressed to batch-wide?)",
			stats.maxBytes, maxPerEntryTight)
	}
	const maxTotalTight int64 = 25000
	if stats.sumBytes >= maxTotalTight {
		t.Errorf("total L2 disk = %d bytes, want < %d (post-fix observed ~14 KB on bench_medium; "+
			"the pre-fix code path produced ~46 KB)",
			stats.sumBytes, maxTotalTight)
	}
}
