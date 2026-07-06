// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package l3

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/scheduler"
	"github.com/conductorone/plaid-lint/internal/test/harness"
)

// peakRSSCeilingBytes is the design ceiling for the
// concurrency=1 + IR-streaming path: 1.5 GB on a c1-scale workload.
// Synthetic fixtures peak at ~55 MB (W10 calibration), so we
// assert the test peaks well under the ceiling; the real
// validation is the W12+ c1 cascade benchmark.
const peakRSSCeilingBytes uint64 = 1536 * 1024 * 1024

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go command not available: %v", err)
	}
}

// streamingFixture generates a production-shape go module under dir.
// The shape mirrors bench/MediumShape from internal/bench/: 10 leaves
// + 6 mid + 3 roots = 19 packages with mid-layer fan-out. Designed
// to exercise enough analyzers × packages that the scheduler's gate
// is binding and the IRManager observes meaningful pin churn.
//
// Why not call bench.GenerateFixture: bench is internal package and
// the test package is fine to import it, but we keep this test
// independent of bench's exact synthesis defaults so a tweak to
// bench's shape doesn't drift this test out from under us.
func streamingFixture(t *testing.T, dir string) {
	t.Helper()
	harness.WriteFile(t, filepath.Join(dir, "go.mod"), `module example.com/l3stream

go 1.22
`)

	const numLeaves = 10
	const numMid = 6
	const numRoots = 3
	const leafFanout = 4

	for i := 0; i < numLeaves; i++ {
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
	t.Name = t.Name
	return t.Name
}
`, name)
		harness.WriteFile(t, filepath.Join(dir, name, name+".go"), body)
	}

	for i := 0; i < numMid; i++ {
		name := fmt.Sprintf("mid%d", i)
		var importLines, useLines string
		for j := 0; j < leafFanout; j++ {
			k := (i + j) % numLeaves
			importLines += fmt.Sprintf("\t%q\n", "example.com/l3stream/leaf"+itoa(k))
			useLines += fmt.Sprintf("\tleaf%d.Use(leaf%d.New(%q))\n", k, k, name+"_use")
		}
		body := fmt.Sprintf(`package %s

import (
%s)

type MidT struct {
	Name string
}

func New(name string) *MidT { return &MidT{Name: name} }

func Touch() string {
%s	return %q
}
`, name, importLines, useLines, name)
		harness.WriteFile(t, filepath.Join(dir, name, name+".go"), body)
	}

	for i := 0; i < numRoots; i++ {
		name := fmt.Sprintf("root%d", i)
		var importLines, useLines string
		for j := 0; j < numMid; j++ {
			k := (i + j) % numMid
			importLines += fmt.Sprintf("\t%q\n", "example.com/l3stream/mid"+itoa(k))
			useLines += fmt.Sprintf("\t_ = mid%d.Touch()\n", k)
		}
		body := fmt.Sprintf(`package %s

import (
%s)

func Run() {
%s}
`, name, importLines, useLines)
		harness.WriteFile(t, filepath.Join(dir, name, name+".go"), body)
	}
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }

// readVmHWMBytes returns the VmHWM (peak resident set size) of the
// current process in bytes, parsed from /proc/self/status. Returns
// 0 on non-Linux or if the file is unreadable.
//
// The implementation matches the W10 scheduler's
// readVmHWMBytes byte-for-byte — by design, the W11 test uses the
// same sampler boundary the W10 calibration was run against, so
// observed peaks are directly comparable. Duplicated here rather
// than imported because the bench package's copy is not exported.
func readVmHWMBytes() uint64 {
	buf, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	lines := splitLines(buf)
	for _, line := range lines {
		const prefix = "VmHWM:"
		if !hasPrefix(line, prefix) {
			continue
		}
		rest := trimSpace(line[len(prefix):])
		i := indexByte(rest, ' ')
		var num []byte
		if i >= 0 {
			num = trimSpace(rest[:i])
		} else {
			num = rest
		}
		n, ok := parseUint(num)
		if !ok {
			return 0
		}
		return n * 1024
	}
	return 0
}

func splitLines(b []byte) [][]byte {
	out := [][]byte{}
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func hasPrefix(b []byte, p string) bool {
	if len(b) < len(p) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if b[i] != p[i] {
			return false
		}
	}
	return true
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func parseUint(b []byte) (uint64, bool) {
	if len(b) == 0 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

// TestL3StreamingCorrectness drives the full Phase 1 analyzer set
// (102 analyzers from harness.FullPhase1Set) at concurrency=1
// against the streaming fixture. Assertions:
//
//   - Analyze completes without error.
//   - The L3 IRManager observed pin events (NeedsIR analyzers fired).
//   - The L3 IRManager has no leaked pins at end of run.
//   - Peak VmHWM is under the 1.5 GB ceiling (with the caveat that
//     synthetic-fixture peaks are ~30× under target; the real
//     validation is W12 c1).
//   - Cold→warm diagnostic equivalence holds.
//
// The 102-analyzer assertion is the W10 pin: a regression to
// the W7-only set (count=8) here would surface as a length mismatch
// before any analysis runs.
func TestL3StreamingCorrectness(t *testing.T) {
	requireGo(t)

	set := harness.FullPhase1Set()
	// Counter assertion: full Phase 1 set is 102 analyzers (W10
	// pin). Any drift here surfaces a wiring regression
	// before we even start analyzing.
	if len(set) != 102 {
		t.Fatalf("harness.FullPhase1Set() returned %d analyzers, want 102 (W10 regression)", len(set))
	}
	// Sanity: a couple of representative entries from each W7 + W8
	// origin appear in the set.
	wantPresent := map[string]bool{
		"printf":  false, // W7 root, non-SA
		"SA1000":  false, // W7 root, dedup'd from SA mass-wire
		"SA4017":  false, // W8 mass-wire, NeedsIR via fact_purity
		"SA9009":  false, // W8 mass-wire, no Requires
	}
	for _, a := range set {
		if _, ok := wantPresent[a.Name]; ok {
			wantPresent[a.Name] = true
		}
	}
	for name, present := range wantPresent {
		if !present {
			t.Errorf("FullPhase1Set missing %q (analyzer-set composition regression)", name)
		}
	}

	harness.InstallAnalyzers(t, set)

	// Materialise the streaming fixture.
	modDir := harness.LeakyTempDir(t, "plaid-w11-l3-mod-")
	streamingFixture(t, modDir)
	goplsDir := harness.LeakyTempDir(t, "plaid-w11-l3-gopls-")
	l1Dir := harness.LeakyTempDir(t, "plaid-w11-l3-l1-")
	l2Dir := harness.LeakyTempDir(t, "plaid-w11-l3-l2-")

	// VmHWM sample before the run; we'll compare end - start to get
	// the run's contribution to peak RSS.
	preRSS := readVmHWMBytes()

	// Cold run: fresh L1+L2, IRManager attached, scheduler attached
	// with concurrency=1. The scheduler's concurrency=1 cap is the
	// "L3 streaming mode" the W9 brief specifies; it serialises the
	// per-package analyzer fan-out so peak RSS stays bounded.
	rss := scheduler.NewRSSBudgetScheduler(scheduler.DefaultRSSBudgetBytes, 1)

	// We deliberately do NOT set AttachIRManager: when a scheduler
	// is attached and the scheduler implements l3.IRManager (which
	// RSSBudgetScheduler does), AttachScheduler installs the
	// scheduler as the active IRManager. AttachIRManager-then-
	// AttachScheduler would have AttachScheduler clobber the W8
	// IRManager — see internal/gopls/cache/cache.go:350. We read
	// pin/release counters off the scheduler's Stats directly.
	cfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
		CacheMutate: func(c *cache.Cache) {
			c.AttachScheduler(scheduler.AsCacheScheduler(rss))
		},
	}
	cold := harness.AnalyzeOnce(t, context.Background(), cfg)
	postColdRSS := readVmHWMBytes()
	t.Logf("cold: L1 hits=%d stores=%d / L2 hits=%d stores=%d / analyzers=%d / VmHWM=%dB → %dB",
		cold.L1Metrics.Hits, cold.L1Metrics.Stores,
		cold.L2Metrics.Hits, cold.L2Metrics.Stores,
		len(set), preRSS, postColdRSS)

	// Counter assertion: cold-run L1 stores > 0 proves the 102
	// analyzers actually executed end-to-end. Zero stores = stdlib
	// regression or analyzer wiring regression.
	if cold.L1Metrics.Stores == 0 {
		t.Fatalf("cold: L1.Stores = 0; analyzers did not run (wiring regression)")
	}

	// IR pin/release contract — read from the scheduler's Stats
	// (the scheduler IS the active IRManager when AttachScheduler
	// installed an l3.IRManager-implementing scheduler). At least
	// one pin must have fired (the W8 mass-wired NeedsIR analyzers
	// SA4017, SA5012, nilness, etc. pin per-package); zero pins is
	// the W8 regression. Every pin must be released by
	// Analyze return; pin/release count mismatch is the leak
	// canary.
	pinSt := rss.Stats()
	t.Logf("cold: scheduler IRPinEvents = %d, IRReleaseEvents = %d", pinSt.IRPinEvents, pinSt.IRReleaseEvents)
	if pinSt.IRPinEvents == 0 {
		t.Errorf("cold: scheduler.IRPinEvents = 0; NeedsIR analyzers should pin (W8 regression)")
	}
	if pinSt.IRReleaseEvents != pinSt.IRPinEvents {
		t.Errorf("cold: IRReleaseEvents (%d) != IRPinEvents (%d): pin leak (W8)",
			pinSt.IRReleaseEvents, pinSt.IRPinEvents)
	}
	if leaked := rss.Snapshot(); len(leaked) != 0 {
		t.Errorf("cold: scheduler.Snapshot = %v, want empty (no leaked pins on scheduler)", leaked)
	}

	// Scheduler IR pin/release stream must mirror the manager's.
	st := rss.Stats()
	t.Logf("cold scheduler: acquired=%d completed=%d blocked=%d peak=%d pins=%d releases=%d",
		st.ActionsAcquired, st.ActionsCompleted, st.ActionsBlocked,
		st.PeakConcurrency, st.IRPinEvents, st.IRReleaseEvents)
	if st.IRPinEvents != st.IRReleaseEvents {
		t.Errorf("scheduler: IRPinEvents (%d) != IRReleaseEvents (%d) — pin/release stream incomplete",
			st.IRPinEvents, st.IRReleaseEvents)
	}
	if st.PeakConcurrency > 1 {
		t.Errorf("scheduler: PeakConcurrency = %d, want ≤ 1 (concurrency=1 cap binding)", st.PeakConcurrency)
	}

	// VmHWM ceiling assertion. peak = max VmHWM since process start;
	// we treat postColdRSS as the run's peak (VmHWM is monotonic per
	// kernel docs). The 1.5 GB ceiling is the design figure for
	// c1-scale; synthetic fixtures should be 30× under, so this
	// assertion's load-bearing value is "if we exceed 1.5 GB on a
	// 19-package synthetic fixture, the architecture is broken".
	if postColdRSS > peakRSSCeilingBytes {
		t.Errorf("cold peak VmHWM = %d B (%.2f GB), want ≤ %d B (%.2f GB) — memory ceiling exceeded on synthetic fixture",
			postColdRSS, float64(postColdRSS)/1e9,
			peakRSSCeilingBytes, float64(peakRSSCeilingBytes)/1e9)
	}
	t.Logf("VmHWM headroom: %d B used, %d B ceiling (%.1fx under target — synthetic fixture, see doc.go)",
		postColdRSS, peakRSSCeilingBytes, float64(peakRSSCeilingBytes)/float64(postColdRSS+1))

	// Warm run: same L1+L2 dirs, fresh scheduler so the cumulative
	// counter inspection is clean. The cold→warm diagnostic
	// equivalence is the contract every Phase 1 test asserts.
	warmRSS := scheduler.NewRSSBudgetScheduler(scheduler.DefaultRSSBudgetBytes, 1)
	warmCfg := harness.Config{
		ModuleRoot:    modDir,
		L1Dir:         l1Dir,
		L2Dir:         l2Dir,
		GoplsCacheDir: goplsDir,
		CacheMutate: func(c *cache.Cache) {
			c.AttachScheduler(scheduler.AsCacheScheduler(warmRSS))
		},
	}
	warm := harness.AnalyzeOnce(t, context.Background(), warmCfg)
	t.Logf("warm: L1 hits=%d stores=%d / L2 hits=%d stores=%d",
		warm.L1Metrics.Hits, warm.L1Metrics.Stores,
		warm.L2Metrics.Hits, warm.L2Metrics.Stores)
	if warm.L1Metrics.Hits == 0 {
		t.Errorf("warm: L1.Hits = 0; warm-path L1 lookup must succeed")
	}
	if cold.Digest != warm.Digest {
		t.Errorf("cold↔warm digest mismatch (cold: %s | warm: %s)", cold.Digest, warm.Digest)
	}
	// Warm-run pin invariant: pins still fire on warm because the
	// L1 cache hit path doesn't capture pin events;
	// only when the analyzer's Run body executes. We assert
	// release-vs-pin balance via the scheduler's Stats, not exact
	// pin counts (warm hits skip Run, so warm pin events < cold).
	warmPinSt := warmRSS.Stats()
	t.Logf("warm: scheduler IRPinEvents = %d, IRReleaseEvents = %d",
		warmPinSt.IRPinEvents, warmPinSt.IRReleaseEvents)
	if warmPinSt.IRReleaseEvents != warmPinSt.IRPinEvents {
		t.Errorf("warm: IRReleaseEvents (%d) != IRPinEvents (%d): pin leak",
			warmPinSt.IRReleaseEvents, warmPinSt.IRPinEvents)
	}
	if leaked := warmRSS.Snapshot(); len(leaked) != 0 {
		t.Errorf("warm: scheduler.Snapshot = %v, want empty (pin leak)", leaked)
	}
}

// TestL3StreamingAnalyzerCount is a tiny defensive pin: the
// full Phase 1 set must remain at 102 analyzers, matching the W10
// corrected workload. A change here is the canary for
// "someone added/removed an analyzer without thinking through cache
// invalidation"; the test exists separately from the main streaming
// test so a count drift surfaces even when the larger test is
// skipped (e.g. no `go` binary on PATH).
func TestL3StreamingAnalyzerCount(t *testing.T) {
	set := analyzers.AllPhase1RootAnalyzers()
	if got := len(set); got != 102 {
		t.Errorf("AllPhase1RootAnalyzers = %d analyzers, want 102 (W10 pin)", got)
	}
}
