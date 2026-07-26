// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
)

func TestContextWithRunTimeout(t *testing.T) {
	t.Run("zero is a no-op", func(t *testing.T) {
		parent, parentCancel := context.WithCancel(context.Background())
		defer parentCancel()
		ctx, cancel := contextWithRunTimeout(parent, 0)
		defer cancel()
		if ctx != parent {
			t.Errorf("zero timeout should return parent unchanged; got distinct ctx")
		}
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("zero timeout should leave parent deadline-less; got deadline")
		}
	})

	t.Run("positive value sets a deadline", func(t *testing.T) {
		ctx, cancel := contextWithRunTimeout(context.Background(), config.Duration(50*time.Millisecond))
		defer cancel()
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("expected deadline to be set")
		}
		if d := time.Until(deadline); d > 50*time.Millisecond || d < 0 {
			t.Errorf("deadline %v not within (0, 50ms]", d)
		}
	})

	t.Run("fires deadline after timeout", func(t *testing.T) {
		ctx, cancel := contextWithRunTimeout(context.Background(), config.Duration(20*time.Millisecond))
		defer cancel()
		select {
		case <-ctx.Done():
			if ctx.Err() != context.DeadlineExceeded {
				t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
			}
		case <-time.After(200 * time.Millisecond):
			t.Errorf("ctx.Done did not fire within 200ms; timeout not enforced")
		}
	})
}

func TestResolveConcurrency(t *testing.T) {
	tests := []struct {
		name     string
		in       int
		want     int
		wantWarn string
	}{
		{"zero is auto", 0, 0, ""},
		{"one", 1, 1, ""},
		{"positive is applied verbatim", 7, 7, ""},
		{"negative warns and falls back to auto", -1, 0, "run.concurrency: -1 is negative; treating as unset"},
		{"large negative warns", -64, 0, "run.concurrency: -64 is negative; treating as unset"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, warn := resolveConcurrency(tc.in)
			if got != tc.want {
				t.Errorf("resolveConcurrency(%d) = %d; want %d", tc.in, got, tc.want)
			}
			if warn != tc.wantWarn {
				t.Errorf("resolveConcurrency(%d) warning = %q; want %q", tc.in, warn, tc.wantWarn)
			}
		})
	}
}

// TestRun_ConcurrencyAppliesGOMAXPROCS is the end-to-end counterpart to
// TestResolveConcurrency: it drives the real `run` path in-process and
// asserts the Go runtime actually moved. This is the assertion that
// would have caught the flag being parsed and then dropped on the
// floor — resolveConcurrency alone can be correct while nothing calls
// it.
//
// runApp executes a.run() in the test process, so runtime.GOMAXPROCS(0)
// after the call observes exactly what the engine ran under. Each
// subtest restores the prior value; none of these may run in parallel,
// since GOMAXPROCS is process-global.
func TestRun_ConcurrencyAppliesGOMAXPROCS(t *testing.T) {
	restoreGOMAXPROCS := func(t *testing.T) int {
		t.Helper()
		prior := runtime.GOMAXPROCS(0)
		t.Cleanup(func() { runtime.GOMAXPROCS(prior) })
		return prior
	}

	t.Run("flag lowers GOMAXPROCS", func(t *testing.T) {
		restoreGOMAXPROCS(t)
		dir := fixtureModule(t)
		code, stdout, stderr := runApp(t, dir, "run", "--no-config", "-j", "1")
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if got := runtime.GOMAXPROCS(0); got != 1 {
			t.Errorf("runtime.GOMAXPROCS(0) after `run -j 1` = %d; want 1", got)
		}
	})

	t.Run("yaml run.concurrency takes effect", func(t *testing.T) {
		restoreGOMAXPROCS(t)
		dir := fixtureRepo(t, `version: "2"
run:
  concurrency: 2
linters:
  default: none
`)
		code, stdout, stderr := runApp(t, dir, "run")
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if got := runtime.GOMAXPROCS(0); got != 2 {
			t.Errorf("runtime.GOMAXPROCS(0) after `run` with run.concurrency: 2 = %d; want 2", got)
		}
	})

	t.Run("flag overrides yaml", func(t *testing.T) {
		restoreGOMAXPROCS(t)
		dir := fixtureRepo(t, `version: "2"
run:
  concurrency: 2
linters:
  default: none
`)
		code, stdout, stderr := runApp(t, dir, "run", "--concurrency=3")
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if got := runtime.GOMAXPROCS(0); got != 3 {
			t.Errorf("runtime.GOMAXPROCS(0) after `run --concurrency=3` = %d; want 3", got)
		}
	})

	t.Run("zero leaves the runtime default alone", func(t *testing.T) {
		prior := restoreGOMAXPROCS(t)
		dir := fixtureModule(t)
		code, stdout, stderr := runApp(t, dir, "run", "--no-config", "-j", "0")
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if got := runtime.GOMAXPROCS(0); got != prior {
			t.Errorf("runtime.GOMAXPROCS(0) after `run -j 0` = %d; want unchanged %d", got, prior)
		}
	})

	t.Run("negative warns and leaves the runtime default alone", func(t *testing.T) {
		prior := restoreGOMAXPROCS(t)
		dir := fixtureModule(t)
		code, stdout, stderr := runApp(t, dir, "run", "--no-config", "-j", "-2")
		if code != 0 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if !strings.Contains(stderr, "run.concurrency: -2 is negative; treating as unset") {
			t.Errorf("stderr missing the negative-concurrency warning: %q", stderr)
		}
		if got := runtime.GOMAXPROCS(0); got != prior {
			t.Errorf("runtime.GOMAXPROCS(0) after `run -j -2` = %d; want unchanged %d", got, prior)
		}
	})
}
