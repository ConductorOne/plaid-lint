// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command plaid-lint is the production CLI for the plaid-lint
// linter engine. It is designed as a binary swap for `golangci-lint
// run`: the same subcommands, the same flags, and the same config
// file format (.golangci.{yml,yaml,json}).
//
// Subcommands:
//
//	plaid-lint run        (default) — lint the code.
//	plaid-lint linters    — list current linters configuration.
//	plaid-lint version    — display version.
//	plaid-lint cache      — cache control and information.
//	plaid-lint config     — configuration file information.
//	plaid-lint help       — display extra help.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is set at build time via -ldflags. The default value lets
// `plaid-lint version` work even in `go run` builds.
var (
	version = "v0-dev"
	commit  = "unknown"
	date    = "unknown"
)

// globalFlags is the set of flags accepted by every subcommand (and
// at the top level before any subcommand name).
type globalFlags struct {
	Color   string
	Verbose bool
	Quiet   bool
	Help    bool
}

// bindGlobalFlags attaches the common --color / -v / -h flags to fs.
func bindGlobalFlags(fs *flag.FlagSet) *globalFlags {
	g := &globalFlags{}
	fs.StringVar(&g.Color, "color", "auto", "Use color when printing; one of 'always', 'auto', or 'never'")
	fs.BoolVar(&g.Verbose, "verbose", false, "Verbose output")
	fs.BoolVar(&g.Verbose, "v", false, "alias for --verbose")
	fs.BoolVar(&g.Quiet, "quiet", true, "Suppress upstream debug-trace output on stderr (default true; pass --quiet=false to see honnef's 'new node, remapping' / 'deduplicating' lines)")
	fs.BoolVar(&g.Help, "help", false, "Help for a command")
	fs.BoolVar(&g.Help, "h", false, "alias for --help")
	return g
}

// app is the top-level dispatcher state. Exposed via app{} construction
// so tests can supply alternate stdout/stderr/argv.
type app struct {
	args   []string
	stdout io.Writer
	stderr io.Writer
}

// main is the production entry point. Tests invoke (&app{...}).run
// directly to capture output and avoid os.Exit.
func main() {
	a := &app{
		args:   os.Args[1:],
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
	os.Exit(a.run())
}

// exitCode is a typed exit code. Values match upstream golangci-lint
// where defined.
const (
	exitSuccess        = 0
	exitIssuesFound    = 1 // upstream's default --issues-exit-code
	exitCLIError       = 2 // CLI parse / dispatch errors
	exitConfigError    = 7 // config file load/validate errors
	exitInternalError  = 3 // unexpected runtime errors
	exitDeferredEngine = 5 // T2.4 stub: engine wiring deferred
)

// run dispatches the top-level command and returns the process exit
// code. Never calls os.Exit directly — that's main's job.
func (a *app) run() int {
	if len(a.args) == 0 {
		// No subcommand and no args: treat as `run` with defaults.
		return a.runRun(nil)
	}

	first := a.args[0]
	rest := a.args[1:]

	// Top-level help / version shortcuts.
	switch first {
	case "--help", "-h":
		a.printTopHelp()
		return exitSuccess
	case "--version":
		return a.runVersion(nil)
	}

	switch first {
	case "run":
		return a.runRun(rest)
	case "linters":
		return a.runLinters(rest)
	case "version":
		return a.runVersion(rest)
	case "cache":
		return a.runCache(rest)
	case "config":
		return a.runConfig(rest)
	case "help":
		return a.runHelp(rest)
	default:
		// Two cases: a flag (e.g. `plaid-lint --enable=foo ./...`)
		// or an unknown subcommand. Detect flags by leading '-'.
		if strings.HasPrefix(first, "-") {
			return a.runRun(a.args)
		}
		fmt.Fprintf(a.stderr, "plaid-lint: unknown subcommand %q\n", first)
		a.printTopHelp()
		return exitCLIError
	}
}

// printTopHelp writes the top-level help text to stdout.
func (a *app) printTopHelp() {
	fmt.Fprintln(a.stdout, `plaid-lint — fast, drop-in linter engine compatible with golangci-lint v2.

Usage:
  plaid-lint [command] [flags]
  plaid-lint run [flags]                lint the code (default subcommand)

Available Commands:
  run         Lint the code.
  linters     List current linters configuration.
  version     Display the plaid-lint version.
  cache       Cache control and information.
  config      Configuration file information.
  help        Display extra help.

Global Flags:
      --color string   Use color when printing; one of 'always', 'auto', 'never' (default "auto")
  -v, --verbose        Verbose output
      --quiet          Suppress upstream debug-trace output on stderr (default true; pass --quiet=false to opt back in)
  -h, --help           Help for a command

Use "plaid-lint [command] --help" for more information about a command.`)
}

// newRunFlagSet creates a clean flag.FlagSet for a subcommand with
// continue-on-error semantics so the dispatcher can render its own
// error messages instead of stdlib's "flag provided but not defined".
func newRunFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}
