// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// l1_result_codec_hitsafety_test.go — regression test for the W7 L1
// hit-safety blocker. A Result-bearing prereq must
// never serve a consumer with pass.ResultOf[prereq]=nil:
//
//   - l1Store must SKIP the L1 write entirely when ResultCodec.Encode
//     returns an error (instead of writing an entry with Result=nil
//     that a later run would hit with result=nil).
//   - tryL1Lookup must REFUSE an L1 entry that has empty Result bytes
//     for a Result-bearing prereq descriptor (treat as miss so the
//     analyzer re-runs).
//
// Without the fix, a Result-bearing prereq + an Encode failure (or a
// pre-existing empty-Result entry) produces an L1 hit with result=nil
// for the consumer, which dereferences pass.ResultOf[prereq] and
// crashes.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// brokenEncodeCodec returns a ResultCodec whose Encode always fails.
// Decode is supplied (so CanCacheResult() reports true) but is never
// expected to run, because Encode failing should prevent the entry
// from ever being written.
func brokenEncodeCodec() *analyzers.ResultCodec {
	return &analyzers.ResultCodec{
		Encode: func(any) ([]byte, error) {
			return nil, errors.New("synthetic encode failure")
		},
		Decode: func(blob []byte) (any, error) {
			var r serialisableResult
			if err := json.Unmarshal(blob, &r); err != nil {
				return nil, err
			}
			return &r, nil
		},
	}
}

// TestL1ResultCodecHitSafety_EncodeFailureSkipsStore exercises the
// l1Store side of the hit-safety fix: when a Result-bearing prereq's codec fails to
// Encode, the L1 entry must NOT be written. The next run must
// re-execute the prereq's Run body, the consumer must see a non-nil
// ResultOf and emit the same diagnostics it did on the cold run, and
// the dedicated EncodeFailures counter must rise.
func TestL1ResultCodecHitSafety_EncodeFailureSkipsStore(t *testing.T) {
	requireGo(t)

	prereq := makeSynthPrereq()
	consumer := makeSynthConsumer(prereq)

	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:    prereq,
		ConfigSalt:  func(any) [32]byte { return [32]byte{0x11} },
		ResultCodec: brokenEncodeCodec(),
	})
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:   consumer,
		ConfigSalt: func(any) [32]byte { return [32]byte{0x12} },
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(consumer)}

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()

	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	const toolVer = "plaid-lint-w7-hitsafety-encode"

	runOnceCustom := func(t *testing.T) (runResult, cache.L1Metrics) {
		t.Helper()
		l1, err := clcache.Open(l1Dir)
		if err != nil {
			t.Fatalf("Open L1: %v", err)
		}
		l2, err := clcache.Open(l2Dir)
		if err != nil {
			t.Fatalf("Open L2: %v", err)
		}
		c := cache.New(nil)
		c.AttachL1WithRegistry(l1, toolVer, registry)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()

		beforeL1 := c.L1Metrics()
		diags := runAnalyzePipeline(t, ws)
		afterL1 := c.L1Metrics()
		return runResult{
			diagnostics: diags,
			l1Hits:      afterL1.Hits - beforeL1.Hits,
			l1Stores:    afterL1.Stores - beforeL1.Stores,
		}, afterL1
	}

	cold, coldMetrics := runOnceCustom(t)
	consumerDiags := cold.diagnostics["synthconsumer"]
	if len(consumerDiags) == 0 {
		t.Fatalf("cold: synthconsumer produced no diagnostics; consumer wiring is the load-bearing claim")
	}
	if cold.l1Hits != 0 {
		t.Errorf("cold: L1 hits = %d, want 0 (fresh cache)", cold.l1Hits)
	}
	if coldMetrics.EncodeFailures == 0 {
		t.Errorf("cold: L1 EncodeFailures = 0, want > 0 (the synthetic prereq codec MUST have failed on Encode)")
	}

	// Warm run: the prereq's L1 entry must NOT have been written, so
	// the prereq's Run body re-executes and the consumer sees a
	// fresh, non-nil ResultOf. If the bug regresses (entry written
	// with Result=nil), tryL1Lookup hits with result=nil and the
	// consumer reports its "missing or nil ResultOf" error — its
	// diagnostic count drops to zero.
	warm, warmMetrics := runOnceCustom(t)
	warmConsumerDiags := warm.diagnostics["synthconsumer"]
	if len(warmConsumerDiags) != len(consumerDiags) {
		t.Errorf("warm vs cold consumer diag count mismatch: warm=%d cold=%d (encode failure must skip store so consumer keeps firing)",
			len(warmConsumerDiags), len(consumerDiags))
	}
	if canonicalDigest(warm.diagnostics) != canonicalDigest(cold.diagnostics) {
		t.Errorf("warm vs cold diagnostic streams differ:\n  cold: %s\n  warm: %s",
			canonicalDigest(cold.diagnostics), canonicalDigest(warm.diagnostics))
	}
	// Each run uses a fresh Cache instance, so the counters reset to
	// zero per run. The warm-run prereq must have re-run because no
	// L1 entry was ever stored for it; that re-run re-fails Encode,
	// so EncodeFailures must be > 0 on warm as well. Together with
	// the consumer-diag invariant above this confirms the prereq is
	// running every time (no Result=nil L1 entry was poisoned in
	// place).
	if warmMetrics.EncodeFailures == 0 {
		t.Errorf("warm: EncodeFailures = 0, want > 0 — the prereq must have re-run on warm and re-failed Encode")
	}
	t.Logf("EncodeFailures cold=%d warm=%d (each run is a fresh Cache instance)",
		coldMetrics.EncodeFailures, warmMetrics.EncodeFailures)
}

