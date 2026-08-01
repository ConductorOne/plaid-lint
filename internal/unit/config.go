// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package unit implements `plaid-lint unit`: hermetic single-package
// analysis driven entirely by declared inputs.
//
// The unit driver is the unitchecker/nogo-shaped counterpart to the
// engine's workspace driver. One invocation analyzes exactly one
// package: sources are named explicitly, dependency types come from
// compiler export data named by an importcfg file, dependency facts
// come from declared fact files, and the analyzer set comes from the
// standard .golangci.yml config. Nothing is discovered: no `go list`,
// no module resolution, no vendor tree, no network, no Go toolchain.
//
// This is the execution mode a build system (Bazel, a REAPI executor)
// invokes as an action. All caching is the caller's concern — the
// driver's job is to be a pure, deterministic function of its inputs.
package unit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SchemaVersion is the unit.json schema this binary reads. Bumped only
// with a release note; readers reject other versions loudly rather
// than guessing.
const SchemaVersion = 1

// Mode selects which analyzer subset a unit invocation runs.
type Mode string

const (
	// ModeFull runs the enabled analyzer set and emits diagnostics.
	ModeFull Mode = "full"

	// ModeFactsOnly runs only fact-producing analyzers (and their
	// Requires closure) and discards diagnostics. Used for packages
	// that are dependencies of the lint scope but excluded from it
	// (generated code, third-party), mirroring nogo's -facts_only.
	ModeFactsOnly Mode = "facts_only"

	// ModeModule runs only module-scoped analyzers (those that read
	// go.mod rather than package sources, e.g. gomoddirectives).
	// go_files may be empty; a synthetic package hosts the pass.
	ModeModule Mode = "module"
)

// Config is the parsed unit.json: the complete declared-input
// description of one analysis action.
type Config struct {
	Schema int `json:"schema"`

	Package  PackageConfig  `json:"package"`
	Deps     DepsConfig     `json:"deps"`
	Module   ModuleConfig   `json:"module"`
	Analysis AnalysisConfig `json:"analysis"`
	Out      OutConfig      `json:"out"`
}

