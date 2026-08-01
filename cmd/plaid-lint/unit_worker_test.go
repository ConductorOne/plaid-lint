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
	if code := unitWorkerLoop(&in, &out); code != exitSuccess {
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
	if code := unitWorkerLoop(&in, &out); code != exitSuccess {
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
	if code := unitWorkerLoop(in, &out); code != exitInternalError {
		t.Fatalf("worker loop exit=%d want %d", code, exitInternalError)
	}
	if out.Len() != 0 {
		t.Errorf("worker answered a malformed request: %q", out.String())
	}
}
