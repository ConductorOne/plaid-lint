// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// decodeWorkResponses decodes every JSON line the worker wrote.
func decodeWorkResponses(t *testing.T, out *bytes.Buffer) []workResponse {
	t.Helper()
	dec := json.NewDecoder(out)
	var resps []workResponse
	for {
		var r workResponse
		if err := dec.Decode(&r); err != nil {
			if err == io.EOF {
				return resps
			}
			t.Fatalf("decode worker response: %v", err)
		}
		resps = append(resps, r)
	}
}

// TestUnitWorker_SequentialRequests drives unitWorkerLoop with two
// back-to-back work requests (one "--cfg path" pair, one "--cfg=path"
// token) and requires two responses with matching requestIds, exit 0,
// and both actions' declared outputs on disk.
func TestUnitWorker_SequentialRequests(t *testing.T) {
	dir, pkgs := buildUnitFixture(t, unitCLIFixtureFiles)
	pkg := pkgs["example.com/unitcli/scratch"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeErrcheckGolangci(t, dir)
	cfg1, sarif1, facts1 := writeUnitCfg(t, pkg, golangci, nil)
	cfg2, sarif2, facts2 := writeUnitCfg(t, pkg, golangci, nil)

	var in bytes.Buffer
	fmt.Fprintf(&in, `{"arguments":["--cfg",%q],"requestId":1}`+"\n", cfg1)
	fmt.Fprintf(&in, `{"arguments":["--cfg=%s"],"requestId":2}`+"\n", cfg2)

	var out bytes.Buffer
	if code := unitWorkerLoop(&in, &out, &unitSession{}, nil); code != exitSuccess {
		t.Fatalf("worker loop exit=%d want %d (output=%q)", code, exitSuccess, out.String())
	}

	resps := decodeWorkResponses(t, &out)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2: %+v", len(resps), resps)
	}
	for i, want := range []int32{1, 2} {
		if resps[i].RequestID != want {
			t.Errorf("response %d requestId=%d want %d", i, resps[i].RequestID, want)
		}
		if resps[i].ExitCode != 0 {
			t.Errorf("response %d exitCode=%d output=%q", i, resps[i].ExitCode, resps[i].Output)
		}
	}
	for _, p := range []string{sarif1, facts1, sarif2, facts2} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("declared output missing after worker run: %v", err)
		}
	}
}

// TestUnitWorker_MissingCfgKeepsServing: a request without --cfg gets
// an exitCode-2 response on its own requestId, and the loop keeps
// serving subsequent requests instead of dying.
func TestUnitWorker_MissingCfgKeepsServing(t *testing.T) {
	dir, pkgs := buildUnitFixture(t, unitCLIFixtureFiles)
	pkg := pkgs["example.com/unitcli/scratch"]
	if pkg == nil {
		t.Fatalf("fixture package missing: %v", pkgs)
	}
	golangci := writeErrcheckGolangci(t, dir)
	cfgOK, sarifOK, _ := writeUnitCfg(t, pkg, golangci, nil)

	var in bytes.Buffer
	in.WriteString(`{"arguments":[],"requestId":7}` + "\n")
	fmt.Fprintf(&in, `{"arguments":["--cfg",%q],"requestId":8}`+"\n", cfgOK)

	var out bytes.Buffer
	if code := unitWorkerLoop(&in, &out, &unitSession{}, nil); code != exitSuccess {
		t.Fatalf("worker loop exit=%d want %d (output=%q)", code, exitSuccess, out.String())
	}

	resps := decodeWorkResponses(t, &out)
	if len(resps) != 2 {
		t.Fatalf("got %d responses, want 2: %+v", len(resps), resps)
	}
	if resps[0].RequestID != 7 || resps[0].ExitCode != exitCLIError {
		t.Errorf("bad-request response = %+v; want requestId 7 exitCode %d", resps[0], exitCLIError)
	}
	if !strings.Contains(resps[0].Output, "--cfg") {
		t.Errorf("bad-request output %q does not name the missing --cfg", resps[0].Output)
	}
	if resps[1].RequestID != 8 || resps[1].ExitCode != 0 {
		t.Errorf("follow-up response = %+v; want requestId 8 exitCode 0", resps[1])
	}
	if _, err := os.Stat(sarifOK); err != nil {
		t.Errorf("follow-up request's sarif output missing: %v", err)
	}
}

