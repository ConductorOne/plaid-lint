// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pipelinetest

// l3_pin_transitive_prereqs_test.go is the W8 regression test for
// Codex's review finding: pinIRForAction must pin IR-consuming
// actions even when the action's analyzer has no registered
// AnalyzerDescriptor. Honnef intermediate prerequisites
// (fact_purity, fact_typedness, honnef's internal nilness) are
// pulled in transitively via SA-* Requires chains but are NOT in
// BundledRegistry. Without the walker-fallback their Run bodies
// consume pass.ResultOf[buildir] with zero pins recorded — the W9
// scheduler's free-after-fanin signal would undercount and a
// use-after-free becomes possible.
//
// The test drives Snapshot.Analyze with SA4017 only — SA4017
// requires both buildir AND honnef's fact_purity. It records every
// pin event with both the package ID AND the analyzer name, then
// asserts that fact_purity is in the recorded set.
//
// This test must FAIL on the pre-fix HEAD (pinIRForAction returned
// nil for fact_purity because its descriptor is nil) and PASS on
// the post-fix HEAD (the walker-fallback path detects fact_purity's
// transitive buildir requirement and pins).

import (
	"sort"
	"sync"
	"testing"

	"golang.org/x/tools/go/analysis"
	"honnef.co/go/tools/staticcheck"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	clcache "github.com/conductorone/plaid-lint/internal/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/settings"
	"github.com/conductorone/plaid-lint/internal/l3"
	"github.com/conductorone/plaid-lint/internal/workspace"
)

// pinEvent captures one (package, analyzer) pin observed during a
// recording-IRManager run. Used by the regression test to assert
// pin coverage of unregistered transitive prereqs.
type pinEvent struct {
	pkg      l3.PackageID
	analyzer string
}

// recordingIRManager is a test-only [l3.AnalyzerAwareIRManager] that
// records every pin event keyed by (pkg, analyzer). It implements
// the optional AnalyzerAwareIRManager extension so pinIRForAction
// can thread the analyzer name through. The base bookkeeping
// (release, package-keyed count) is delegated to an embedded
// SequentialIRManager — the unexported `release` method satisfies
// the IRManager contract through promotion, and Pin events
// captured by this wrapper still account for releases correctly
// because the returned *l3.Pin's Release calls back into the
// embedded manager. Concurrency-safe.
type recordingIRManager struct {
	*l3.SequentialIRManager
	mu     sync.Mutex
	events []pinEvent
}

func newRecordingIRManager() *recordingIRManager {
	return &recordingIRManager{SequentialIRManager: l3.NewSequentialIRManager()}
}

// Pin implements [l3.IRManager]. Records the event with an empty
// analyzer name and delegates the actual bookkeeping to the
// embedded SequentialIRManager.
func (m *recordingIRManager) Pin(pkg l3.PackageID) *l3.Pin {
	return m.recordAndPin(pkg, "")
}

// PinWithAnalyzer implements [l3.AnalyzerAwareIRManager].
func (m *recordingIRManager) PinWithAnalyzer(pkg l3.PackageID, name string) *l3.Pin {
	return m.recordAndPin(pkg, name)
}

func (m *recordingIRManager) recordAndPin(pkg l3.PackageID, name string) *l3.Pin {
	m.mu.Lock()
	m.events = append(m.events, pinEvent{pkg: pkg, analyzer: name})
	m.mu.Unlock()
	return m.SequentialIRManager.Pin(pkg)
}

