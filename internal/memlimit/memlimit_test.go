// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package memlimit

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime/debug"
	"strings"
	"testing"
)

// 64 GiB in bytes — the canonical cgroup ceiling on c1's worker nodes
// and the value the 75% headroom rationale was calibrated against.
const sixtyFourGiB uint64 = 64 * 1024 * 1024 * 1024

// 48 GiB in bytes — 75% of 64 GiB; the validated cascade-d-m1on
// shipping target.
const fortyEightGiB int64 = 48 * 1024 * 1024 * 1024

func TestParseV2Limit_Sane(t *testing.T) {
	got, ok := parseV2Limit([]byte("68719476736\n"))
	if !ok || got != sixtyFourGiB {
		t.Fatalf("parseV2Limit(64GiB) = (%d, %v); want (%d, true)", got, ok, sixtyFourGiB)
	}
}

func TestParseV2Limit_Max(t *testing.T) {
	if v, ok := parseV2Limit([]byte("max\n")); ok {
		t.Fatalf("parseV2Limit(\"max\") = (%d, true); want (_, false)", v)
	}
}

func TestParseV2Limit_Empty(t *testing.T) {
	if v, ok := parseV2Limit([]byte("")); ok {
		t.Fatalf("parseV2Limit(\"\") = (%d, true); want (_, false)", v)
	}
	if v, ok := parseV2Limit([]byte("\n")); ok {
		t.Fatalf("parseV2Limit(\"\\n\") = (%d, true); want (_, false)", v)
	}
}

func TestParseV2Limit_Garbage(t *testing.T) {
	for _, in := range []string{"not-a-number", "12abc", "0xff", "-1"} {
		if v, ok := parseV2Limit([]byte(in)); ok {
			t.Errorf("parseV2Limit(%q) = (%d, true); want (_, false)", in, v)
		}
	}
}

func TestParseV1Limit_Sane(t *testing.T) {
	got, ok := parseV1Limit([]byte("68719476736"))
	if !ok || got != sixtyFourGiB {
		t.Fatalf("parseV1Limit(64GiB) = (%d, %v); want (%d, true)", got, ok, sixtyFourGiB)
	}
}

func TestParseV1Limit_Sentinels(t *testing.T) {
	// Both kernel "no limit" sentinels must report unlimited.
	for _, in := range []string{
		"9223372036854775807", // int64 max — modern kernels
		"9223372036854771712", // old kernel sentinel (page-aligned)
	} {
		if v, ok := parseV1Limit([]byte(in)); ok {
			t.Errorf("parseV1Limit(%q) = (%d, true); want (_, false)", in, v)
		}
	}
}

func TestParseV1Limit_AboveSanityCeiling(t *testing.T) {
	// 512 GiB > 256 GiB sanityCeiling.
	in := []byte("549755813888")
	if v, ok := parseV1Limit(in); ok {
		t.Fatalf("parseV1Limit(512 GiB) = (%d, true); want (_, false)", v)
	}
}

func TestParseV1Limit_Garbage(t *testing.T) {
	if v, ok := parseV1Limit([]byte("nope")); ok {
		t.Fatalf("parseV1Limit(\"nope\") = (%d, true); want (_, false)", v)
	}
}

func TestSaneLimit_Zero(t *testing.T) {
	if v, ok := saneLimit(0); ok {
		t.Fatalf("saneLimit(0) = (%d, true); want (_, false)", v)
	}
}

func TestApply_NoOpWhenGOMEMLIMITSet(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "1GiB")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "")

	swapReadFile(t, func(string) ([]byte, error) {
		return []byte("68719476736\n"), nil
	})
	logged := swapLogf(t)

	before := debug.SetMemoryLimit(-1)
	Apply()
	after := debug.SetMemoryLimit(-1)

	if before != after {
		t.Errorf("Apply() changed limit despite GOMEMLIMIT set: before=%d after=%d", before, after)
	}
	if len(*logged) != 0 {
		t.Errorf("Apply() logged despite GOMEMLIMIT set: %v", *logged)
	}
}