// PackageConfig identifies the package under analysis and the build
// configuration its sources were compiled with.
type PackageConfig struct {
	// Path is the import path of the package (compiler package path).
	Path string `json:"path"`

	// GoFiles are the compiled Go sources, in compile order. Paths are
	// relative to the process working directory (the execroot under
	// Bazel) or absolute.
	GoFiles []string `json:"go_files"`

	// IgnoredFiles are Go sources excluded by build constraints.
	// Optional; surfaced to analyzers via analysis.Pass.IgnoredFiles.
	IgnoredFiles []string `json:"ignored_files,omitempty"`

	// GOOS / GOARCH describe the build target. GOARCH selects
	// types.SizesFor("gc", GOARCH).
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`

	// Tags are the build tags the sources were selected under.
	// Informational for v1 (file selection already happened at
	// the build layer).
	Tags []string `json:"tags,omitempty"`

	// GoVersion is the language version the package compiles under,
	// e.g. "1.26". Sets types.Config.GoVersion when non-empty.
	GoVersion string `json:"go_version,omitempty"`

	// TestFilter mirrors the rules_go compile builder's -testfilter
	// flag for go_test archives whose declared sources span both the
	// internal and external test package: "exclude" keeps files whose
	// package clause does not end in _test (the internal archive),
	// "only" keeps files whose package clause does (the external
	// archive), "" / "off" keeps everything.
	TestFilter string `json:"test_filter,omitempty"`
}

// DepsConfig names the dependency artifacts.
type DepsConfig struct {
	// Importcfg is the path to a compiler-style importcfg file whose
	// `packagefile <importpath>=<file>` lines name export data for
	// every import needed to type-check the package. Deep gc export
	// data covers transitive imports, so direct deps suffice.
	Importcfg string `json:"importcfg"`

	// Facts maps direct-dependency import paths to their .plaidfacts
	// files. Missing entries mean "no facts" (stdlib, unlinted
	// third-party) and are tolerated, like nogo.
	Facts map[string]string `json:"facts,omitempty"`

	// StdlibDir is an optional directory of compiled standard-library
	// packages laid out as <StdlibDir>/<goos>_<goarch>/<importpath>.a
	// (the shape rules_go's compiled stdlib tree and a classic
	// GOPATH/pkg both use). Import paths not named by the importcfg
	// are resolved here before failing. The directory is a declared
	// input — reading it is within the hermeticity contract.
	StdlibDir string `json:"stdlib_dir,omitempty"`
}

// ModuleConfig names module-level inputs.
type ModuleConfig struct {
	// GoMod is the path to the module's go.mod. Required for
	// ModeModule; optional otherwise (module-scoped analyzers are
	// skipped in ModeFull when absent). Also populates
	// analysis.Pass.Module.
	GoMod string `json:"go_mod,omitempty"`

	// Path is the module path. Populates analysis.Pass.Module.Path
	// when set; otherwise derived from go.mod when present.
	Path string `json:"path,omitempty"`
}

// AnalysisConfig selects the analyzer set.
type AnalysisConfig struct {
	// Config is the path to the .golangci.{yml,yaml,json} file. When
	// empty, plaid-lint's defaults apply.
	Config string `json:"config,omitempty"`

	// Mode selects the analyzer subset. Empty means ModeFull.
	Mode Mode `json:"mode,omitempty"`

	// EnableOnly restricts the run to the named linters (post-config
	// resolution), mirroring `run --enable-only`. Optional.
	EnableOnly []string `json:"enable_only,omitempty"`
}

// OutConfig names the declared outputs. Every named output is written
// on every run — including runs with findings or type errors — so a
// build system can rely on their existence.
type OutConfig struct {
	// Facts is the .plaidfacts output path. Required except in
	// ModeModule (where it is optional; module analyzers produce no
	// package facts, but the file is still written when named).
	Facts string `json:"facts,omitempty"`

	// Sarif is the SARIF 2.1.0 diagnostics output path. Required.
	Sarif string `json:"sarif"`
}

// LoadConfig reads and validates a unit.json.
func LoadConfig(path string) (*Config, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unit: read config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("unit: parse %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("unit: invalid %s: %w", path, err)
	}
	return &c, nil
}

// validate checks the structural invariants LoadConfig promises to
// callers of the driver.
func (c *Config) validate() error {
	if c.Schema != SchemaVersion {
		return fmt.Errorf("schema %d not supported (want %d)", c.Schema, SchemaVersion)
	}
	switch c.Analysis.Mode {
	case "", ModeFull, ModeFactsOnly, ModeModule:
	default:
		return fmt.Errorf("unknown analysis.mode %q", c.Analysis.Mode)
	}
	switch c.Package.TestFilter {
	case "", "off", "only", "exclude":
	default:
		return fmt.Errorf("unknown package.test_filter %q", c.Package.TestFilter)
	}
	mode := c.EffectiveMode()
	if mode == ModeModule {
		if c.Module.GoMod == "" {
			return fmt.Errorf("analysis.mode %q requires module.go_mod", ModeModule)
		}
	} else {
		if c.Package.Path == "" {
			return fmt.Errorf("package.path is required")
		}
		if len(c.Package.GoFiles) == 0 {
			return fmt.Errorf("package.go_files must name at least one file")
		}
	}
	if c.Package.GOARCH == "" && mode != ModeModule {
		return fmt.Errorf("package.goarch is required")
	}
	if c.Package.GOOS == "" && mode != ModeModule {
		return fmt.Errorf("package.goos is required")
	}
	if c.Out.Sarif == "" {
		return fmt.Errorf("out.sarif is required")
	}
	if c.Out.Facts == "" && mode != ModeModule {
		return fmt.Errorf("out.facts is required")
	}
	for _, f := range c.Package.GoFiles {
		if filepath.IsAbs(f) {
			continue
		}
		// Relative paths are resolved against the working directory;
		// nothing to validate beyond non-emptiness.
		if f == "" {
			return fmt.Errorf("package.go_files contains an empty path")
		}
	}
	return nil
}

// EffectiveMode returns the configured mode, defaulting to ModeFull.
func (c *Config) EffectiveMode() Mode {
	if c.Analysis.Mode == "" {
		return ModeFull
	}
	return c.Analysis.Mode
}
