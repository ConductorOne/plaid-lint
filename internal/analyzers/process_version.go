// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
)

// ProcessBinaryVersion returns a stable per-binary version string. Two
// different plaid-lint builds produce different values; one build
// across many runs produces the same value. The result is the W7
// replacement for the W6 sha256(analyzer.Name)[:8] stub.
//
// Resolution order:
//
//  1. Hash of the running executable file (os.Executable + sha256). The
//     authoritative source: any change to the binary — analyzer code,
//     fork edits, dependency bumps — produces a new hash.
//  2. Go module build info: VCS revision + main module version + Go
//     version. Used when os.Executable fails (e.g. test binaries on
//     some platforms).
//  3. Static fallback: the Go runtime version + GOOS + GOARCH. Stable
//     across runs but not across builds.
//
// The hash is the first 16 hex chars of sha256; full 64 chars are
// overkill for a cache-version field. The "clk-" prefix tags the
// source-of-truth so debug output is self-describing.
func ProcessBinaryVersion() string {
	processBinaryVersionOnce.Do(func() {
		processBinaryVersion = computeProcessBinaryVersion()
	})
	return processBinaryVersion
}

var (
	processBinaryVersionOnce sync.Once
	processBinaryVersion     string
)

func computeProcessBinaryVersion() string {
	if h, ok := hashOwnExecutable(); ok {
		return "clk-bin-" + h
	}
	if h, ok := hashBuildInfo(); ok {
		return "clk-bldinfo-" + h
	}
	return "clk-fallback-" + runtime.Version() + "-" + runtime.GOOS + "-" + runtime.GOARCH
}

func hashOwnExecutable() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", false
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], true
}

func hashBuildInfo() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%s\n%s\n",
		info.GoVersion, info.Main.Path, info.Main.Version, info.Main.Sum)
	for _, s := range info.Settings {
		fmt.Fprintf(h, "%s=%s\n", s.Key, s.Value)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16], true
}

// ResetProcessBinaryVersionForTest forces re-computation on next call.
// Test-only.
func ResetProcessBinaryVersionForTest() {
	processBinaryVersionOnce = sync.Once{}
	processBinaryVersion = ""
}
