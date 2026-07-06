// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
)

// runHelp dispatches `plaid-lint help [subcommand]`.
//
// With no argument, prints the top-level help. With one argument,
// dispatches to the named subcommand with --help.
func (a *app) runHelp(args []string) int {
	if len(args) == 0 {
		a.printTopHelp()
		return exitSuccess
	}
	switch args[0] {
	case "run":
		printRunHelp(a.stdout)
	case "linters":
		printLintersHelp(a.stdout)
	case "version":
		// Forward to the version subcommand's --help.
		return a.runVersion([]string{"--help"})
	case "cache":
		printCacheHelp(a.stdout, a.stderr)
	case "config":
		printConfigHelp(a.stdout)
	case "help":
		a.printTopHelp()
	default:
		fmt.Fprintf(a.stderr, "plaid-lint help: unknown subcommand %q\n", args[0])
		a.printTopHelp()
		return exitCLIError
	}
	return exitSuccess
}
