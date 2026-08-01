// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package c1parity seeds one violation per C1 config class exercised
// by golangci-c1.yml (the C1-SHAPED fixture config): tracecheck,
// depguard, forbidigo, exhaustive, plus a nolint'd errcheck line that
// must stay suppressed and an unused-private pair whose test-only half
// must be superseded by the in-package test archive. e2e_c1parity.sh
// asserts exactly this surface through //c1parity:c1parity_lint.
package c1parity

import (
	"context"
	"fmt"
	"os"

	"example.com/plaidexample/c1parity/internal/forbidden"
)

// spanTracer is the minimal shape tracecheck's AST matcher keys on:
// a value whose Start method is called with two arguments, the second
// a string literal, on the right-hand side of an assignment (see
// internal/analyzers/tracecheck/testdata/src/trace).
type spanTracer struct{}

// Start mirrors the trace.Tracer.Start signature.
func (spanTracer) Start(ctx context.Context, name string) (context.Context, error) {
	_ = name
	return ctx, nil
}

// RecordCheckout starts a span whose name does not match the enclosing
// function name in snake_case: tracecheck must report that
// "checkout_span" should be "record_checkout" — proving the BUNDLED
// analyzer fired with no /linters/tracecheck.so anywhere.
func RecordCheckout(ctx context.Context) {
	_, _ = spanTracer{}.Start(ctx, "checkout_span")
}

// ForbiddenValue trips the depguard deny-forbidden rule through this
// package's import of internal/forbidden.
func ForbiddenValue() string {
	return forbidden.Value
}

// PrintBanner trips the forbidigo fmt\.Print.* pattern. Both results
// are consumed so forbidigo is the only finding on the line.
func PrintBanner() {
	_, _ = fmt.Println("c1parity banner")
}

// checkoutState is the enum for the exhaustive violation. Its
// constants are all referenced, so the only finding involving them is
// exhaustive's missing-case.
type checkoutState int

const (
	statePending checkoutState = iota
	stateActive
	stateDone
)

// DescribeState switches over checkoutState without covering stateDone
// and without a default clause (default-signifies-exhaustive is set):
// exhaustive must report the missing case.
func DescribeState(s checkoutState) string {
	switch s {
	case statePending:
		return "pending"
	case stateActive:
		return "active"
	}
	return "unknown"
}

// FinishState keeps stateDone referenced.
func FinishState() checkoutState { return stateDone }

// EnsureScratchDir carries the errcheck suppression that must HOLD
// under the suite: a specific, explained directive (nolintlint's
// require-specific + require-explanation are satisfied), so neither
// errcheck nor nolintlint may report this line.
func EnsureScratchDir() {
	os.Mkdir("c1parity-scratch", 0o750) //nolint:errcheck // parity fixture: the suppression must hold
}

// parityTestedOnly is referenced only by the in-package test: the test
// archive's run supersedes the library run's unused finding, so the
// suite must NOT report it.
func parityTestedOnly() int { return 7 }

// parityNeverUsed has no referent anywhere: the suite MUST report it.
func parityNeverUsed() int { return 8 }
