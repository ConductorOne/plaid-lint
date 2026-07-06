// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Phase 1.6 Lever A investigation hook: --profile-allocs.
//
// When --profile-allocs is set, the bench captures a heap pprof
// profile (plus runtime.MemStats and /proc/self/smaps_rollup) at
// the cold-peak watermark and writes them to a directory under
// $SCRATCH. The runtime.MemProfileRate is configured per the paired
// --memprofile-rate flag to allow the paired perturbation sanity
// check the Phase 1.6 spec requires.
//
// Production path is unchanged when --profile-allocs is not set:
// every code path in this file is gated on profileAllocs being true.
// The flag is opt-in scaffolding for the Lever A investigation; the
// engine itself is untouched.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"
)

// Flag values are bound in main() so the help text appears alongside
// the existing flags.
var (
	profileAllocs    bool
	profileAllocsDir string
	memProfileRate   int
	peakSampleHz     time.Duration
)

var (
	profileAllocsOnce  sync.Once
	profileAllocsStart time.Time

	peakRSSBytes atomic.Uint64
	peakCaptured atomic.Bool
	captureMu    sync.Mutex
	captureCount atomic.Int64

	captureEvents []captureEvent
	captureLog    sync.Mutex
)

// captureEvent records each new-max capture for the run summary file.
type captureEvent struct {
	TSec         float64 `json:"t_sec"`
	PhaseLabel   string  `json:"phase_label,omitempty"`
	VmHWMBytes   uint64  `json:"vmhwm_bytes"`
	HeapInuse    uint64  `json:"heap_inuse"`
	HeapSys      uint64  `json:"heap_sys"`
	HeapAlloc    uint64  `json:"heap_alloc"`
	HeapIdle     uint64  `json:"heap_idle"`
	HeapReleased uint64  `json:"heap_released"`
	NumGC        uint32  `json:"num_gc"`
	PprofPath    string  `json:"pprof_path"`
	SmapsPath    string  `json:"smaps_rollup_path"`
	MemStatsPath string  `json:"memstats_path"`
}

// startAllocProfiler installs runtime.MemProfileRate and launches a
// high-cadence sampler that captures a heap pprof + memstats +
// smaps_rollup snapshot every time /proc/self/status VmHWM hits a new
// maximum. No-op when --profile-allocs is not set.
//
// The first capture always fires (even on the initial sample) so a
// run that never grows past its initial RSS still produces at least
// one artifact set the Lever A attribution can compare against.
//
// On ctx.Done the sampler writes a manifest (summary.json) listing
// every capture point with timestamp + memstats deltas; the manifest
// is also flushed via a `defer` to handle SIGINT/panic exits.
func startAllocProfiler(ctx context.Context) error {
	if !profileAllocs {
		return nil
	}
	if profileAllocsDir == "" {
		return fmt.Errorf("--profile-allocs requires --profile-allocs-dir")
	}
	if err := os.MkdirAll(profileAllocsDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", profileAllocsDir, err)
	}
	// Configure sampling rate before any allocation tracking starts.
	// Default 512K matches Phase 1.5 baseline; 1 means every alloc.
	if memProfileRate > 0 {
		runtime.MemProfileRate = memProfileRate
	}
	profileAllocsStart = time.Now()

	// T+0 capture: always fire, so the first profile is the
	// baseline that all subsequent samples are diffed against.
	captureNewPeak(readVmHWMBytesLocal(), true)

	go func() {
		cadence := peakSampleHz
		if cadence <= 0 {
			cadence = 1 * time.Second
		}
		ticker := time.NewTicker(cadence)
		defer ticker.Stop()
		defer flushAllocProfilerManifest()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rss := readVmHWMBytesLocal()
				prev := peakRSSBytes.Load()
				if rss > prev {
					captureNewPeak(rss, false)
				}
			}
		}
	}()
	return nil
}

// captureNewPeak writes the heap pprof / memstats / smaps_rollup
// triple for a new VmHWM maximum. force = true bypasses the >prev
// check (used for the T+0 baseline).
//
// We serialize captures via captureMu so concurrent ticker fires
// (impossible in practice but cheap to guard) can't interleave.
func captureNewPeak(rss uint64, force bool) {
	captureNewPeakLabeled(rss, force, "")
}

