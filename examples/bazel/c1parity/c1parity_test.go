// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package c1parity

import (
	"fmt"
	"testing"
)

// TestParityTestedOnly is parityTestedOnly's only referent: the
// internal test archive's lint run must supersede the library run's
// unused finding about it.
func TestParityTestedOnly(t *testing.T) {
	if parityTestedOnly() != 7 {
		t.Fatal("nope")
	}
}

// testOnlyBanner violates the forbidigo fmt.Print* rule, but the
// config's `path: _test\.go` exclusion rule suppresses forbidigo in
// test files — mirroring C1's real exclusions. The suite must NOT
// report it (path-scoped exclusion parity).
func testOnlyBanner() {
	fmt.Println("allowed in tests")
}

var _ = testOnlyBanner
