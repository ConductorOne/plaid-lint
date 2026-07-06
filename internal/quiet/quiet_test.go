// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package quiet

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestInstall_SuppressesNoisyPrefixes(t *testing.T) {
	out, _ := captureStderr(t, func() {
		fmt.Fprintf(os.Stderr, "new node, remapping %d -> %d\n", 0, 1)
		fmt.Fprintf(os.Stderr, "deduplicating %d -> %d based on path %s\n", 2, 1, "foo")
		fmt.Fprintf(os.Stderr, "deduplicating %d -> %d based on position %s\n", 3, 1, "bar.go:5:5")
	})
	if out != "" {
		t.Errorf("expected noisy prefixes suppressed; got stderr=%q", out)
	}
}

func TestInstall_PreservesUnrelatedLines(t *testing.T) {
	out, _ := captureStderr(t, func() {
		fmt.Fprintln(os.Stderr, "plaid-lint: warning: something happened")
		fmt.Fprintln(os.Stderr, "real diagnostic")
		// Interleave noisy + non-noisy.
		fmt.Fprintf(os.Stderr, "new node, remapping %d -> %d\n", 1, 2)
		fmt.Fprintln(os.Stderr, "keep me")
	})
	for _, want := range []string{
		"plaid-lint: warning: something happened",
		"real diagnostic",
		"keep me",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in passthrough output; got %q", want, out)
		}
	}
	if strings.Contains(out, "new node, remapping") {
		t.Errorf("noisy line leaked: %q", out)
	}
}

func TestFromEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"debug", false},
		{"info", false},
		{"warn", true},
		{"WARN", true},
		{" warning ", true},
		{"error", true},
		{"trace", false},
	}
	for _, c := range cases {
		t.Run(c.val, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", c.val)
			if got := FromEnv(); got != c.want {
				t.Errorf("FromEnv() with LOG_LEVEL=%q = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

// captureStderr installs the quiet filter, runs fn, and returns the
// bytes that made it through to the real stderr (redirected via a
// pipe of our own that sits behind the filter).
func captureStderr(t *testing.T, fn func()) (string, error) {
	t.Helper()
	// Swap the real os.Stderr for a pipe we control, then install the
	// quiet filter on top so its forwarded output lands in our pipe.
	origStderr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = pw

	restore := Install()
	fn()
	restore()

	// Close our pipe writer so the reader sees EOF.
	_ = pw.Close()
	os.Stderr = origStderr

	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := pr.Read(buf)
		if n > 0 {
			sb.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	_ = pr.Close()
	return sb.String(), nil
}
