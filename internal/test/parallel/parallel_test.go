// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parallel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/test/harness"
)

// parallelInvocations is the count of concurrent Analyze drivers
// the stress test spawns. The W11 brief calls for 8: large enough
// that the W4 link(2) EEXIST path gets exercised every time, small
// enough that the test stays under a few seconds on a 4-core box.
const parallelInvocations = 8

const parallelWorkerEnv = "PLAID_PARALLEL_WORKER"

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

// fixture writes a small but non-trivial multi-package go module into dir.
// The shape is intentionally small: 2 leaves + 1 mid + 1 root. Cross-flow at
// the mid layer exercises the L2 fast path under contention.
func fixture(t *testing.T, dir string) {
	t.Helper()
	harness.WriteFile(t, filepath.Join(dir, "go.mod"), `module example.com/parallel

go 1.22
`)

	for i := 0; i < 2; i++ {
		name := fmt.Sprintf("leaf%d", i)
		body := fmt.Sprintf(`package %s

type T struct {
	Name string
}

func New(name string) *T { return &T{Name: name} }

func Use(t *T) string {
	if t == nil {
		return ""
	}
	t.Name = t.Name // assign self-assignment trigger
	return t.Name
}
`, name)
		harness.WriteFile(t, filepath.Join(dir, name, name+".go"), body)
	}

	harness.WriteFile(t, filepath.Join(dir, "mid", "mid.go"), `package mid

import (
	"example.com/parallel/leaf0"
	"example.com/parallel/leaf1"
)

type MidT struct {
	Underlying0 *leaf0.T
	Underlying1 *leaf1.T
}

func New(name string) *MidT {
	return &MidT{
		Underlying0: leaf0.New(name),
		Underlying1: leaf1.New(name),
	}
}
`)

	harness.WriteFile(t, filepath.Join(dir, "root", "root.go"), `package root

import "example.com/parallel/mid"

func Run() *mid.MidT { return mid.New("root") }
`)
}

// TestParallelSafetyEightInvocations is the W11 1.41 stress test. Eight
// subprocesses each open the same shared L1+L2 cache dirs and drive
// Snapshot.Analyze on the fixture.
//
// This intentionally uses subprocesses, not goroutines. The contract under
// test is cross-process cache safety for concurrent `plaid-lint run`
// invocations. Goroutines would also share process-global analysis futures and
// phase barriers, which is a different contract and can deadlock independently
// of on-disk cache correctness.
func TestParallelSafetyEightInvocations(t *testing.T) {
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	modDir := harness.LeakyTempDir(t, "plaid-w11-parallel-mod-")
	fixture(t, modDir)

	// Shared cache dirs across all invocations. This is the load-bearing
	// contract: concurrent `plaid-lint run`
	// processes pointing at the same XDG cache produce correct, identical
	// outputs. Each subprocess gets a separate GOPLSCACHE so the stress stays
	// focused on L1/L2 first-writer-wins behavior.
	sharedL1 := harness.LeakyTempDir(t, "plaid-w11-parallel-l1-")
	sharedL2 := harness.LeakyTempDir(t, "plaid-w11-parallel-l2-")

	results := make([]invocationResult, parallelInvocations)
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})

	for i := 0; i < parallelInvocations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-startBarrier
			results[idx] = runParallelWorker(t, idx, modDir, sharedL1, sharedL2)
		}(i)
	}

	close(startBarrier)
	wg.Wait()

	for _, r := range results {
		if r.err != nil {
			t.Errorf("invocation %d errored: %v\noutput:\n%s", r.index, r.err, r.output)
		}
	}

	digests := make(map[string][]int)
	for _, r := range results {
		if r.err != nil {
			continue
		}
		digests[r.digest] = append(digests[r.digest], r.index)
	}
	if len(digests) > 1 {
		buckets := make([]string, 0, len(digests))
		for d, idxs := range digests {
			sort.Ints(idxs)
			buckets = append(buckets, fmt.Sprintf("  digest=%s  invocations=%v", d, idxs))
		}
		sort.Strings(buckets)
		t.Errorf("invocations produced %d distinct digests, want 1 (parallel-safety regression):\n%s",
			len(digests), formatBuckets(buckets))
	}

	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         sharedL1,
		L2Dir:         sharedL2,
		GoplsCacheDir: harness.LeakyTempDir(t, "plaid-w11-parallel-post-gopls-"),
	}
	post := harness.AnalyzeOnce(t, context.Background(), cfg)
	t.Logf("post-stress: L1 hits=%d stores=%d", post.L1Metrics.Hits, post.L1Metrics.Stores)
	if post.L1Metrics.Hits == 0 {
		t.Errorf("post-stress: L1.Hits = 0, want > 0 (some race must have produced reusable entries)")
	}

	hitCounts := make([]int64, 0, parallelInvocations)
	storeCounts := make([]int64, 0, parallelInvocations)
	for _, r := range results {
		if r.err == nil {
			hitCounts = append(hitCounts, r.l1Hits)
			storeCounts = append(storeCounts, r.stores)
		}
	}
	sort.Slice(hitCounts, func(i, j int) bool { return hitCounts[i] < hitCounts[j] })
	sort.Slice(storeCounts, func(i, j int) bool { return storeCounts[i] < storeCounts[j] })
	t.Logf("8-invocation L1 hits (sorted, ascending): %v", hitCounts)
	t.Logf("8-invocation L1 stores (sorted, ascending): %v", storeCounts)
	t.Logf("post-stress L1 metrics: hits=%d stores=%d misses=%d encode_failures=%d errors=%d",
		post.L1Metrics.Hits, post.L1Metrics.Stores, post.L1Metrics.Misses,
		post.L1Metrics.EncodeFailures, post.L1Metrics.Errors)
}

