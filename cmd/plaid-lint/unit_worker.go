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
	"runtime/debug"
	"runtime/metrics"
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
func (a *app) runUnitWorker(sess *unitSession, releaseHeapMB int) int {
	return unitWorkerLoop(os.Stdin, a.stdout, sess, newHeapReleaser(releaseHeapMB))
}

// defaultWorkerReleaseHeapMB is the --worker-release-heap-mb default.
// Most packages finish well under it, so they never pay for a release;
// the few large ones that cross it are the ones whose retained heap
// matters to the host.
const defaultWorkerReleaseHeapMB = 1024

// heapReleaser returns a persistent worker's heap to the OS after a
// request that grew it past a threshold.
//
// The Go runtime keeps freed heap pages mapped and hands them back
// only gradually, so a worker that has just analyzed a large package
// keeps that package's peak heap resident while it sits idle. Bazel
// schedules worker actions against a per-action memory reservation
// that it releases when the response arrives; the retained heap is
// invisible to that accounting, and several idle workers each
// holding a multi-gigabyte heap can exhaust the host. Releasing it
// right after the response costs one forced GC over a heap that is,
// at that point, almost entirely garbage.
type heapReleaser struct {
	thresholdBytes uint64
	retained       func() uint64 // heap bytes currently held from the OS
	free           func()        // returns as much memory to the OS as possible
}

// newHeapReleaser returns a releaser for thresholdMB, or nil (never
// release) when thresholdMB is not positive.
func newHeapReleaser(thresholdMB int) *heapReleaser {
	if thresholdMB <= 0 {
		return nil
	}
	return &heapReleaser{
		thresholdBytes: uint64(thresholdMB) << 20,
		retained:       retainedHeapBytes,
		free:           debug.FreeOSMemory,
	}
}

// afterResponse releases the heap when it exceeds the threshold and
// reports whether it did. It runs after the response is written, so
// the release never delays the action Bazel is waiting on.
func (r *heapReleaser) afterResponse() bool {
	if r == nil || r.retained() <= r.thresholdBytes {
		return false
	}
	r.free()
	return true
}

// retainedHeapBytes is the heap memory the runtime holds from the OS:
// live and not-yet-swept objects, fragmentation, and free pages not
// yet returned. Because freed pages stay retained until the scavenger
// or a release returns them, right after a request this is at least
// the request's heap high-water mark, less whatever the background
// scavenger already returned.
func retainedHeapBytes() uint64 {
	samples := []metrics.Sample{
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/heap/unused:bytes"},
		{Name: "/memory/classes/heap/free:bytes"},
	}
	metrics.Read(samples)
	var total uint64
	for _, s := range samples {
		if s.Value.Kind() == metrics.KindUint64 {
			total += s.Value.Uint64()
		}
	}
	return total
}

// unitWorkerLoop is the testable core of runUnitWorker. A nil release
// keeps every request's heap until the runtime returns it on its own.
func unitWorkerLoop(in io.Reader, out io.Writer, sess *unitSession, release *heapReleaser) int {
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
		release.afterResponse()
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
