package cache

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"
)

var (
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)

// BenchmarkCacheBaseline characterizes the flat-files cache primitive's
// warm-path performance.
//
// It writes N=10000 L1 entries cold, then reads them back warm, capturing
// per-op write/read latency (mean, median, p99) and final disk size. The
// raw numbers feed the "should we migrate to Pebble?" decision in W5/W6.
//
// Run with: go test -run=^$ -bench=BenchmarkCacheBaseline ./internal/cache/...
// To get full latency stats (not just go test's allocs/op):
//
//	go test -run=BenchmarkCacheBaselineReport -tags=plaid_bench ./internal/cache/...
//
// (The Report test below runs unconditionally and dumps numbers to
// bench/w4-cache-baseline.json under $PLAID_BENCH_OUT.)
func BenchmarkCacheBaselineWrite(b *testing.B) {
	c := newBenchCache(b)
	e := sampleL1()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var seed [8]byte
		binary.LittleEndian.PutUint64(seed[:], uint64(i))
		id := NewActionID(seed[:])
		if err := c.WriteL1(e, id); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCacheBaselineRead(b *testing.B) {
	c := newBenchCache(b)
	e := sampleL1()
	// Pre-populate.
	const seedCount = 1024
	ids := make([]ActionID, seedCount)
	for i := range ids {
		var seed [8]byte
		binary.LittleEndian.PutUint64(seed[:], uint64(i))
		ids[i] = NewActionID(seed[:])
		if err := c.WriteL1(e, ids[i]); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := c.ReadL1(e.Analyzer, ids[i%seedCount])
		if err != nil {
			b.Fatal(err)
		}
	}
}

func newBenchCache(b *testing.B) *Cache {
	b.Helper()
	c, err := Open(b.TempDir())
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	return c
}

// TestCacheBaselineReport writes 10000 L1 entries, reads them back, and
// dumps the per-op write/read latency distribution + disk size to
// $PLAID_BENCH_OUT (or skips if unset). Designed to be invoked once
// during W4 to populate bench/w4-cache-baseline.json.
//
// Skipped under -short or when PLAID_BENCH_OUT is unset, so this does
// not affect normal CI run time.
func TestCacheBaselineReport(t *testing.T) {
	out := os.Getenv("PLAID_BENCH_OUT")
	if out == "" {
		t.Skip("PLAID_BENCH_OUT not set; skipping perf capture")
	}
	if testing.Short() {
		t.Skip("-short")
	}

	const N = 10000
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	base := sampleL1()
	ids := make([]ActionID, N)

	writeLat := make([]time.Duration, N)
	tWriteStart := time.Now()
	for i := 0; i < N; i++ {
		var seed [8]byte
		binary.LittleEndian.PutUint64(seed[:], uint64(i))
		ids[i] = NewActionID(seed[:])
		s := time.Now()
		if err := c.WriteL1(base, ids[i]); err != nil {
			t.Fatal(err)
		}
		writeLat[i] = time.Since(s)
	}
	writeTotal := time.Since(tWriteStart)

	readLat := make([]time.Duration, N)
	tReadStart := time.Now()
	for i := 0; i < N; i++ {
		s := time.Now()
		_, err := c.ReadL1(base.Analyzer, ids[i])
		readLat[i] = time.Since(s)
		if err != nil {
			t.Fatal(err)
		}
	}
	readTotal := time.Since(tReadStart)

	diskSize := dirSize(t, dir)

	wStats := statsOf(writeLat)
	rStats := statsOf(readLat)

	report := map[string]any{
		"n":              N,
		"go_version":     "go1.26",
		"platform":       fmt.Sprintf("%s/%s", goos(), goarch()),
		"write_total_ms": writeTotal.Milliseconds(),
		"write_mean_us":  wStats.mean.Microseconds(),
		"write_p50_us":   wStats.p50.Microseconds(),
		"write_p99_us":   wStats.p99.Microseconds(),
		"write_max_us":   wStats.max.Microseconds(),
		"read_total_ms":  readTotal.Milliseconds(),
		"read_mean_us":   rStats.mean.Microseconds(),
		"read_p50_us":    rStats.p50.Microseconds(),
		"read_p99_us":    rStats.p99.Microseconds(),
		"read_max_us":    rStats.max.Microseconds(),
		"disk_bytes":     diskSize,
		"per_entry_bytes": func() int64 {
			if N == 0 {
				return 0
			}
			return diskSize / int64(N)
		}(),
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote perf report to %s", out)
	t.Logf("write: mean=%v p50=%v p99=%v max=%v total=%v",
		wStats.mean, wStats.p50, wStats.p99, wStats.max, writeTotal)
	t.Logf("read:  mean=%v p50=%v p99=%v max=%v total=%v",
		rStats.mean, rStats.p50, rStats.p99, rStats.max, readTotal)
	t.Logf("disk:  %d bytes total, %d bytes/entry", diskSize, diskSize/int64(N))
}

type latStats struct {
	mean, p50, p99, max time.Duration
}

func statsOf(d []time.Duration) latStats {
	if len(d) == 0 {
		return latStats{}
	}
	cp := make([]time.Duration, len(d))
	copy(cp, d)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var sum time.Duration
	for _, x := range cp {
		sum += x
	}
	return latStats{
		mean: sum / time.Duration(len(cp)),
		p50:  cp[len(cp)/2],
		p99:  cp[(len(cp)*99)/100],
		max:  cp[len(cp)-1],
	}
}

func dirSize(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func goos() string {
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return runtimeGOOS
}

func goarch() string {
	if v := os.Getenv("GOARCH"); v != "" {
		return v
	}
	return runtimeGOARCH
}
