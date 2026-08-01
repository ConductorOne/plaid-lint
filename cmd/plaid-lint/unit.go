// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/exclusion"
	"github.com/conductorone/plaid-lint/internal/registry"
	"github.com/conductorone/plaid-lint/internal/unit"
)

// runUnit executes the `plaid-lint unit` subcommand: one hermetic
// single-package analysis action driven entirely by a unit.json
// config (see internal/unit).
//
// Exit codes differ from `run` deliberately: findings are results
// (recorded in the SARIF output), never an exit code. 0 = analysis
// completed; exitCLIError (2) = bad flags; exitInternalError (3) =
// unusable inputs or an internal fault; exitConfigError (7) = invalid
// .golangci config.
func (a *app) runUnit(args []string) int {
	fs := newRunFlagSet("unit", a.stderr)
	g := bindGlobalFlags(fs)
	cfgPath := fs.String("cfg", "", "path to the unit.json action config (required)")
	workerMode := fs.Bool("worker", false, "run as a Bazel persistent worker (JSON protocol on stdin/stdout)")

	if err := fs.Parse(args); err != nil {
		return exitCLIError
	}
	if g.Help {
		printUnitHelp(a.stdout)
		return exitSuccess
	}
	if *workerMode {
		return a.runUnitWorker()
	}
	if *cfgPath == "" {
		fmt.Fprintln(a.stderr, "plaid-lint: unit: --cfg is required")
		return exitCLIError
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintf(a.stderr, "plaid-lint: unit: unexpected arguments %q\n", fs.Args())
		return exitCLIError
	}

	code, msgs := unitOnce(context.Background(), *cfgPath)
	for _, m := range msgs {
		fmt.Fprintf(a.stderr, "plaid-lint: %s\n", m)
	}
	return code
}

// unitOnce runs a single unit action. Returned messages go to stderr
// regardless of outcome; the exit code follows the unit contract.
//
// Findings never influence the exit code: the SARIF output is the
// findings channel, and a separate consumer (a Bazel validation
// action, a CI collector) decides what fails.
func unitOnce(ctx context.Context, cfgPath string) (int, []string) {
	var msgs []string

	ucfg, err := unit.LoadConfig(cfgPath)
	if err != nil {
		return exitInternalError, append(msgs, err.Error())
	}

	golangci, cfgWarnings, err := loadUnitGolangciConfig(ucfg)
	if err != nil {
		return exitConfigError, append(msgs, err.Error())
	}
	for _, w := range cfgWarnings {
		msgs = append(msgs, fmt.Sprintf("warning: %s: %s", w.Field, w.Message))
	}
	if errs := config.Validate(golangci); len(errs) > 0 {
		for _, e := range errs {
			msgs = append(msgs, fmt.Sprintf("config error: %v", e))
		}
		return exitConfigError, msgs
	}

	reg, regWarnings, err := registry.BuildFromConfig(golangci)
	if err != nil {
		return exitInternalError, append(msgs, err.Error())
	}
	for _, w := range regWarnings {
		msgs = append(msgs, fmt.Sprintf("warning: %s: %s", w.Field, w.Message))
	}
	if len(ucfg.Analysis.EnableOnly) > 0 {
		reg, err = reg.SelectAnalyzers(ucfg.Analysis.EnableOnly)
		if err != nil {
			return exitInternalError, append(msgs, err.Error())
		}
	}

	// The exclusion filter anchors path-relative rules at the process
	// working directory — the execroot under Bazel — matching how the
	// declared source paths are spelled.
	filter, err := exclusion.NewFilter(golangci, mustGetwd(), nil)
	if err != nil {
		return exitInternalError, append(msgs, fmt.Sprintf("exclusion filter: %v", err))
	}

	res, err := unit.Run(ctx, ucfg, golangci, reg, filter)
	if err != nil {
		return exitInternalError, append(msgs, err.Error())
	}
	for _, w := range res.Warnings {
		msgs = append(msgs, "warning: "+w)
	}
	return exitSuccess, msgs
}

// loadUnitGolangciConfig loads the .golangci config named by the unit
// config, or plaid-lint defaults when none is named. Unlike `run`,
// there is NO directory discovery: unit actions declare every input,
// so an undeclared config file must not influence the result.
func loadUnitGolangciConfig(ucfg *unit.Config) (*config.Config, []config.Warning, error) {
	if ucfg.Analysis.Config == "" {
		return config.NewDefault(), nil, nil
	}
	return config.Load(ucfg.Analysis.Config)
}

// printUnitHelp writes the `unit` help text.
func printUnitHelp(w io.Writer) {
	fmt.Fprintln(w, `plaid-lint unit — analyze exactly one package from declared inputs.

Usage:
  plaid-lint unit --cfg unit.json
  plaid-lint unit --worker

The unit.json config names the package sources, an importcfg mapping
import paths to compiler export data, dependency fact files, the
.golangci config, and the output paths (SARIF diagnostics + a
.plaidfacts fact file). Nothing is discovered: no go list, no module
resolution, no Go toolchain.

Findings are written to the SARIF output and never affect the exit
code. Exit codes: 0 analysis completed (with or without findings),
2 bad flags, 3 unusable inputs or internal error, 7 invalid
.golangci config.

Flags:
      --cfg string   path to the unit.json action config
      --worker       run as a Bazel persistent worker: read one JSON
                     WorkRequest per line on stdin (arguments:
                     ["--cfg", <path>]), write one JSON WorkResponse
                     per line on stdout`)
}

// mustGetwd returns the working directory; the empty string on
// failure keeps the exclusion filter operating on absolute paths.
func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
