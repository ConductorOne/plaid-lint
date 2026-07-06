// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Binary customplugin is a synthetic linter plugin for
// internal/subproc/custom_test.go — it implements the custom-plugin
// protocol contract so the CustomRunner can be exercised end-to-end
// without depending on any real plugin binary.
//
// CLI contract:
//
//   - Positional args include `./...` (ignored — discovery is faked).
//   - Optional `--<key>=<value>` flags from CustomLinterSettings.Settings.
//     Recognized keys: `--severity=error|warning|info`, `--file=PATH`,
//     `--start-line=N`. Unknown flags are accepted and ignored so the
//     plugin survives a settings-key future-extension.
//
// stdout: NDJSON, one customDiagLine per line.
// stderr: free-form debug output (a banner + one line per emitted diag).
//
// Behavior knobs (all via env vars, mirroring T3.1's fakelinter):
//
//	CUSTOMPLUGIN_DIAGS=N        number of diagnostics to emit. Default 0.
//	CUSTOMPLUGIN_FAIL=MODE      see switch below.
//	CUSTOMPLUGIN_RELATED=true   include the optional `related` field.
//
// CUSTOMPLUGIN_FAIL modes:
//
//	exit2         emit valid NDJSON, then exit 2 (hard error).
//	malformed     emit `{not json` on stdout, exit 1.
//	hang          sleep 30s (use ctx deadline to kill).
//	stderr-noise  emit valid NDJSON + heavy stderr output, exit 1.
//	missing-fields emit a JSON object missing required fields, exit 1.
//
// Exit semantics:
//
//	0  -> no findings (CUSTOMPLUGIN_DIAGS=0 and no FAIL mode).
//	1  -> findings present (CUSTOMPLUGIN_DIAGS>0).
//	2  -> hard error path (`exit2` mode).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type diagPoint struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

type diagLine struct {
	File     string      `json:"file"`
	Line     int         `json:"line"`
	Column   int         `json:"column"`
	Severity string      `json:"severity"`
	Message  string      `json:"message"`
	Code     string      `json:"code,omitempty"`
	Related  []diagPoint `json:"related,omitempty"`
}

func main() {
	// Use a custom FlagSet so unknown flags don't abort — protocol
	// extension is additive, and a plugin should tolerate keys it
	// doesn't recognize.
	fs := flag.NewFlagSet("customplugin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	severity := fs.String("severity", "warning", "diagnostic severity")
	file := fs.String("file", "pkg/foo/foo.go", "diagnostic file path (relative to module root)")
	startLine := fs.Int("start-line", 10, "starting line number for emitted diagnostics")
	// Parse but ignore unknown args. The protocol commits to
	// forward-compat: a future runner may pass new flags this plugin
	// hasn't been rebuilt for.
	parseLenient(fs, os.Args[1:])

	mode := os.Getenv("CUSTOMPLUGIN_FAIL")
	switch mode {
	case "hang":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "malformed":
		fmt.Fprint(os.Stdout, "{not json")
		os.Exit(1)
	case "missing-fields":
		// File present but line missing — should fail the
		// required-field check.
		fmt.Fprintln(os.Stdout, `{"file":"pkg/x.go","severity":"error","message":"bad"}`)
		os.Exit(1)
	case "stderr-noise":
		fmt.Fprintln(os.Stderr, "customplugin: lots of noise")
		fmt.Fprintln(os.Stderr, "customplugin: still more noise")
	}

	count := 0
	if v := os.Getenv("CUSTOMPLUGIN_DIAGS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad CUSTOMPLUGIN_DIAGS: %v\n", err)
			os.Exit(2)
		}
		count = n
	}

	includeRelated := strings.EqualFold(os.Getenv("CUSTOMPLUGIN_RELATED"), "true")

	enc := json.NewEncoder(os.Stdout)
	for i := 0; i < count; i++ {
		d := diagLine{
			File:     *file,
			Line:     *startLine + i,
			Column:   3,
			Severity: *severity,
			Message:  fmt.Sprintf("custom diagnostic %d", i),
			Code:     "X001",
		}
		if includeRelated {
			d.Related = []diagPoint{
				{File: *file, Line: *startLine + i + 100, Column: 1, Message: "see also"},
			}
		}
		if err := enc.Encode(d); err != nil {
			fmt.Fprintf(os.Stderr, "encode: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "customplugin: emitted diag %d\n", i)
	}

	if mode == "exit2" {
		os.Exit(2)
	}
	if count > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}

// parseLenient walks args and feeds only the recognized `--key=value`
// pairs into fs. Unknown keys are silently dropped so plugins survive
// settings-key extensions added to the protocol.
func parseLenient(fs *flag.FlagSet, args []string) {
	known := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { known[f.Name] = true })

	var keep []string
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		body := strings.TrimPrefix(a, "--")
		k := body
		if i := strings.Index(body, "="); i >= 0 {
			k = body[:i]
		}
		if known[k] {
			keep = append(keep, a)
		}
	}
	_ = fs.Parse(keep)
}
