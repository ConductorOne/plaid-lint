// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"math"
	"testing"
)

// TestParseGoMemSize covers the byte-suffix parser the
// --gomemlimit flag uses. Phase 1.6 Lever D.
func TestParseGoMemSize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1073741824", 1073741824, false},
		{"1", 1, false},
		{"0", 0, false},
		{"1B", 1, false},
		{"8KiB", 8 << 10, false},
		{"8KB", 8 << 10, false},
		{"8K", 8 << 10, false},
		{"16MiB", 16 << 20, false},
		{"16MB", 16 << 20, false},
		{"16M", 16 << 20, false},
		{"4GiB", 4 << 30, false},
		{"4GB", 4 << 30, false},
		{"4G", 4 << 30, false},
		{"32GiB", 32 << 30, false},
		{"10G", 10 << 30, false},
		{"1TiB", 1 << 40, false},
		{"1T", 1 << 40, false},
		// Whitespace tolerated.
		{"  8GiB  ", 8 << 30, false},
		// Errors.
		{"", 0, true},
		{"abc", 0, true},
		{"4.5GiB", 0, true},
		{"-1", 0, true},
		{"GiB", 0, true},
		{"1XX", 0, true},
		// Overflow guard.
		{"10000000T", 0, true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			got, err := parseGoMemSize(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseGoMemSize(%q) = %d, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGoMemSize(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("parseGoMemSize(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestParseGoMemSize_GoRuntimeAlignsWithUnits anchors the parser to
// the same multipliers debug.SetMemoryLimit accepts: KB/MB/GB are
// 1024-based, matching the runtime's GOMEMLIMIT env-var parser. If
// the Go runtime ever switches to SI-1000 these values change too,
// but as of Go 1.26 the parser must stay IEC-1024.
func TestParseGoMemSize_GoRuntimeAlignsWithUnits(t *testing.T) {
	t.Parallel()
	v, err := parseGoMemSize("1KB")
	if err != nil {
		t.Fatalf("parseGoMemSize(1KB): %v", err)
	}
	if v != 1024 {
		t.Fatalf("1KB = %d, want 1024 (IEC-1024 alignment with Go runtime)", v)
	}
	v, err = parseGoMemSize("1MB")
	if err != nil {
		t.Fatalf("parseGoMemSize(1MB): %v", err)
	}
	if v != 1024*1024 {
		t.Fatalf("1MB = %d, want %d", v, 1024*1024)
	}
}

// TestParseGoMemSize_Bounds confirms the overflow guard fires before
// the shift wraps int64.
func TestParseGoMemSize_Bounds(t *testing.T) {
	t.Parallel()
	// Largest representable: MaxInt64 ≈ 8 EiB. 8 EiB = 1 << 63 - 1.
	// 7 EiB shifted up should fit; 16 EiB shouldn't.
	v, err := parseGoMemSize("7E")
	if err == nil {
		// We don't expose "E" suffix. Should error.
		t.Fatalf("expected error for 7E, got %d", v)
	}
	// MaxInt64 plain integer is fine.
	v, err = parseGoMemSize("9223372036854775807")
	if err != nil {
		t.Fatalf("parseGoMemSize(MaxInt64): %v", err)
	}
	if v != math.MaxInt64 {
		t.Fatalf("MaxInt64 round-trip: got %d, want %d", v, int64(math.MaxInt64))
	}
}
