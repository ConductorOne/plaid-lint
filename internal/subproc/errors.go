// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"context"
	"errors"
	"fmt"
)

// Failure modes for subprocess invocation. The taxonomy is small on
// purpose: the engine only needs to distinguish "subprocess refused
// to run" (InvokeError) from "subprocess ran but I can't read its
// output" (ParseError) from "deadline tripped" (context.DeadlineExceeded
// wrapped via errors.Is). Anything else is wrapped as InvokeError so
// the same diagnostic path catches it.
//
//  - Timeout: ctx deadline trips. [Invoke] cancels the child, drains
//    whatever stderr it managed to emit, and returns an error that
//    satisfies errors.Is(err, context.DeadlineExceeded). ExitCode is
//    -1 because the process never exited cleanly.
//
//  - Non-zero exit: [Invoke] returns the captured stdout and stderr,
//    plus an *InvokeError whose ExitCode reflects the child. Some
//    real wrappers (unused, unparam) exit non-zero when they have
//    findings; the per-linter wrapper layer decides whether non-zero
//    means "error" or "findings present" by inspecting stdout first.
//
//  - Subprocess crash / signal: identical surface to non-zero exit.
//    Go's os/exec converts signals to a non-zero ExitCode (-1 for
//    "signaled, no exit code") via ExitCode() — we surface what the
//    standard library gives us. Callers can extract the underlying
//    *exec.ExitError via errors.As for signal-specific details if
//    they need them.
//
//  - Spawn failure (binary not found, EACCES, …): wrapped as
//    *InvokeError with ExitCode == -1 and an explanatory Err. The
//    caller distinguishes spawn vs run failure by whether ExitCode
//    is -1 AND Stderr is empty.

// InvokeError is returned by [Invoke] for any subprocess execution
// outcome that the engine should treat as a failure. ExitCode == -1
// is the sentinel for "the process never produced a clean exit"
// (spawn failure, signal, ctx cancellation).
type InvokeError struct {
	// Name is the subprocess binary name as passed to [Invoke].
	Name string

	// ExitCode is the OS exit code, or -1 if the child never exited
	// cleanly (spawn failure, signal, ctx-cancellation).
	ExitCode int

	// Stderr is the captured stderr output (may be empty).
	Stderr []byte

	// Err is the underlying error from os/exec or context. errors.Is
	// is forwarded so callers can pattern-match
	// context.DeadlineExceeded / context.Canceled cleanly.
	Err error
}

// Error implements error.
func (e *InvokeError) Error() string {
	if e == nil {
		return "<nil InvokeError>"
	}
	switch {
	case e.ExitCode >= 0:
		return fmt.Sprintf("subproc %s: exit %d: %v", e.Name, e.ExitCode, e.Err)
	case e.Err != nil:
		return fmt.Sprintf("subproc %s: %v", e.Name, e.Err)
	default:
		return fmt.Sprintf("subproc %s: failed (no exit code)", e.Name)
	}
}

// Unwrap supports errors.Is / errors.As traversal into the embedded
// error (notably context.DeadlineExceeded for timeout matching).
func (e *InvokeError) Unwrap() error { return e.Err }

// IsTimeout reports whether the error chain contains a deadline /
// cancellation marker. Equivalent to errors.Is(err, context.DeadlineExceeded)
// || errors.Is(err, context.Canceled), exposed as a helper so
// wrappers don't need to import the context package solely for
// classification.
func IsTimeout(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// ParseError is returned when a wrapper's diagnostic mapper cannot
// decode the subprocess's stdout. ParseError is recoverable in the
// sense that plaid-lint itself doesn't crash; the engine surfaces
// it as a hard error on the offending linter and continues with the
// other linters.
type ParseError struct {
	// Name is the subprocess binary name (for context in the
	// rendered message).
	Name string

	// Detail is a short human-readable explanation
	// ("malformed JSON at offset 42", "missing required field
	// `pos`", etc.).
	Detail string

	// Err is the underlying decoding error (typically a *json.SyntaxError
	// or an io error), preserved for errors.As.
	Err error
}

// Error implements error.
func (e *ParseError) Error() string {
	if e == nil {
		return "<nil ParseError>"
	}
	if e.Err != nil {
		return fmt.Sprintf("subproc %s: parse: %s: %v", e.Name, e.Detail, e.Err)
	}
	return fmt.Sprintf("subproc %s: parse: %s", e.Name, e.Detail)
}

// Unwrap supports errors.Is / errors.As traversal.
func (e *ParseError) Unwrap() error { return e.Err }
