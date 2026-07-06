// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bench

// regenerate_test.go is a one-shot test that materialises the
// in-repo synthetic fixtures under internal/pipelinetest/testdata.
// It is gated by the PLAID_REGEN_FIXTURES env var so the normal
// `go test ./internal/bench/...` run does not rewrite the testdata
// tree.
//
// To regenerate the fixtures after editing one of the FixtureShape
// presets:
//
//   PLAID_REGEN_FIXTURES=1 go test -run TestRegenerateStaticFixtures \
//     ./internal/bench/...
//
// The regenerator runs from the repo's checkout root so the
// absolute target path is stable across machines (it uses the
// runtime.Caller-derived repo root rather than a hard-coded path).

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRegenerateStaticFixtures is the helper described above. It
// returns t.Skip when PLAID_REGEN_FIXTURES != "1" so CI never
// touches the testdata tree by accident.
func TestRegenerateStaticFixtures(t *testing.T) {
	if os.Getenv("PLAID_REGEN_FIXTURES") != "1" {
		t.Skip("set PLAID_REGEN_FIXTURES=1 to regenerate testdata")
	}
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// here = .../internal/bench/regenerate_test.go
	// target = .../internal/pipelinetest/testdata
	repoInternal := filepath.Dir(filepath.Dir(here)) // .../internal
	base := filepath.Join(repoInternal, "pipelinetest", "testdata")

	shapes := []FixtureShape{SmallShape, MediumShape, CascadeShape}
	for _, s := range shapes {
		dir := filepath.Join(base, s.Name)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove %s: %v", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		_, cf, err := GenerateFixture(dir, s)
		if err != nil {
			t.Fatalf("GenerateFixture %s: %v", s.Name, err)
		}
		t.Logf("generated %s (cascade=%s)", dir, cf)
	}
}
