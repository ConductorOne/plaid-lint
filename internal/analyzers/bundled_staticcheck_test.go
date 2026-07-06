// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/analysis"

	"honnef.co/go/tools/staticcheck"
)

// TestStaticcheckAnalyzersRegistered asserts that every SA-* analyzer
// shipped by staticcheck.Analyzers has a descriptor in
// BundledRegistry. Pins the W8 mass-wire: no SA-* check is allowed
// to silently fall back to the descriptor-missing path.
func TestStaticcheckAnalyzersRegistered(t *testing.T) {
	missing := []string{}
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		if d := BundledRegistry.Lookup(a); d == nil {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("BundledRegistry missing descriptors for %d SA-* analyzers: %v",
			len(missing), missing)
	}
}

// TestStaticcheckAnalyzerCount pins the landed count so a future
// staticcheck upgrade that adds or removes checks surfaces here
// rather than as a silent skew. The W8 brief targets the 94
// remaining SA-* analyzers (95 total in honnef.co/go/tools v0.6.1
// minus SA1000 already shipped in W7).
func TestStaticcheckAnalyzerCount(t *testing.T) {
	saTotal := 0
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a != nil && strings.HasPrefix(a.Name, "SA") {
			saTotal++
		}
	}
	// The exact count at the pinned version. If you bumped the
	// honnef.co/go/tools dependency and got here, update the
	// constants and document the SA-* delta.
	const (
		wantTotal       = 95
		wantNewlyInW8   = 94 // SA1000 was wired in W7
	)
	if saTotal != wantTotal {
		t.Errorf("staticcheck SA-* analyzer count = %d, want %d (honnef.co/go/tools/staticcheck @ pinned go.sum)",
			saTotal, wantTotal)
	}
	newlyWired := 0
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		if a.Name == "SA1000" {
			continue
		}
		newlyWired++
	}
	if newlyWired != wantNewlyInW8 {
		t.Errorf("W8 newly-wired SA-* count = %d, want %d",
			newlyWired, wantNewlyInW8)
	}
}

// TestStaticcheckNeedsIRCounts pins the NeedsIR breakdown at the
// pinned staticcheck version. A future analyzer-set change that
// shifts the buildir-vs-inspect split surfaces here, forcing a
// deliberate update rather than silent drift.
//
// At honnef.co/go/tools v0.6.1: 50 NeedsIR=true, 45 NeedsIR=false,
// 95 total.
func TestStaticcheckNeedsIRCounts(t *testing.T) {
	yes, no := 0, 0
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		if requiresIR(a) {
			yes++
		} else {
			no++
		}
	}
	const (
		wantYes = 50
		wantNo  = 45
	)
	if yes != wantYes {
		t.Errorf("SA-* NeedsIR=true count = %d, want %d (update the expected counts)", yes, wantYes)
	}
	if no != wantNo {
		t.Errorf("SA-* NeedsIR=false count = %d, want %d (update the expected counts)", no, wantNo)
	}
}

// TestStaticcheckNeedsIRClassification asserts that every SA-*
// descriptor's NeedsIR field is set from a buildir-walk of its
// Requires chain. The contract: SA-* analyzers whose transitive
// Requires includes buildir must have NeedsIR=true (so the L3
// IRManager pins their package), and those that don't must have
// NeedsIR=false (so no unnecessary pin fires).
func TestStaticcheckNeedsIRClassification(t *testing.T) {
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		d := BundledRegistry.Lookup(a)
		if d == nil {
			continue // covered by TestStaticcheckAnalyzersRegistered
		}
		want := requiresIR(a)
		if d.NeedsIR != want {
			t.Errorf("SA-* %q: NeedsIR=%t, want %t (buildir-walk over Requires)",
				a.Name, d.NeedsIR, want)
		}
	}
}

// TestStaticcheckDescriptorFields rounds out the per-SA assertion:
// every SA-* descriptor must carry a non-nil ConfigSalt and a
// non-empty AnalyzerVersion (auto-filled by Register).
func TestStaticcheckDescriptorFields(t *testing.T) {
	for _, sa := range staticcheck.Analyzers {
		a := sa.Analyzer
		if a == nil || !strings.HasPrefix(a.Name, "SA") {
			continue
		}
		d := BundledRegistry.Lookup(a)
		if d == nil {
			continue // covered above
		}
		if d.ConfigSalt == nil {
			t.Errorf("SA-* %q: ConfigSalt is nil", a.Name)
		}
		if d.AnalyzerVersion == "" {
			t.Errorf("SA-* %q: AnalyzerVersion is empty", a.Name)
		}
	}
}

// TestRequiresIRDetectsBuildirNested asserts the requiresIR walker
// catches buildir reached via a multi-hop Requires chain. The fixture
// is a fake analyzer whose Requires[0] is itself a fake whose
// Requires[0] is buildir from sa9008 (a known buildir+inspect-using
// SA-*).
func TestRequiresIRDetectsBuildirNested(t *testing.T) {
	// Build a stub chain: top -> mid -> sa9008Buildir.
	var sa9008 *analysis.Analyzer
	for _, sa := range staticcheck.Analyzers {
		if sa.Analyzer != nil && sa.Analyzer.Name == "SA9008" {
			sa9008 = sa.Analyzer
			break
		}
	}
	if sa9008 == nil {
		t.Skip("SA9008 not present in staticcheck.Analyzers; skipping nested-walk smoke")
	}
	// SA9008's Requires already includes buildir directly; that's the
	// 1-hop case. Wrap it in another fake to test 2-hop.
	mid := &analysis.Analyzer{Name: "mid", Requires: []*analysis.Analyzer{sa9008}}
	top := &analysis.Analyzer{Name: "top", Requires: []*analysis.Analyzer{mid}}
	if !requiresIR(top) {
		t.Errorf("requiresIR(top) = false; expected true via 2-hop traversal to buildir")
	}
	if !requiresIR(mid) {
		t.Errorf("requiresIR(mid) = false; expected true via 1-hop traversal to buildir")
	}
	if !requiresIR(sa9008) {
		t.Errorf("requiresIR(SA9008) = false; expected true via direct buildir dep")
	}
}

// TestRequiresIRRejectsInspectOnly asserts the walker doesn't false-
// positive on inspect-only analyzers (e.g. printf, which Requires
// inspect but not buildir).
func TestRequiresIRRejectsInspectOnly(t *testing.T) {
	// Use SA1001 as the inspect-only fixture; per the W8 categorisation,
	// SA1001 Requires inspect.Analyzer and nothing else.
	var sa1001 *analysis.Analyzer
	for _, sa := range staticcheck.Analyzers {
		if sa.Analyzer != nil && sa.Analyzer.Name == "SA1001" {
			sa1001 = sa.Analyzer
			break
		}
	}
	if sa1001 == nil {
		t.Skip("SA1001 not present in staticcheck.Analyzers")
	}
	if requiresIR(sa1001) {
		t.Errorf("requiresIR(SA1001) = true; SA1001 should be inspect-only")
	}
}

// TestRequiresIRHandlesCycles guards against a future analyzer
// shape with a self-referential Requires (shouldn't happen in
// staticcheck but the walker must terminate either way).
func TestRequiresIRHandlesCycles(t *testing.T) {
	a := &analysis.Analyzer{Name: "selfreq"}
	a.Requires = []*analysis.Analyzer{a}
	if requiresIR(a) {
		t.Errorf("requiresIR(selfreq) = true; selfreq has no buildir dep")
	}
}