// TestUnitWorker_MalformedLine: a non-JSON request line has no
// requestId to answer, so the loop exits with the internal-error code
// (Bazel restarts the worker).
func TestUnitWorker_MalformedLine(t *testing.T) {
	in := strings.NewReader("{this is not json}\n")
	var out bytes.Buffer
	if code := unitWorkerLoop(in, &out, &unitSession{}, nil); code != exitInternalError {
		t.Fatalf("worker loop exit=%d want %d", code, exitInternalError)
	}
	if out.Len() != 0 {
		t.Errorf("worker answered a malformed request: %q", out.String())
	}
}

// TestUnitWorker_ReleasesHeapAboveThreshold: the worker returns its
// heap to the OS after a response only when the retained heap exceeds
// the threshold, and only once the response has been written.
func TestUnitWorker_ReleasesHeapAboveThreshold(t *testing.T) {
	const threshold = 1 << 30
	var in bytes.Buffer
	// Requests without --cfg are answered cheaply (exit 2) but still
	// go through the post-response release check.
	for id := 1; id <= 3; id++ {
		fmt.Fprintf(&in, `{"arguments":[],"requestId":%d}`+"\n", id)
	}
	retained := []uint64{threshold + 1, threshold, threshold / 4}
	var (
		out        bytes.Buffer
		calls      int
		freedAfter []int // response count already written at each release
	)
	release := &heapReleaser{
		thresholdBytes: threshold,
		retained: func() uint64 {
			v := retained[calls]
			calls++
			return v
		},
		free: func() {
			freedAfter = append(freedAfter, len(decodeWorkResponses(t, bytes.NewBuffer(out.Bytes()))))
		},
	}
	if code := unitWorkerLoop(&in, &out, &unitSession{}, release); code != exitSuccess {
		t.Fatalf("worker loop exit=%d want %d", code, exitSuccess)
	}
	if calls != 3 {
		t.Fatalf("retained heap checked %d times, want once per response (3)", calls)
	}
	// Only the first request's heap (threshold+1) exceeds the
	// threshold; exactly-at and below must not release.
	if len(freedAfter) != 1 || freedAfter[0] != 1 {
		t.Fatalf("releases happened after responses %v, want exactly one, after response 1", freedAfter)
	}
	if n := len(decodeWorkResponses(t, &out)); n != 3 {
		t.Fatalf("got %d responses, want 3", n)
	}
}

func TestNewHeapReleaser_DisabledAtZero(t *testing.T) {
	for _, mb := range []int{0, -1} {
		if r := newHeapReleaser(mb); r != nil {
			t.Errorf("newHeapReleaser(%d) = %+v, want nil (disabled)", mb, r)
		}
		// A nil releaser is a valid no-op.
		if newHeapReleaser(mb).afterResponse() {
			t.Errorf("disabled releaser (%d MB) reported a release", mb)
		}
	}
	if r := newHeapReleaser(defaultWorkerReleaseHeapMB); r == nil || r.thresholdBytes != defaultWorkerReleaseHeapMB<<20 {
		t.Errorf("newHeapReleaser(%d) = %+v, want a %d MiB threshold", defaultWorkerReleaseHeapMB, r, defaultWorkerReleaseHeapMB)
	}
}

// heapSink keeps the allocation in TestHeapReleaser_ReturnsHeapToOS
// reachable until the test drops it.
var heapSink [][]byte

// TestHeapReleaser_ReturnsHeapToOS exercises the real runtime path:
// after a burst of now-garbage allocations above the threshold the
// retained heap drops back down, and below the threshold nothing is
// released.
func TestHeapReleaser_ReturnsHeapToOS(t *testing.T) {
	const (
		thresholdMB = 64
		burstMB     = 256
	)
	r := newHeapReleaser(thresholdMB)
	r.afterResponse() // start from a released heap

	if retainedHeapBytes() > thresholdMB<<20 {
		t.Skipf("test process already retains %d MiB, above the %d MiB threshold", retainedHeapBytes()>>20, thresholdMB)
	}
	if r.afterResponse() {
		t.Fatal("released below the threshold")
	}

	for range burstMB {
		heapSink = append(heapSink, make([]byte, 1<<20))
	}
	heapSink = nil
	before := retainedHeapBytes()
	if before < burstMB<<20 {
		t.Fatalf("retained heap after a %d MiB burst = %d MiB, want at least the burst", burstMB, before>>20)
	}
	if !r.afterResponse() {
		t.Fatalf("no release with %d MiB retained over a %d MiB threshold", before>>20, thresholdMB)
	}
	if after := retainedHeapBytes(); after > thresholdMB<<20 {
		t.Fatalf("retained heap after release = %d MiB (was %d MiB), want <= %d MiB", after>>20, before>>20, thresholdMB)
	}
}
