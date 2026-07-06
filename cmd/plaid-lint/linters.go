// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// linterListEntry is the per-linter row emitted by `linters --json`.
type linterListEntry struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	IsFormatter bool   `json:"is_formatter,omitempty"`
}

// runLinters executes the `plaid-lint linters` subcommand.
func (a *app) runLinters(args []string) int {
	fs := newRunFlagSet("linters", a.stderr)
	g := bindGlobalFlags(fs)

	// `linters` reuses the linter-selection flags from `run` so the
	// listing reflects the same resolution logic.
	rf := &runFlags{setFlags: map[string]bool{}}
	fs.StringVar(&rf.ConfigPath, "config", "", "Read config from file path PATH")
	fs.StringVar(&rf.ConfigPath, "c", "", "alias for --config")
	fs.BoolVar(&rf.NoConfig, "no-config", false, "Don't read config file")
	fs.StringVar(&rf.Default, "default", "", "Default set of linters to enable")
	fs.Var(&rf.Disable, "disable", "Disable specific linter")
	fs.Var(&rf.Disable, "D", "alias for --disable")
	fs.Var(&rf.Enable, "enable", "Enable specific linter")
	fs.Var(&rf.Enable, "E", "alias for --enable")
	fs.Var(&rf.EnableOnly, "enable-only", "Override config to only run the specific linter(s)")
	fs.BoolVar(&rf.FastOnly, "fast-only", false, "Filter enabled linters to only fast linters")

	asJSON := false
	fs.BoolVar(&asJSON, "json", false, "Display as JSON")

	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		printLintersHelp(a.stdout)
		return exitSuccess
	}
	rf.recordSetFlags(fs)

	cfg, warnings, _, err := loadConfig(rf)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitConfigError
	}
	for _, w := range warnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: %s: %s\n", w.Field, w.Message)
	}
	cfg = rf.applyOverlay(cfg)
	if errs := config.Validate(cfg); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(a.stderr, "plaid-lint: config error: %v\n", e)
		}
		return exitConfigError
	}

	reg, regWarnings, err := registry.BuildFromConfig(cfg)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitInternalError
	}
	for _, w := range regWarnings {
		fmt.Fprintf(a.stderr, "plaid-lint: warning: %s: %s\n", w.Field, w.Message)
	}

	all := reg.All()
	if asJSON {
		entries := make([]linterListEntry, 0, len(all))
		for _, l := range all {
			entries = append(entries, linterListEntry{
				Name:        l.Name,
				Enabled:     l.Status == registry.StatusEnabled,
				IsFormatter: l.Shape == registry.ShapeFormatter,
			})
		}
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(entries); err != nil {
			fmt.Fprintf(a.stderr, "plaid-lint: encode json: %v\n", err)
			return exitInternalError
		}
		return exitSuccess
	}

	fmt.Fprintln(a.stdout, "Enabled by your configuration linters:")
	for _, l := range all {
		if l.Status == registry.StatusEnabled {
			suffix := ""
			if l.Shape == registry.ShapeFormatter {
				suffix = " (formatter)"
			}
			fmt.Fprintf(a.stdout, "  %s%s\n", l.Name, suffix)
		}
	}
	fmt.Fprintln(a.stdout)
	fmt.Fprintln(a.stdout, "Disabled by your configuration linters:")
	for _, l := range all {
		if l.Status != registry.StatusEnabled {
			fmt.Fprintf(a.stdout, "  %s\n", l.Name)
		}
	}
	return exitSuccess
}

func printLintersHelp(w io.Writer) {
	fmt.Fprintln(w, `List current linters configuration.

Usage:
  plaid-lint linters [flags]

Flags:
  -c, --config PATH       Read config from file path PATH
      --no-config         Don't read config file
      --default string    Default set of linters to enable (default "standard")
  -D, --disable strings   Disable specific linter (repeatable)
  -E, --enable strings    Enable specific linter (repeatable)
      --enable-only strings   Override config to only run the specific linter(s)
      --fast-only         Filter enabled linters to only fast linters
      --json              Display as JSON

Global Flags:
      --color string   Use color when printing (default "auto")
  -v, --verbose        Verbose output
  -h, --help           Help for a command`)
	_ = flag.CommandLine // suppress unused import in some test stripping
}