// AnalyzerNames returns the deduplicated set of analyzer names
// observed across every recorded pin event, sorted lexically.
func (m *recordingIRManager) AnalyzerNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]bool, len(m.events))
	for _, e := range m.events {
		seen[e.analyzer] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// EventCount returns the total number of recorded pin events.
func (m *recordingIRManager) EventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// TestL3PinCoversUnregisteredTransitivePrereqs is the Codex-blocker
// regression test. With ONLY SA4017 enabled,
// drive the pipeline and assert that the recorded pin set includes
// fact_purity — an unregistered prerequisite that consumes
// pass.ResultOf[buildir] in its Run body.
//
// Pre-fix HEAD: pinIRForAction returns nil for fact_purity because
// act.descriptor() is nil, so the test fails.
// Post-fix HEAD: the walker-fallback path detects fact_purity's
// transitive buildir requirement and pins, so the test passes.
func TestL3PinCoversUnregisteredTransitivePrereqs(t *testing.T) {
	requireGo(t)
	// Resolve SA4017 from staticcheck.Analyzers.
	var sa4017 *analysis.Analyzer
	for _, sa := range staticcheck.Analyzers {
		if sa.Analyzer != nil && sa.Analyzer.Name == "SA4017" {
			sa4017 = sa.Analyzer
			break
		}
	}
	if sa4017 == nil {
		t.Skip("SA4017 not present in staticcheck.Analyzers @ pinned version")
	}

	// Sanity-check the BundledRegistry coverage: SA4017 must have a
	// descriptor (NeedsIR=true) and fact_purity must NOT have one —
	// the latter is the precondition for the test's load-bearing
	// assertion.
	if d := analyzers.BundledRegistry.Lookup(sa4017); d == nil {
		t.Fatalf("SA4017 has no descriptor in BundledRegistry; precondition violated")
	} else if !d.NeedsIR {
		t.Fatalf("SA4017 descriptor NeedsIR=false; precondition violated")
	}
	var factPurity *analysis.Analyzer
	for _, r := range sa4017.Requires {
		if r.Name == "fact_purity" {
			factPurity = r
			break
		}
	}
	if factPurity == nil {
		t.Skip("SA4017.Requires no longer includes fact_purity @ pinned staticcheck version")
	}
	if d := analyzers.BundledRegistry.Lookup(factPurity); d != nil {
		t.Skipf("fact_purity is now registered in BundledRegistry (descriptor=%v); the test's pre-condition (unregistered IR-consuming prereq) is no longer satisfied — pick a different intermediate or unregister this one", d)
	}
	// Confirm the walker would classify fact_purity as IR-consuming
	// — this is what the post-fix path keys off.
	if !analyzers.AnalyzerRequiresIR(factPurity) {
		t.Fatalf("AnalyzerRequiresIR(fact_purity) = false; walker can't detect the prereq, test setup is broken")
	}

	// Pin SA4017 (and only SA4017) as the root analyzer set.
	prev := settings.AllAnalyzers
	t.Cleanup(func() { settings.AllAnalyzers = prev })
	settings.AllAnalyzers = []*settings.Analyzer{settings.NewAnalyzer(sa4017)}

	t.Setenv("GOPLSCACHE", goplsCacheDir(t))
	modDir := t.TempDir()
	saBatchFixture(t, modDir)
	l1Dir := t.TempDir()
	l2Dir := t.TempDir()
	const toolVer = "plaid-lint-w8-transitive"

	runOnce := func(t *testing.T, mgr *recordingIRManager) map[string][]canonicalDiag {
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
		c.AttachL1(l1, toolVer)
		c.AttachL2(l2, "linux/arm64/cgo0", "go1.22", toolVer)
		c.AttachIRManager(mgr)
		ws := workspace.NewWithCache(modDir, c)
		defer ws.Close()
		return runAnalyzePipeline(t, ws)
	}

	// Cold run with the recording manager — every pin observed.
	coldMgr := newRecordingIRManager()
	cold := runOnce(t, coldMgr)
	t.Logf("cold: pins=%d analyzers=%v", coldMgr.EventCount(), coldMgr.AnalyzerNames())

	// Load-bearing assertion: fact_purity must appear in the pin set.
	got := coldMgr.AnalyzerNames()
	gotSet := make(map[string]bool, len(got))
	for _, n := range got {
		gotSet[n] = true
	}
	if !gotSet["fact_purity"] {
		t.Errorf("pin set does not include fact_purity (unregistered transitive prereq of SA4017)\n  got analyzers: %v\n  expected fact_purity to be pinned via walker-fallback", got)
	}
	if !gotSet["SA4017"] {
		t.Errorf("pin set does not include SA4017 (registered NeedsIR=true)\n  got analyzers: %v", got)
	}
	if !gotSet["buildir"] {
		t.Errorf("pin set does not include buildir (registered NeedsIR=true)\n  got analyzers: %v", got)
	}

	// Cumulative pin event count must be ≥ 3 (root + buildir +
	// fact_purity at minimum). The fixture has a single package,
	// so one pin per IR-consuming analyzer on that package.
	if coldMgr.EventCount() < 3 {
		t.Errorf("cold pin event count = %d, want >= 3 (SA4017 + buildir + fact_purity)", coldMgr.EventCount())
	}

	// Every pin must have been released — no leak.
	if leak := coldMgr.Snapshot(); len(leak) != 0 {
		t.Errorf("cold: pin leak %v (every pin must be released by end-of-Analyze)", leak)
	}

	// Cold→warm diagnostic equivalence must still hold (the W7
	// contract is unchanged by the fix). Drive a warm run with a
	// fresh recording manager but the same on-disk caches.
	warmMgr := newRecordingIRManager()
	warm := runOnce(t, warmMgr)
	t.Logf("warm: pins=%d analyzers=%v", warmMgr.EventCount(), warmMgr.AnalyzerNames())

	coldKey := canonicalDigest(cold)
	warmKey := canonicalDigest(warm)
	if coldKey != warmKey {
		t.Errorf("cold→warm diagnostics differ:\n  cold: %s\n  warm: %s", coldKey, warmKey)
	}
}
