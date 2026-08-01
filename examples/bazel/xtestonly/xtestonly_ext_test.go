// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package xtestonly_test

import (
	"testing"

	"example.com/plaidexample/xtestonly"
)

func TestDouble(t *testing.T) {
	if xtestonly.Double(2) != 4 {
		t.Fatal("nope")
	}
}