// CapturePhase forces a heap pprof / memstats / smaps_rollup
// capture at a named phase boundary regardless of the VmHWM
// watermark. Used by the Phase 1.9 / WF.0 workspace-residency-floor
// attribution audit to snapshot the heap at scenario phase boundaries
// (end-of-load, end-of-fanout, idle-post-run). No-op when
// --profile-allocs is not set. If gc is true, runtime.GC() is invoked
// before the capture so HeapInuse reflects retained-not-cached
// memory — used at the idle-post-run boundary to expose the genuine
// residency floor.
func CapturePhase(label string, gc bool) {
	if !profileAllocs {
		return
	}
	if gc {
		runtime.GC()
	}
	captureNewPeakLabeled(readVmHWMBytesLocal(), true, label)
}

// captureNewPeakLabeled is the inner implementation of captureNewPeak
// plus CapturePhase. When phaseLabel is non-empty it is included in
// the on-disk filename so the WF.0 attribution can identify which
// capture corresponds to which scenario phase boundary.
func captureNewPeakLabeled(rss uint64, force bool, phaseLabel string) {
	captureMu.Lock()
	defer captureMu.Unlock()
	if !force {
		if rss <= peakRSSBytes.Load() {
			return
		}
	}
	if rss > peakRSSBytes.Load() {
		peakRSSBytes.Store(rss)
	}
	peakCaptured.Store(true)
	idx := captureCount.Add(1)

	tSec := time.Since(profileAllocsStart).Seconds()
	var tag string
	if phaseLabel != "" {
		// Sanitize label for filename use: replace ':' and '/' with '_'.
		safe := make([]byte, 0, len(phaseLabel))
		for i := 0; i < len(phaseLabel); i++ {
			c := phaseLabel[i]
			if c == ':' || c == '/' {
				safe = append(safe, '_')
			} else {
				safe = append(safe, c)
			}
		}
		tag = fmt.Sprintf("T+%06.1fs-phase-%s-%03d", tSec, string(safe), idx)
	} else {
		tag = fmt.Sprintf("T+%06.1fs-peak-%03d", tSec, idx)
	}

	pprofPath := filepath.Join(profileAllocsDir, tag+"-heap.pb.gz")
	smapsPath := filepath.Join(profileAllocsDir, tag+"-smaps-rollup.txt")
	statsPath := filepath.Join(profileAllocsDir, tag+"-mem-stats.json")

	if hf, err := os.Create(pprofPath); err == nil {
		if perr := pprof.Lookup("heap").WriteTo(hf, 0); perr != nil {
			fmt.Fprintf(os.Stderr, "profile-allocs: heap %s: %v\n", tag, perr)
		}
		_ = hf.Close()
	} else {
		fmt.Fprintf(os.Stderr, "profile-allocs: create %s: %v\n", pprofPath, err)
	}

	if err := copyProcFile(smapsPath, "/proc/self/smaps_rollup"); err != nil {
		fmt.Fprintf(os.Stderr, "profile-allocs: smaps_rollup %s: %v\n", tag, err)
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	if mf, err := os.Create(statsPath); err == nil {
		enc := json.NewEncoder(mf)
		enc.SetIndent("", "  ")
		_ = enc.Encode(memStatsSnapshot(rss, &m))
		_ = mf.Close()
	}

	captureLog.Lock()
	captureEvents = append(captureEvents, captureEvent{
		TSec:         tSec,
		PhaseLabel:   phaseLabel,
		VmHWMBytes:   rss,
		HeapInuse:    m.HeapInuse,
		HeapSys:      m.HeapSys,
		HeapAlloc:    m.HeapAlloc,
		HeapIdle:     m.HeapIdle,
		HeapReleased: m.HeapReleased,
		NumGC:        m.NumGC,
		PprofPath:    pprofPath,
		SmapsPath:    smapsPath,
		MemStatsPath: statsPath,
	})
	captureLog.Unlock()
}

// memStatsSnapshot wraps the runtime.MemStats fields Lever A needs
// in a JSON-friendly shape (the runtime type itself has many fields
// the report does not consume).
func memStatsSnapshot(rss uint64, m *runtime.MemStats) map[string]any {
	return map[string]any{
		"vmhwm_bytes":           rss,
		"heap_alloc":            m.HeapAlloc,
		"heap_sys":              m.HeapSys,
		"heap_idle":             m.HeapIdle,
		"heap_inuse":            m.HeapInuse,
		"heap_released":         m.HeapReleased,
		"sys_total":             m.Sys,
		"stack_inuse":           m.StackInuse,
		"stack_sys":             m.StackSys,
		"mspan_sys":             m.MSpanSys,
		"mcache_sys":            m.MCacheSys,
		"buckhash_sys":          m.BuckHashSys,
		"gc_sys":                m.GCSys,
		"other_sys":             m.OtherSys,
		"next_gc":               m.NextGC,
		"num_gc":                m.NumGC,
		"mem_profile_rate":      runtime.MemProfileRate,
		"total_alloc_bytes":     m.TotalAlloc,
		"mallocs":               m.Mallocs,
		"frees":                 m.Frees,
		"num_forced_gc":         m.NumForcedGC,
		"gc_cpu_fraction":       m.GCCPUFraction,
		"pause_total_nanos":     m.PauseTotalNs,
		"last_gc_unix_nanos":    m.LastGC,
		"heap_objects":          m.HeapObjects,
		"goroutines":            runtime.NumGoroutine(),
		"go_max_procs":          runtime.GOMAXPROCS(0),
		"capture_t_sec":         time.Since(profileAllocsStart).Seconds(),
	}
}

// flushAllocProfilerManifest is called on ctx.Done from inside the
// sampler goroutine; it is also wired into a defer in main() so
// SIGINT/panic exits don't lose the manifest.
func flushAllocProfilerManifest() {
	if !profileAllocs {
		return
	}
	profileAllocsOnce.Do(func() {
		captureLog.Lock()
		events := append([]captureEvent(nil), captureEvents...)
		captureLog.Unlock()

		path := filepath.Join(profileAllocsDir, "summary.json")
		f, err := os.Create(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "profile-allocs: create %s: %v\n", path, err)
			return
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"mem_profile_rate":       runtime.MemProfileRate,
			"capture_count":          len(events),
			"final_peak_vmhwm_bytes": peakRSSBytes.Load(),
			"events":                 events,
			"go_max_procs":           runtime.GOMAXPROCS(0),
			"run_started":            profileAllocsStart.UTC().Format(time.RFC3339Nano),
			"run_ended":              time.Now().UTC().Format(time.RFC3339Nano),
		})
		fmt.Fprintf(os.Stderr, "profile-allocs: wrote %s (%d captures, peak VmHWM=%d bytes)\n",
			path, len(events), peakRSSBytes.Load())
	})
}

// copyProcFile copies a /proc pseudo-file to dst. /proc files require
// a Read-then-write pattern; io.Copy works on most kernels but can
// return short reads on smaps for large processes (the Phase 1.5
// instrumentation hit this).
func copyProcFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	b, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// readVmHWMBytesLocal duplicates the bench's VmHWM-reading logic so
// the alloc-profiler does not introduce a dependency on
// internal/bench/harness.go's unexported helper.
func readVmHWMBytesLocal() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return 0
	}
	// Walk lines looking for "VmHWM:".
	for i := 0; i < len(b); {
		end := i
		for end < len(b) && b[end] != '\n' {
			end++
		}
		line := b[i:end]
		const prefix = "VmHWM:"
		if len(line) > len(prefix) && string(line[:len(prefix)]) == prefix {
			// Parse "<ws>NNN kB".
			rest := line[len(prefix):]
			var val uint64
			started := false
			for _, c := range rest {
				if c >= '0' && c <= '9' {
					val = val*10 + uint64(c-'0')
					started = true
				} else if started {
					break
				}
			}
			return val * 1024
		}
		i = end + 1
	}
	return 0
}
