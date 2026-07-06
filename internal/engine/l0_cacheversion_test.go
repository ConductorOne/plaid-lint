// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package engine

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
)

// fakeAnalyzerForCacheVersion returns a minimal *analysis.Analyzer used
// only by the cacheVersion plumbing tests in this file.
func fakeAnalyzerForCacheVersion(name string) *analysis.Analyzer {
	return &analysis.Analyzer{
		Name: name,
		Doc:  "fake",
		Run:  func(*analysis.Pass) (any, error) { return nil, nil },
	}
}

// TestCacheVersion_L0SetHashInvalidatesOnDescriptorBump verifies that
// bumping a single descriptor's CacheVersion changes the L0 analyzer-
// set hash — the surface the L0 key uses to detect "this wrapper's
// emission contract changed; old cache entries are stale".
func TestCacheVersion_L0SetHashInvalidatesOnDescriptorBump(t *testing.T) {
	a := fakeAnalyzerForCacheVersion("errcheck")
	reg := analyzers.NewRegistry()
	reg.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{1} },
		AnalyzerVersion: "test-bin-v1",
		CacheVersion:    1,
	})
	plan := &runPlan{analyzers: []analyzerEntry{{analyzer: a}}}

	hashV1 := computeAnalyzerSetHash(plan, reg)

	reg.Register(&analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{1} },
		AnalyzerVersion: "test-bin-v1",
		CacheVersion:    2,
	})
	hashV2 := computeAnalyzerSetHash(plan, reg)

	if hashV1 == hashV2 {
		t.Errorf("L0 analyzer-set hash unchanged across CacheVersion bump 1→2")
	}
}

// TestCacheVersion_L0AnalyzerVersionFormat pins the version-string
// shape (.cvN.eN suffix). The shape is part of the cache stability
// contract: changing it must itself be accompanied by an
// EngineCacheVersion bump so cached entries against the old format
// invalidate cleanly.
func TestCacheVersion_L0AnalyzerVersionFormat(t *testing.T) {
	a := fakeAnalyzerForCacheVersion("fake")
	d := &analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{} },
		AnalyzerVersion: "base-v1",
		CacheVersion:    7,
	}
	got := analyzerVersionFor(a, d)
	if !strings.HasPrefix(got, "base-v1.cv7.e") {
		t.Errorf("analyzerVersionFor: got %q, want prefix base-v1.cv7.e", got)
	}
	gotStub := analyzerVersionFor(a, nil)
	if !strings.Contains(gotStub, ".cv0.e") {
		t.Errorf("analyzerVersionFor(nil desc): expected .cv0.e suffix; got %q", gotStub)
	}
}

// TestCacheVersion_L0EngineLevelInvalidatesAll verifies that the
// engine-level constant participates in the version-string format —
// i.e. bumping analyzers.EngineCacheVersion would invalidate every
// analyzer's L0 entries simultaneously. We can't actually mutate a
// const at runtime; instead, assert the constant appears in the
// rendered version string.
func TestCacheVersion_L0EngineLevelInvalidatesAll(t *testing.T) {
	a := fakeAnalyzerForCacheVersion("fake")
	d := &analyzers.AnalyzerDescriptor{
		Analyzer:        a,
		ConfigSalt:      func(any) [32]byte { return [32]byte{} },
		AnalyzerVersion: "base-v1",
		CacheVersion:    0,
	}
	got := analyzerVersionFor(a, d)
	// e<N> where N = analyzers.EngineCacheVersion.
	wantSuffix := ".e" + decimal(int(analyzers.EngineCacheVersion))
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("analyzerVersionFor: got %q, want suffix %q (EngineCacheVersion=%d)",
			got, wantSuffix, analyzers.EngineCacheVersion)
	}
}

// decimal renders a small non-negative int as ASCII digits without
// pulling in strconv. Test-helper only.
func decimal(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