type invocationResult struct {
	index  int
	digest string
	l1Hits int64
	stores int64
	output string
	err    error
}

func runParallelWorker(t *testing.T, idx int, modDir, sharedL1, sharedL2 string) invocationResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestParallelSafetyWorker$", "-test.v")
	cmd.Env = append(envWithout(os.Environ(), "GOCACHEPROG"),
		parallelWorkerEnv+"=1",
		"PLAID_PARALLEL_MODULE_ROOT="+modDir,
		"PLAID_PARALLEL_L1_DIR="+sharedL1,
		"PLAID_PARALLEL_L2_DIR="+sharedL2,
		"PLAID_PARALLEL_GOPLSCACHE="+harness.LeakyTempDir(t, fmt.Sprintf("plaid-w11-parallel-gopls-%d-", idx)),
	)
	out, err := cmd.CombinedOutput()
	result := invocationResult{
		index:  idx,
		output: string(out),
		err:    err,
	}
	if ctx.Err() != nil {
		result.err = ctx.Err()
		return result
	}
	if err != nil {
		return result
	}
	digest, hits, stores, err := parseParallelWorkerResult(string(out))
	if err != nil {
		result.err = err
		return result
	}
	result.digest = digest
	result.l1Hits = hits
	result.stores = stores
	return result
}

func envWithout(env []string, keys ...string) []string {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	out := env[:0]
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if ok && drop[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func parseParallelWorkerResult(out string) (digest string, hits, stores int64, err error) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[0] != "PLAID_PARALLEL_RESULT" {
			continue
		}
		for _, f := range fields[1:] {
			k, v, ok := strings.Cut(f, "=")
			if !ok {
				return "", 0, 0, fmt.Errorf("malformed worker result field %q", f)
			}
			switch k {
			case "digest":
				digest = v
			case "l1_hits":
				hits, err = strconv.ParseInt(v, 10, 64)
				if err != nil {
					return "", 0, 0, fmt.Errorf("parse l1_hits %q: %w", v, err)
				}
			case "stores":
				stores, err = strconv.ParseInt(v, 10, 64)
				if err != nil {
					return "", 0, 0, fmt.Errorf("parse stores %q: %w", v, err)
				}
			default:
				return "", 0, 0, fmt.Errorf("unknown worker result field %q", f)
			}
		}
		if digest == "" {
			return "", 0, 0, fmt.Errorf("worker result missing digest")
		}
		return digest, hits, stores, nil
	}
	return "", 0, 0, fmt.Errorf("worker result line not found")
}

func TestParallelSafetyWorker(t *testing.T) {
	if os.Getenv(parallelWorkerEnv) != "1" {
		t.Skip("helper process for TestParallelSafetyEightInvocations")
	}
	requireGo(t)
	harness.InstallAnalyzers(t, harness.SmallW7Set())

	cfg := harness.Config{
		ModuleRoot:    os.Getenv("PLAID_PARALLEL_MODULE_ROOT"),
		L1Dir:         os.Getenv("PLAID_PARALLEL_L1_DIR"),
		L2Dir:         os.Getenv("PLAID_PARALLEL_L2_DIR"),
		GoplsCacheDir: os.Getenv("PLAID_PARALLEL_GOPLSCACHE"),
	}
	if cfg.ModuleRoot == "" || cfg.L1Dir == "" || cfg.L2Dir == "" || cfg.GoplsCacheDir == "" {
		t.Fatalf("worker env incomplete: module=%q l1=%q l2=%q gopls=%q", cfg.ModuleRoot, cfg.L1Dir, cfg.L2Dir, cfg.GoplsCacheDir)
	}
	res := harness.AnalyzeOnce(t, context.Background(), cfg)
	fmt.Printf("PLAID_PARALLEL_RESULT digest=%s l1_hits=%d stores=%d\n", res.Digest, res.L1Metrics.Hits, res.L1Metrics.Stores)
}

func formatBuckets(b []string) string {
	return strings.Join(b, "\n")
}
