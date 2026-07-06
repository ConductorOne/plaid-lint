// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"os"
	"testing"
)

// fakeBinary writes contents to a temp file with 0755 mode and
// returns its path. Used by Runner tests that need to verify
// linterVersion's binary-hash branch without invoking a real lint
// binary. Originated in unparam_test.go; retained for
// dupl_test.go.
func fakeBinary(t *testing.T, contents string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fake-bin-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.WriteString(contents); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Chmod(f.Name(), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return f.Name()
}