func TestApply_NoOpWhenDisabled(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "1")

	swapReadFile(t, func(string) ([]byte, error) {
		return []byte("68719476736\n"), nil
	})
	logged := swapLogf(t)

	before := debug.SetMemoryLimit(-1)
	Apply()
	after := debug.SetMemoryLimit(-1)

	if before != after {
		t.Errorf("Apply() changed limit despite PLAID_DISABLE_AUTO_GOMEMLIMIT=1: before=%d after=%d", before, after)
	}
	if len(*logged) != 0 {
		t.Errorf("Apply() logged despite disabled: %v", *logged)
	}
}

func TestApply_NoOpWhenNoCgroup(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "")

	swapReadFile(t, func(string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: "x", Err: errors.New("no such file")}
	})
	logged := swapLogf(t)

	before := debug.SetMemoryLimit(-1)
	Apply()
	after := debug.SetMemoryLimit(-1)

	if before != after {
		t.Errorf("Apply() changed limit despite no detectable cgroup: before=%d after=%d", before, after)
	}
	if len(*logged) != 0 {
		t.Errorf("Apply() logged despite no detectable cgroup: %v", *logged)
	}
}

func TestApply_SetsLimitFromV2(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "")

	swapReadFile(t, func(path string) ([]byte, error) {
		if path == "/sys/fs/cgroup/memory.max" {
			return []byte("68719476736\n"), nil
		}
		return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("nope")}
	})
	logged := swapLogf(t)

	original := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(original)

	Apply()
	got := debug.SetMemoryLimit(-1)
	if got != fortyEightGiB {
		t.Errorf("Apply() set limit to %d; want %d (75%% of 64 GiB)", got, fortyEightGiB)
	}
	if len(*logged) != 1 || !strings.Contains((*logged)[0], "auto-set GOMEMLIMIT to 48.0 GiB") {
		t.Errorf("Apply() log line = %v; want one entry mentioning 48.0 GiB", *logged)
	}
}

func TestApply_FallsBackToV1(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "")

	swapReadFile(t, func(path string) ([]byte, error) {
		switch path {
		case "/sys/fs/cgroup/memory.max":
			return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("no v2")}
		case "/sys/fs/cgroup/memory/memory.limit_in_bytes":
			return []byte("68719476736"), nil
		}
		return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("nope")}
	})
	logged := swapLogf(t)

	original := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(original)

	Apply()
	got := debug.SetMemoryLimit(-1)
	if got != fortyEightGiB {
		t.Errorf("Apply() (v1 fallback) set limit to %d; want %d", got, fortyEightGiB)
	}
	if len(*logged) != 1 {
		t.Errorf("Apply() expected one log line; got %v", *logged)
	}
}

func TestApply_V2MaxFallsThroughToV1(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("PLAID_DISABLE_AUTO_GOMEMLIMIT", "")

	swapReadFile(t, func(path string) ([]byte, error) {
		switch path {
		case "/sys/fs/cgroup/memory.max":
			return []byte("max\n"), nil
		case "/sys/fs/cgroup/memory/memory.limit_in_bytes":
			return []byte("68719476736"), nil
		}
		return nil, errors.New("nope")
	})
	swapLogf(t)
	original := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(original)

	Apply()
	got := debug.SetMemoryLimit(-1)
	if got != fortyEightGiB {
		t.Errorf("Apply() with v2=max should fall through to v1; got limit=%d want=%d", got, fortyEightGiB)
	}
}

// swapReadFile replaces the package's readFile hook for the duration of
// t. The original is restored on t.Cleanup.
func swapReadFile(t *testing.T, fn func(string) ([]byte, error)) {
	t.Helper()
	orig := readFile
	readFile = fn
	t.Cleanup(func() { readFile = orig })
}

// swapLogf replaces the package's logf hook with a slice-backed sink
// for the duration of t. The returned pointer dereferences to the
// accumulated log lines.
func swapLogf(t *testing.T) *[]string {
	t.Helper()
	orig := logf
	var lines []string
	logf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { logf = orig })
	return &lines
}