// TestL1ResultCodecHitSafety_EmptyResultRefusedOnHit exercises the
// tryL1Lookup side of the hit-safety fix: an L1 entry with empty Result for a
// Result-bearing prereq descriptor (the bug-shape a pre-fix binary
// would have written) must be refused — tryL1Lookup returns ok=false
// and the analyzer re-runs.
//
// Fixture shape:
//  1. Cold run: prereq encodes Result, store succeeds (synthResultCodec
//     works). Note: the test re-runs only as a sanity baseline; the
//     load-bearing assertion is in step 2.
//  2. Surgically rewrite the prereq's on-disk L1 entry to one with
//     Result section empty. Then re-run.
//  3. Assertion: the warm run's consumer must STILL produce its
//     diagnostics (it sees a non-nil ResultOf because the bad-shape
//     entry was treated as a miss and the prereq re-ran), and the
//     entry must have been re-stored.
func TestL1ResultCodecHitSafety_EmptyResultRefusedOnHit(t *testing.T) {
	requireGo(t)

	prereq := makeSynthPrereq()
	consumer := makeSynthConsumer(prereq)

	registry := analyzers.NewRegistry()
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:    prereq,
		ConfigSalt:  func(any) [32]byte { return [32]byte{0x21} },
		ResultCodec: synthResultCodec(),
	})
	registry.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:   consumer,
		ConfigSalt: func(any) [32]byte { return [32]byte{0x22} },
	})

	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(consumer)}

	modDir := t.TempDir()
	pipelineFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	t.Setenv("GOPLSCACHE", goplsCacheDir(t))

	const toolVer = "plaid-lint-w7-hitsafety-empty"

	runOnceCustom := func(t *testing.T) runResult {
		t.Helper()
		l1, err := clcache.Open(l1Dir)
		if err != nil {
			t.Fatalf("Open L1: %v", err)
		}
		l2, err := clcache.Open(l2Dir)
		if err != nil {
			t.Fatalf("Open L2: %v", err)
		}
		c := cache.New(nil)
		c.AttachL1WithRegistry(l1, toolVer, registry)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()

		beforeL1 := c.L1Metrics()
		diags := runAnalyzePipeline(t, ws)
		afterL1 := c.L1Metrics()
		return runResult{
			diagnostics: diags,
			l1Hits:      afterL1.Hits - beforeL1.Hits,
			l1Stores:    afterL1.Stores - beforeL1.Stores,
		}
	}

	cold := runOnceCustom(t)
	consumerDiags := cold.diagnostics["synthconsumer"]
	if len(consumerDiags) == 0 {
		t.Fatalf("cold: synthconsumer produced no diagnostics")
	}
	if cold.l1Stores == 0 {
		t.Fatalf("cold: L1 stores = 0, want > 0")
	}

	// Walk the on-disk L1 layout for the prereq and rewrite each
	// entry's Result section to empty (the bug-shape that a pre-fix
	// binary or a concurrent older worker could have produced). We
	// decode → blank Result → re-encode → atomic replace; the
	// content-addressed key stays valid because Result is not part
	// of ComputeL1ActionID.
	rewriteEmptyResultEntries(t, l1Dir, "synthprereq")
	// Evict the consumer's L1 entries so the consumer re-runs and
	// pulls its prereq through pass.ResultOf — that's the path that
	// surfaces the bug. If we left the consumer's entries intact, the
	// consumer's diagnostics would just restore from L1 and the
	// (broken) prereq L1 entry would be ignored, hiding the
	// regression.
	evictAnalyzerL1(t, l1Dir, "synthconsumer")

	// Warm run: tryL1Lookup must refuse the empty-Result entry for
	// the Result-bearing synthprereq descriptor; the prereq's Run
	// body re-executes and the consumer sees a non-nil ResultOf.
	warm := runOnceCustom(t)
	warmConsumerDiags := warm.diagnostics["synthconsumer"]
	if len(warmConsumerDiags) != len(consumerDiags) {
		t.Errorf("warm vs cold consumer diag count mismatch: warm=%d cold=%d (empty-Result entry for codec'd prereq must be refused as miss)",
			len(warmConsumerDiags), len(consumerDiags))
	}
	if canonicalDigest(warm.diagnostics) != canonicalDigest(cold.diagnostics) {
		t.Errorf("warm vs cold diagnostic streams differ:\n  cold: %s\n  warm: %s",
			canonicalDigest(cold.diagnostics), canonicalDigest(warm.diagnostics))
	}
	if warm.l1Stores == 0 {
		t.Errorf("warm: L1 stores = 0, want > 0 (the refused empty-Result entries must be re-stored after the prereq re-ran)")
	}
}

