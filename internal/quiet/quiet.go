// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package quiet suppresses third-party debug-trace lines that escape to
// the process stderr from upstream dependencies we don't fork.
//
// The specific offender is `honnef.co/go/tools/unused/serialize.go`'s
// package-level `trace()` helper: it writes unconditionally to
// `os.Stderr` ("new node, remapping X -> Y", "deduplicating X -> Y
// based on path Z", "deduplicating X -> Y based on position Z") for
// every node the SerializedGraph visits during a U1000 pass. A cold
// bench trial on a medium workspace emits millions of these lines —
// gigabytes of noise.
//
// Install replaces `os.Stderr` with a pipe whose reader goroutine
// drops the known noise prefixes and forwards every other line to the
// real stderr. Diagnostics (which go to stdout) and plaid-lint's own
// warnings/errors (which use absolute prefixes like "plaid-lint:")
// are unaffected.
package quiet

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
)

// noisyPrefixes is the deny-list of upstream debug-trace lines we
// always strip in quiet mode. Each entry matches one of the three
// fmt.Fprintf format strings in `honnef.co/go/tools@v0.6.1`'s
// `unused/serialize.go:trace()`. Keep these in sync with that file's
// trace() call sites.
var noisyPrefixes = []string{
	"new node, remapping ",
	"deduplicating ",
}

// Install replaces os.Stderr with a pipe whose reader goroutine drops
// lines matching the noise deny-list and forwards everything else to
// the original stderr. Returns a Restore function the caller may defer
// to put the real os.Stderr back; production callers normally let it
// stay installed for the process lifetime.
//
// Calling Install twice replaces the previously installed filter; the
// returned Restore from a superseded call is a no-op.
func Install() (restore func()) {
	orig := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		// Pipe creation should never fail outside of fd exhaustion;
		// when it does, just leave stderr alone.
		return func() {}
	}
	os.Stderr = pw
	done := make(chan struct{})
	go filterLoop(pr, orig, done)
	return func() {
		_ = pw.Close()
		<-done
		os.Stderr = orig
	}
}

// FromEnv decides whether quiet mode is requested via the
// LOG_LEVEL=warn environment variable. Returns true when LOG_LEVEL is
// set to "warn" or "error" (case-insensitive); other values (including
// unset, "debug", "info") return false.
func FromEnv() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	return v == "warn" || v == "warning" || v == "error"
}

// filterLoop reads lines from r and forwards every non-noisy line to
// w. Closes done when r returns EOF (or the writer half closes).
func filterLoop(r io.ReadCloser, w io.Writer, done chan<- struct{}) {
	defer close(done)
	defer r.Close()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 && !isNoisy(line) {
			_, _ = w.Write(line)
		}
		if err != nil {
			return
		}
	}
}

// isNoisy reports whether line starts with any of the known third-
// party debug-trace prefixes. Trailing whitespace is irrelevant — we
// match on the leading bytes only.
func isNoisy(line []byte) bool {
	// honnef's trace() emits "<format>\n<empty-Println>\n", so we'll
	// see both lines that start with the noisy prefix AND empty
	// trailing newlines that should pass through silently anyway.
	if bytes.Equal(line, []byte("\n")) {
		// Standalone newline from upstream's Fprintln(os.Stderr) after
		// each Fprintf. Could be legitimate too, so we don't drop it.
		return false
	}
	for _, p := range noisyPrefixes {
		if bytes.HasPrefix(line, []byte(p)) {
			return true
		}
	}
	return false
}
