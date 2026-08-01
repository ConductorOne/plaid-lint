// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"fmt"
	"os"
	"regexp"

	gomoddirectivespass "github.com/ldez/gomoddirectives"
	"golang.org/x/mod/modfile"

	"github.com/conductorone/plaid-lint/internal/config"
	"github.com/conductorone/plaid-lint/internal/output"
	"github.com/conductorone/plaid-lint/internal/registry"
)

// moduleScopedLinters names the linters whose subject is go.mod rather
// than package sources. They run in ModeModule (one action per module,
// keyed on go.mod content) and are excluded from ModeFull/ModeFactsOnly:
// their registry wrappers locate go.mod by shelling out to `go env
// GOMOD` (ldez/gomoddirectives GetModuleFile), which violates the unit
// contract (no toolchain, no discovery) — so the unit driver routes
// them through a declared-input path instead.
var moduleScopedLinters = map[string]bool{
	"gomoddirectives": true,
}

// isModuleScoped reports whether the named linter is go.mod-scoped.
func isModuleScoped(name string) bool { return moduleScopedLinters[name] }

// runModuleMode executes the module-scoped linters against the
// declared go.mod, entirely from declared inputs.
//
// Unlike package modes, no analyzer graph runs: each module-scoped
// linter has a direct, pure entry point (gomoddirectives.AnalyzeFile)
// that the registry wrapper cannot use because upstream's
// analysis.Pass adapter rediscovers go.mod via the toolchain. The
// linter set still respects the config's enable/disable resolution:
// only linters the registry enabled produce diagnostics.
func runModuleMode(cfg *Config, golangci *config.Config, reg *registry.Registry) ([]output.Diagnostic, error) {
	enabled := map[string]bool{}
	for _, r := range reg.Enabled() {
		if isModuleScoped(r.Name) {
			enabled[r.Name] = true
		}
	}
	if len(enabled) == 0 {
		return nil, nil
	}

	body, err := os.ReadFile(cfg.Module.GoMod)
	if err != nil {
		return nil, fmt.Errorf("unit: read go.mod: %w", err)
	}
	// Parse (not ParseLax): module-directive linting wants the full
	// directive set, and a malformed go.mod should fail the action
	// loudly — the build system's go.mod is the same file the build
	// already parsed.
	mf, err := modfile.Parse(cfg.Module.GoMod, body, nil)
	if err != nil {
		return nil, fmt.Errorf("unit: parse %s: %w", cfg.Module.GoMod, err)
	}

	var diags []output.Diagnostic
	if enabled["gomoddirectives"] {
		opts := gomoddirectivesOptions(golangci)
		for _, r := range gomoddirectivespass.AnalyzeFile(mf, opts) {
			diags = append(diags, output.Diagnostic{
				Linter:   "gomoddirectives",
				Message:  r.Reason,
				Severity: output.SeverityError,
				Pos: output.Position{
					Filename: cfg.Module.GoMod,
					Line:     r.Start.Line,
					Column:   r.Start.Column,
				},
			})
		}
	}
	return diags, nil
}

// gomoddirectivesOptions maps the .golangci.yml settings block onto
// upstream options — the same mapping the registry wrapper applies
// (internal/registry/wire_analyzers_wrapbatch.go), kept in sync by
// TestGomoddirectivesOptionParity.
func gomoddirectivesOptions(golangci *config.Config) gomoddirectivespass.Options {
	var opts gomoddirectivespass.Options
	if golangci == nil {
		return opts
	}
	s := golangci.Linters.Settings.GoModDirectives
	opts.ReplaceAllowList = s.ReplaceAllowList
	opts.ReplaceAllowLocal = s.ReplaceLocal
	opts.ExcludeForbidden = s.ExcludeForbidden
	opts.RetractAllowNoExplanation = s.RetractAllowNoExplanation
	opts.ToolchainForbidden = s.ToolchainForbidden
	opts.ToolForbidden = s.ToolForbidden
	opts.GoDebugForbidden = s.GoDebugForbidden
	opts.CheckModulePath = s.CheckModulePath
	if s.ToolchainPattern != "" {
		if rx, err := regexp.Compile(s.ToolchainPattern); err == nil {
			opts.ToolchainPattern = rx
		}
	}
	if s.GoVersionPattern != "" {
		if rx, err := regexp.Compile(s.GoVersionPattern); err == nil {
			opts.GoVersionPattern = rx
		}
	}
	return opts
}
