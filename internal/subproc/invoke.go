// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

// invokeInDirWithStdin is [invokeInDir] with a stdin reader. Used by
// wrap runners whose upstream CLI takes a file list on stdin (dupl
// `-files`, lll `--files`) — a single invocation suffices, dodging
// the argv-size chunking the positional-arg runners need.
func invokeInDirWithStdin(ctx context.Context, name, cwd string, env, args []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout = outBuf.Bytes()
	stderr = errBuf.Bytes()

	if runErr == nil {
		return stdout, stderr, 0, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout, stderr, -1, &InvokeError{
			Name:     name,
			ExitCode: -1,
			Stderr:   stderr,
			Err:      ctxErr,
		}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		return stdout, stderr, code, &InvokeError{
			Name:     name,
			ExitCode: code,
			Stderr:   stderr,
			Err:      exitErr,
		}
	}

	return stdout, stderr, -1, &InvokeError{
		Name:     name,
		ExitCode: -1,
		Stderr:   stderr,
		Err:      runErr,
	}
}

// Invoke spawns name with the given args and optional stdin, captures
// stdout and stderr separately, and returns the exit code. It honors
// ctx for cancellation: when ctx is cancelled or its deadline is
// reached, the child is killed (via os/exec's KillOnCancel default)
// and the returned err wraps ctx.Err() so callers can match
// context.DeadlineExceeded / context.Canceled with errors.Is.
//
// Return contract:
//
//   - Clean exit, status 0: stdout, stderr, 0, nil.
//
//   - Clean exit, non-zero status: stdout, stderr, exitCode,
//     *InvokeError{ExitCode: exitCode, Stderr: stderr, Err: *exec.ExitError}.
//     Both stdout and stderr remain populated; the caller chooses
//     whether to treat findings (in stdout) as authoritative.
//
//   - Spawn failure (binary not found, EACCES, …): nil, nil, -1,
//     *InvokeError{ExitCode: -1, Err: <underlying>}.
//
//   - ctx cancellation / deadline: whatever output drained before
//     cancellation, -1, *InvokeError{ExitCode: -1, Err: ctx.Err()}
//     (which satisfies errors.Is(err, context.DeadlineExceeded) /
//     context.Canceled as appropriate). Stderr is preserved as the
//     operator-facing debug surface.
//
// Stdin, if non-nil, is fully drained into the child's stdin before
// the child is allowed to produce its terminating exit; callers that
// want streaming should not pass stdin here.
func Invoke(ctx context.Context, name string, args []string, stdin io.Reader) (stdout, stderr []byte, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, name, args...)

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if stdin != nil {
		cmd.Stdin = stdin
	}

	runErr := cmd.Run()

	stdout = outBuf.Bytes()
	stderr = errBuf.Bytes()

	if runErr == nil {
		return stdout, stderr, 0, nil
	}

	// ctx-trip wins over any process exit code. CommandContext kills
	// the child on cancellation, which makes runErr present even
	// when the child ostensibly finished, so we check ctx first.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout, stderr, -1, &InvokeError{
			Name:     name,
			ExitCode: -1,
			Stderr:   stderr,
			Err:      ctxErr,
		}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		return stdout, stderr, code, &InvokeError{
			Name:     name,
			ExitCode: code,
			Stderr:   stderr,
			Err:      exitErr,
		}
	}

	// Spawn failure (PathError on lookup, EACCES, etc.). Stdout /
	// stderr are typically empty here, but pass through whatever
	// drained.
	return stdout, stderr, -1, &InvokeError{
		Name:     name,
		ExitCode: -1,
		Stderr:   stderr,
		Err:      runErr,
	}
}

// invokeInDir is [Invoke] with two extra knobs that every per-linter
// wrapper in this package needs but the stdlib-primitive [Invoke]
// deliberately omits: a working directory and an env extension. Used
// by [UnusedRunner] (staticcheck `./...` discovery), [UnparamRunner]
// (unparam `./...` discovery + `GOFLAGS=-tags=...`), and
// [CustomRunner] (custom plugins do their own discovery from
// `WorkspaceRef.ModuleRoot`).
//
// Capture, error taxonomy, and ctx-cancellation behavior mirror
// [Invoke] exactly so wrappers above this layer don't need to
// special-case either entry point.
//
//   - cwd: passed as cmd.Dir. Empty leaves cwd inherited from parent.
//   - env: appended to os.Environ() before assignment to cmd.Env.
//     Empty leaves the inherited environment unchanged.
func invokeInDir(ctx context.Context, name, cwd string, env, args []string) (stdout, stderr []byte, exitCode int, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	runErr := cmd.Run()
	stdout = outBuf.Bytes()
	stderr = errBuf.Bytes()

	if runErr == nil {
		return stdout, stderr, 0, nil
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return stdout, stderr, -1, &InvokeError{
			Name:     name,
			ExitCode: -1,
			Stderr:   stderr,
			Err:      ctxErr,
		}
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		code := exitErr.ExitCode()
		return stdout, stderr, code, &InvokeError{
			Name:     name,
			ExitCode: code,
			Stderr:   stderr,
			Err:      exitErr,
		}
	}

	return stdout, stderr, -1, &InvokeError{
		Name:     name,
		ExitCode: -1,
		Stderr:   stderr,
		Err:      runErr,
	}
}
