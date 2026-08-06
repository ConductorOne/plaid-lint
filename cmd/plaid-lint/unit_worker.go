// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Bazel persistent-worker JSON protocol (one JSON object per line;
// the shape mirrors blaze.worker.WorkRequest / WorkResponse from
// worker_protocol.proto, camelCase field names as Bazel's
// --experimental_worker_allow_json_protocol emits them).
//
// The worker is SERIAL by design: requests are handled one at a time
// in arrival order, and multiplex mode (concurrent requests with ids)
// is not advertised. This is a correctness requirement, not a
// simplification — registry.BuildFromConfig applies per-linter
// settings by mutating package-global analyzer FlagSets inside the
// wire closures, so two configs must never be active concurrently in
// one process. Requests within one Bazel invocation share a config,
// so the per-config state (parsed config, registry, filter) is
// memoized in a unitSession and rebuilt only when a request's config
// path, content digest, or enable_only set differs from the previous
// request's (see unitOnce).
//
// Worker SANDBOXING is unsupported by design: unit.json paths resolve
// against the worker's working directory, so a sandboxed request
// (non-empty sandboxDir) would silently read the wrong generation of
// inputs. rules_plaid never sets supports-worker-sandboxing; a
// request carrying sandboxDir is answered with an error rather than
// wrong-but-cached outputs.
type workRequest struct {
	Arguments  []string   `json:"arguments,omitempty"`
	Inputs     []workFile `json:"inputs,omitempty"`
	RequestID  int32      `json:"requestId,omitempty"`
	Cancel     bool       `json:"cancel,omitempty"`
	Verbosity  int32      `json:"verbosity,omitempty"`
	SandboxDir string     `json:"sandboxDir,omitempty"`
}

type workFile struct {
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
}

type workResponse struct {
	ExitCode     int32  `json:"exitCode"`
	Output       string `json:"output,omitempty"`
	RequestID    int32  `json:"requestId,omitempty"`
	WasCancelled bool   `json:"wasCancelled,omitempty"`
}

// runUnitWorker services unit requests over the persistent-worker
// protocol until stdin closes. Each request carries the same argv a
// one-shot invocation would: ["--cfg", "<path>"] (a single
// "--cfg=<path>" token is also accepted).
//
// The session is built once, from the startup flags: --cache-dir is a
// worker-lifetime setting, since a per-request cache directory would
// let two requests in one process disagree about where results live.
func (a *app) runUnitWorker(sess *unitSession) int {
	return unitWorkerLoop(os.Stdin, a.stdout, sess)
}

// unitWorkerLoop is the testable core of runUnitWorker.
func unitWorkerLoop(in io.Reader, out io.Writer, sess *unitSession) int {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	enc := json.NewEncoder(out)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req workRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// A malformed request line is a protocol failure; there
			// is no request id to answer, so exit and let Bazel
			// restart the worker.
			fmt.Fprintf(os.Stderr, "plaid-lint: unit worker: malformed request: %v\n", err)
			return exitInternalError
		}
		if req.Cancel {
			// Requests run synchronously, so by the time a cancel
			// arrives its target has already been answered. Ack per
			// protocol.
			_ = enc.Encode(workResponse{RequestID: req.RequestID, WasCancelled: true})
			continue
		}
		if req.SandboxDir != "" {
			_ = enc.Encode(workResponse{
				ExitCode:  int32(exitInternalError),
				RequestID: req.RequestID,
				Output:    "plaid-lint: unit worker: worker sandboxing (sandboxDir) is not supported; do not set supports-worker-sandboxing",
			})
			continue
		}

		cfgPath, err := unitCfgFromArgs(req.Arguments)
		var (
			code int
			msgs []string
		)
		if err != nil {
			code, msgs = exitCLIError, []string{err.Error()}
		} else {
			code, msgs = unitOnce(context.Background(), cfgPath, sess)
		}
		resp := workResponse{
			ExitCode:  int32(code),
			RequestID: req.RequestID,
			Output:    strings.Join(msgs, "\n"),
		}
		if err := enc.Encode(resp); err != nil {
			fmt.Fprintf(os.Stderr, "plaid-lint: unit worker: write response: %v\n", err)
			return exitInternalError
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "plaid-lint: unit worker: read: %v\n", err)
		return exitInternalError
	}
	return exitSuccess
}

// unitCfgFromArgs extracts the --cfg value from a work request's
// argument list. Bazel appends the contents of the params file (or
// the flag itself), so both "--cfg=path" and "--cfg path" appear.
func unitCfgFromArgs(args []string) (string, error) {
	for i := range args {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "--cfg="):
			return strings.TrimPrefix(arg, "--cfg="), nil
		case arg == "--cfg":
			if i+1 < len(args) {
				return args[i+1], nil
			}
			return "", fmt.Errorf("unit worker: --cfg missing value")
		}
	}
	return "", fmt.Errorf("unit worker: request has no --cfg argument (got %q)", args)
}
