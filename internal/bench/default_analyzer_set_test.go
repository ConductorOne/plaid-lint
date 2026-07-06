// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"strings"
	"testing"
)

// TestDefaultAnalyzerSet_IncludesW8Mass is the W10-fix regression test
// for the calibration bug: the W10 first-pass harness defaulted to the
// W7 root set only (8 analyzers), so every checked-in calibration JSON
// reported analyzer_count=8 even though every plaid-lint deployment
// runs the full W7+W8 root set (102 unique analyzers after dedup of
// SA1000).
//
// This test asserts the default set:
//
//   - Contains exactly 102 analyzers (7 non-SA W7 roots + 95 SA-*).
//   - Includes a representative W7 non-static root (printf).
//   - Includes representative NeedsIR SA-checks from the W8 mass-wire
//     (SA4017, SA5012).
//   - Includes a representative inspect-only SA-check (SA1006).
//   - Contains SA1000 exactly once (it is in both AllBundledAnalyzers
//     and AllStaticcheckSAAnalyzers; the union must dedup).
//
// On the pre-fix HEAD (defaultAnalyzerSet calling AllBundledAnalyzers
// directly) this test FAILS at the first assertion: len == 8, not 102,
// and SA4017/SA5012/SA1006 are absent.
func TestDefaultAnalyzerSet_IncludesW8Mass(t *testing.T) {
	const expectedCount = 102

	got := defaultAnalyzerSet()
	if len(got) != expectedCount {
		t.Errorf("defaultAnalyzerSet length = %d, want %d "+
			"(7 non-SA W7 roots + 95 SA-* checks, SA1000 deduped)",
			len(got), expectedCount)
	}

	names := make(map[string]int, len(got))
	for _, a := range got {
		if a == nil || a.Analyzer() == nil {
			t.Fatal("defaultAnalyzerSet returned a nil entry")
		}
		names[a.Analyzer().Name]++
	}

	mustHave := []string{
		"printf",  // W7 non-static root with FactClassObject
		"SA4017",  // W8 NeedsIR (consumes purity → buildir)
		"SA5012",  // W8 NeedsIR (consumes buildir directly)
		"SA1006",  // W8 inspect-only SA-check (no IR)
		"SA1000",  // present in both W7 + W8 sets; must appear exactly once
	}
	for _, name := range mustHave {
		if names[name] == 0 {
			t.Errorf("defaultAnalyzerSet missing %q", name)
		}
	}
	if names["SA1000"] != 1 {
		t.Errorf("SA1000 count = %d, want exactly 1 (dedup of W7+W8 union)",
			names["SA1000"])
	}

	// Sanity: at least 90 SA-* names (the SA-* mass-wire is the
	// load-bearing claim of this test; a regression that loses the
	// staticcheck table would still pass the count assertion if W7
	// somehow grew to 102 analyzers).
	var saCount int
	for n := range names {
		if strings.HasPrefix(n, "SA") {
			saCount++
		}
	}
	if saCount < 90 {
		t.Errorf("SA-* analyzer count = %d, want >= 90 (W8 mass-wire is 95)",
			saCount)
	}
}