// rewriteEmptyResultEntries walks the on-disk L1 cache for the given
// analyzer name and rewrites each entry's Result section to empty.
// The Result is not part of the content-addressed key, so
// the entry remains addressable under its existing filename. This
// simulates the bug-shape a pre-fix binary could have produced: an
// otherwise-valid L1 entry whose Result bytes are missing.
func rewriteEmptyResultEntries(t *testing.T, l1Dir, analyzer string) {
	t.Helper()
	root := filepath.Join(l1Dir, "analyzer", analyzer)
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entry, err := clcache.DecodeL1(raw)
		if err != nil {
			return err
		}
		entry.Result = nil
		encoded, err := entry.Encode()
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("rewriteEmptyResultEntries(%q): %v", analyzer, err)
	}
	if count == 0 {
		t.Fatalf("rewriteEmptyResultEntries(%q): no entries found at %s; cold run did not store?", analyzer, root)
	}
	t.Logf("rewriteEmptyResultEntries(%q): rewrote %d entries", analyzer, count)
}

// evictAnalyzerL1 removes the on-disk L1 entries for the given
// analyzer so the next run cold-misses for it.
func evictAnalyzerL1(t *testing.T, l1Dir, analyzer string) {
	t.Helper()
	dir := filepath.Join(l1Dir, "analyzer", analyzer)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("evictAnalyzerL1(%q): %v", analyzer, err)
	}
}
