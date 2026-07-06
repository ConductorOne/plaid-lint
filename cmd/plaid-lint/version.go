// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"runtime/debug"
)

// versionInfo is the structured payload emitted by `version --json`.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// resolveVersion populates a versionInfo from the build-time globals
// plus runtime/debug.ReadBuildInfo for the version / commit / date
// fallbacks. The ldflags-set values win when present; ReadBuildInfo
// fills in the rest from the binary's embedded module + VCS metadata.
//
// This matters for `go install github.com/.../plaid-lint@<sha>`
// installs that don't pass -ldflags: the binary's module pseudo-version
// (e.g. v0.0.0-20260527125549-0eeffd7b9f9f) and the vcs.revision
// embedded by the Go toolchain give a meaningful answer instead of
// the "v0-dev" default sentinel.
func resolveVersion() versionInfo {
	v := versionInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}
	// Fall back to the embedded module pseudo-version when -ldflags
	// didn't pin Version. info.Main.Version is "(devel)" for `go run`
	// / in-tree builds and a v0.0.0-<timestamp>-<sha> pseudo-version
	// for `go install <pkg>@<rev>` installs — the case c1's Makefile
	// pin guard depends on.
	if (v.Version == "v0-dev" || v.Version == "") &&
		info.Main.Version != "" && info.Main.Version != "(devel)" {
		v.Version = info.Main.Version
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if v.Commit == "unknown" || v.Commit == "" {
				v.Commit = s.Value
			}
		case "vcs.time":
			if v.Date == "unknown" || v.Date == "" {
				v.Date = s.Value
			}
		}
	}
	return v
}

// runVersion executes the `plaid-lint version` subcommand.
func (a *app) runVersion(args []string) int {
	fs := newRunFlagSet("version", a.stderr)
	g := bindGlobalFlags(fs)
	var short, asJSON, asDebug bool
	fs.BoolVar(&short, "short", false, "Display only the version number")
	fs.BoolVar(&asJSON, "json", false, "Display as JSON")
	fs.BoolVar(&asDebug, "debug", false, "Add build information")

	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		fmt.Fprintln(a.stdout, `Display the plaid-lint version.

Usage:
  plaid-lint version [flags]

Flags:
      --debug   Add build information
      --json    Display as JSON
      --short   Display only the version number`)
		return exitSuccess
	}

	v := resolveVersion()
	switch {
	case asJSON:
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
			return exitInternalError
		}
	case short:
		fmt.Fprintln(a.stdout, v.Version)
	case asDebug:
		fmt.Fprintf(a.stdout, "plaid-lint version %s built with %s from %s on %s\n",
			v.Version, v.Go, v.Commit, v.Date)
		if info, ok := debug.ReadBuildInfo(); ok {
			fmt.Fprintln(a.stdout)
			fmt.Fprintln(a.stdout, info.String())
		}
	default:
		fmt.Fprintf(a.stdout, "plaid-lint has version %s built with %s from %s on %s\n",
			v.Version, v.Go, v.Commit, v.Date)
	}
	return exitSuccess
}
