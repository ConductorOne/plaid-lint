// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_TracePath_RoundTrip drives the production CLI with
// --trace-path and asserts the resulting file exists, is non-empty,
// and carries the runtime/trace magic header. The trace parser isn't
// vendored, so we identify the format by its on-disk magic
// (`go 1.<N> trace\x00\x00\x00`) rather than fully decoding it.
func TestRun_TracePath_RoundTrip(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	out := filepath.Join(t.TempDir(), "run.trace")
	_, _, stderr := runApp(t, dir, "run", "--trace-path", out)

	st, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat trace file: %v (stderr=%q)", err, stderr)
	}
	if st.Size() == 0 {
		t.Fatalf("trace file is empty (stderr=%q)", stderr)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	// Modern runtime/trace files start with "go 1.<N> trace" followed
	// by NUL padding. Tolerate any minor version so the test isn't
	// pinned to whatever Go we built with today.
	head := body
	if len(head) > 64 {
		head = head[:64]
	}
	if !bytes.HasPrefix(head, []byte("go 1.")) || !bytes.Contains(head, []byte(" trace")) {
		t.Fatalf("trace file missing magic header; first 64 bytes = %q", head)
	}
	if strings.Contains(stderr, "trace:") {
		t.Errorf("unexpected trace warning on stderr: %q", stderr)
	}
}

// TestRun_TracePath_DefaultOff confirms that omitting --trace-path
// produces no trace artifact and no trace-related warnings.
func TestRun_TracePath_DefaultOff(t *testing.T) {
	dir := fixtureRepo(t, `version: "2"
linters:
  default: none
  enable:
    - ineffassign
`)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tmp := t.TempDir()
	out := filepath.Join(tmp, "run.trace")
	_, _, stderr := runApp(t, dir, "run")

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("trace file should not exist when flag is unset; stat err=%v", err)
	}
	if strings.Contains(stderr, "trace") {
		t.Errorf("unexpected trace-related stderr: %q", stderr)
	}
}
