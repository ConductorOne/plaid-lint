// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"

	"github.com/conductorone/plaid-lint/internal/config"
)

// runConfig dispatches `plaid-lint config <subcmd>`.
//
// `path` prints the resolved config file path; `verify` is deferred to
// Phase 3 (needs a published JSON schema).
func (a *app) runConfig(args []string) int {
	if len(args) == 0 {
		printConfigHelp(a.stdout)
		return exitCLIError
	}
	switch args[0] {
	case "path":
		return a.runConfigPath(args[1:])
	case "verify":
		fmt.Fprintln(a.stderr, "plaid-lint config verify: not implemented in T2.4 (deferred to Phase 3 — needs JSON schema asset)")
		return exitDeferredEngine
	case "--help", "-h", "help":
		printConfigHelp(a.stdout)
		return exitSuccess
	default:
		fmt.Fprintf(a.stderr, "plaid-lint config: unknown subcommand %q\n", args[0])
		printConfigHelp(a.stdout)
		return exitCLIError
	}
}

func (a *app) runConfigPath(args []string) int {
	fs := newRunFlagSet("config path", a.stderr)
	g := bindGlobalFlags(fs)
	rf := &runFlags{setFlags: map[string]bool{}}
	fs.StringVar(&rf.ConfigPath, "config", "", "Read config from file path PATH")
	fs.StringVar(&rf.ConfigPath, "c", "", "alias for --config")
	fs.BoolVar(&rf.NoConfig, "no-config", false, "Don't read config file")
	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		fmt.Fprintln(a.stdout, `Print used configuration path.

Usage:
  plaid-lint config path [flags]

Flags:
  -c, --config PATH   Read config from file path PATH
      --no-config     Don't read config file`)
		return exitSuccess
	}

	_, _, path, err := loadConfig(rf)
	if err != nil {
		fmt.Fprintf(a.stderr, "plaid-lint: %v\n", err)
		return exitConfigError
	}
	if path == "" {
		fmt.Fprintln(a.stdout, "(no config file found)")
		return exitSuccess
	}
	fmt.Fprintln(a.stdout, path)
	// Touch config package to avoid unused-import-when-stubbed warnings.
	_ = config.NewDefault
	return exitSuccess
}

func printConfigHelp(w io.Writer) {
	fmt.Fprintln(w, `Configuration file information and verification.

Usage:
  plaid-lint config [command]

Available Commands:
  path      Print used configuration path.
  verify    Verify configuration against JSON schema. (deferred to Phase 3)

Global Flags:
      --color string   Use color when printing (default "auto")
  -v, --verbose        Verbose output
  -h, --help           Help for a command`)
}
