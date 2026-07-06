// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Binary fakelinter is a stand-in for a real subprocess linter
// (unused, unparam, custom plugin) used by internal/subproc tests.
//
// Contract:
//
//   - Accepts `--module-root=PATH` (required for the happy path).
//
//   - Emits a JSON array of diagnostic objects on stdout:
//     [{"file": "...", "line": N, "column": N,
//     "severity": "error|warning|info", "message": "..."}]
//
//   - Emits diagnostic text on stderr to test stderr capture
//     (one line per emitted diagnostic).
//
//   - Failure modes selected via env vars (so the same binary
//     reuses across multiple test cases without arg gymnastics):
//
//     FAKELINTER_FAIL=exit2  -> emit valid JSON, then exit 2.
//     FAKELINTER_FAIL=crash  -> emit "boom" on stderr, exit 7.
//     FAKELINTER_FAIL=malformed -> emit "{not json" on stdout, exit 0.
//     FAKELINTER_FAIL=hang   -> sleep 30s then exit 0 (use ctx deadline).
//     FAKELINTER_FAIL=stdin  -> echo "stdin:<hash>" on stdout, exit 0.
//
//   - FAKELINTER_DIAGS=N controls how many diagnostics are emitted
//     in the happy path. Defaults to 1.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

type diag struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

func main() {
	moduleRoot := flag.String("module-root", "", "absolute path to the module root")
	flag.Parse()

	switch os.Getenv("FAKELINTER_FAIL") {
	case "crash":
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(7)
	case "malformed":
		fmt.Fprint(os.Stdout, "{not json")
		os.Exit(0)
	case "hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "stdin":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
			os.Exit(3)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(os.Stdout, "stdin:%s", hex.EncodeToString(sum[:]))
		os.Exit(0)
	}

	count := 1
	if v := os.Getenv("FAKELINTER_DIAGS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad FAKELINTER_DIAGS: %v\n", err)
			os.Exit(2)
		}
		count = n
	}

	diags := make([]diag, 0, count)
	for i := 0; i < count; i++ {
		diags = append(diags, diag{
			File:     "pkg/foo/foo.go",
			Line:     10 + i,
			Column:   3,
			Severity: severityFor(i),
			Message:  fmt.Sprintf("fake diagnostic %d (root=%s)", i, *moduleRoot),
		})
		fmt.Fprintf(os.Stderr, "fakelinter: emitted diag %d\n", i)
	}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(diags); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
		os.Exit(2)
	}

	if os.Getenv("FAKELINTER_FAIL") == "exit2" {
		os.Exit(2)
	}
}

func severityFor(i int) string {
	switch i % 3 {
	case 0:
		return "error"
	case 1:
		return "warning"
	default:
		return "info"
	}
}
