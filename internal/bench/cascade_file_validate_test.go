// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/scheduler"
)

// TestHarness_CascadeFileMissing_FailsFast is the LEARN-FGL-006
// regression. Pre-fix, an unreachable --cascade-file argument was
// only detected inside applyCascadeEdit, after cold (and warm, if
// enabled) had already run — 7-15 minutes on the c1 fixture. The
// guard added to bench.Run must catch the missing path before any
// scenario starts.
//
// The test drives bench.Run with an absolute path that's guaranteed
// not to exist and asserts:
//
//   - Run returns an error in well under the time it would take to
//     run a single cold scenario.
//   - The error message includes both the original CascadeFile
//     argument (so the operator sees what they typed) and the
//     resolved path (so they see how it was interpreted).
//
// Wall budget: 5s. On SmallShape, pre-fix this test would wall on
// the cold scenario (a few seconds) and then fail in applyCascadeEdit
// — 5+ seconds. Post-fix the guard fires before cold runs and the
// whole test finishes in milliseconds.
func TestHarness_CascadeFileMissing_FailsFast(t *testing.T) {
	requireGo(t)
	dir := t.TempDir()
	if _, _, err := GenerateFixture(dir, SmallShape); err != nil {
		t.Fatalf("GenerateFixture: %v", err)
	}

	const missing = "/does/not/exist.go"

	cfg := Config{
		Fixture:           dir,
		FixtureShape:      SmallShape.Name,
		BudgetBytes:       512 * 1024 * 1024,
		MaxConcurrency:    2,
		ObservationSource: scheduler.SourceVmHWM,
		// Cascade is enabled (the default) so the guard runs. We
		// also skip warm to keep the pre-fix failure path
		// representative of the "wasted cold scenario" footgun
		// without amplifying it further.
		SkipWarm:    true,
		CascadeFile: missing,
	}

	// 5s wall budget. Pre-fix this is exceeded (cold scenario on
	// SmallShape takes a few seconds, then applyCascadeEdit errors).
	// Post-fix the os.Stat guard fires synchronously and Run returns
	// in milliseconds.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err := Run(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("Run returned no error; expected fail-fast on missing cascade-file %q", missing)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Run took %s to fail; expected < 5s (cold scenario should not have run). err=%v", elapsed, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, missing) {
		t.Errorf("error message %q does not contain the original CascadeFile argument %q", msg, missing)
	}
	if !strings.Contains(msg, "cascade-file") {
		t.Errorf("error message %q does not name the field (\"cascade-file\") whose resolution failed", msg)
	}
	if !strings.Contains(msg, "resolved") {
		t.Errorf("error message %q does not include the resolved-path context (\"resolved\")", msg)
	}
}
