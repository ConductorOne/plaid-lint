// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"slices"

	"github.com/conductorone/plaid-lint/internal/config"
)

// propagateGoVersion writes cfg.Run.Go into the per-linter Go fields
// of every active linter whose Entry has HasGoVersion=true. The
// fields are:
//
//   - Govet.Go
//   - Revive.Go
//   - Gocritic.Go
//   - ParallelTest.Go
//   - Formatters.GoFumpt.LangVersion
//
// Propagation only fires when (a) the linter is in the active set
// (so we don't dirty config for a linter the user has disabled) and
// (b) the target field is empty (so an explicit per-linter Go
// override — possible via the engine's API, not via YAML — wins).
//
// If cfg.Run.Go is empty (the T2.1 intentional gap — no go.mod
// detection), this function is a no-op. Engine-side go-mod detection
// (a future track) populates Run.Go before [Build] runs.
//
// Mutates cfg in place; the new fields are visible to subsequent
// reads of cfg.
func propagateGoVersion(cfg *config.Config, active map[string]*Entry) {
	v := cfg.Run.Go
	if v == "" {
		return
	}
	if _, ok := active["govet"]; ok {
		if cfg.Linters.Settings.Govet.Go == "" {
			cfg.Linters.Settings.Govet.Go = v
		}
	}
	if _, ok := active["revive"]; ok {
		if cfg.Linters.Settings.Revive.Go == "" {
			cfg.Linters.Settings.Revive.Go = v
		}
	}
	if _, ok := active["gocritic"]; ok {
		if cfg.Linters.Settings.Gocritic.Go == "" {
			cfg.Linters.Settings.Gocritic.Go = v
		}
	}
	if _, ok := active["paralleltest"]; ok {
		if cfg.Linters.Settings.ParallelTest.Go == "" {
			cfg.Linters.Settings.ParallelTest.Go = v
		}
	}
	// gofumpt is a formatter, not a linter, so it doesn't appear in
	// `active`. The user opting in via formatters.enable means the
	// formatter pipeline runs; propagation still applies.
	if slices.Contains(cfg.Formatters.Enable, "gofumpt") {
		if cfg.Formatters.Settings.GoFumpt.LangVersion == "" {
			cfg.Formatters.Settings.GoFumpt.LangVersion = v
		}
	}
}

// consolidateStaticcheckChecks merges v1's gosimple.checks and
// stylecheck.checks into v2's staticcheck.checks. The Settings
// struct shape already has both legacy fields collapsed under
// [config.StaticCheckSettings] (the v2 schema), so this function
// is technically a defensive consolidation: if a downstream input
// path (e.g. a hand-crafted *config.Config from a programmatic
// caller, or a future "decode v1 without legacy migration" path)
// ever surfaces gosimple/stylecheck checks separately, we'd
// re-merge here.
//
// Today the consolidation runs once at Build time:
//
//  1. Read existing `Staticcheck.Checks`.
//  2. Walk for any `linters-settings.gosimple.checks` /
//     `linters-settings.stylecheck.checks` selectors. T2.1's legacy
//     migrator already folded the slices in, so this step is a
//     dedupe pass.
//  3. Preserve insertion order; honnef's selector evaluator cares
//     about negation ordering.
//
// Returns a single warning when the merge dropped duplicates, or
// nil when the input was already canonical.
func consolidateStaticcheckChecks(cfg *config.Config) *Warning {
	if cfg == nil {
		return nil
	}
	checks := cfg.Linters.Settings.Staticcheck.Checks
	if len(checks) == 0 {
		return nil
	}
	deduped := dedupePreserveOrder(checks)
	if len(deduped) == len(checks) {
		return nil
	}
	cfg.Linters.Settings.Staticcheck.Checks = deduped
	return &Warning{
		Field:   "linters.settings.staticcheck.checks",
		Message: "consolidated gosimple/stylecheck check selectors; dropped duplicates",
	}
}

// dedupePreserveOrder returns s with duplicates removed, keeping the
// first occurrence of each value.
func dedupePreserveOrder(s []string) []string {
	if len(s) <= 1 {
		return s
	}
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// perLinterSettings returns the typed settings sub-block for the
// named linter, suitable for embedding in [Resolved.Settings]. The
// return type is the typed Settings struct pointer (e.g.
// `*config.GovetSettings`); callers do a type switch on
// [Resolved.Name] to recover the concrete shape.
//
// Returns nil for linters with no settings block (the catalog rows
// where the Settings struct exists but the entry's needs don't
// touch it), and for ShapeFormatter / ShapeSubprocess entries.
//
// Custom plugins (`linters.settings.custom[name]`) return the
// untyped `any` shape of [config.CustomLinterSettings].
func perLinterSettings(cfg *config.Config, name string) any {
	if cfg == nil {
		return nil
	}
	s := &cfg.Linters.Settings
	switch name {
	case "asasalint":
		return &s.Asasalint
	case "bidichk":
		return &s.BiDiChk
	case "copyloopvar":
		return &s.CopyLoopVar
	case "cyclop":
		return &s.Cyclop
	case "decorder":
		return &s.Decorder
	case "depguard":
		return &s.Depguard
	case "dogsled":
		return &s.Dogsled
	case "dupl":
		return &s.Dupl
	case "dupword":
		return &s.DupWord
	case "embeddedstructfieldcheck":
		return &s.EmbeddedStructFieldCheck
	case "errcheck":
		return &s.Errcheck
	case "errchkjson":
		return &s.ErrChkJSON
	case "errorlint":
		return &s.ErrorLint
	case "exhaustive":
		return &s.Exhaustive
	case "exhaustruct":
		return &s.Exhaustruct
	case "fatcontext":
		return &s.Fatcontext
	case "forbidigo":
		return &s.Forbidigo
	case "funcorder":
		return &s.FuncOrder
	case "funlen":
		return &s.Funlen
	case "ginkgolinter":
		return &s.GinkgoLinter
	case "gochecksumtype":
		return &s.GoChecksumType
	case "gocognit":
		return &s.Gocognit
	case "goconst":
		return &s.Goconst
	case "gocritic":
		return &s.Gocritic
	case "gocyclo":
		return &s.Gocyclo
	case "godoclint":
		return &s.Godoclint
	case "godot":
		return &s.Godot
	case "godox":
		return &s.Godox
	case "goheader":
		return &s.Goheader
	case "gomoddirectives":
		return &s.GoModDirectives
	case "gomodguard":
		return &s.Gomodguard
	case "gosec":
		return &s.Gosec
	case "gosmopolitan":
		return &s.Gosmopolitan
	case "govet":
		return &s.Govet
	case "grouper":
		return &s.Grouper
	case "iface":
		return &s.Iface
	case "importas":
		return &s.ImportAs
	case "inamedparam":
		return &s.Inamedparam
	case "ineffassign":
		return &s.Ineffassign
	case "interfacebloat":
		return &s.InterfaceBloat
	case "iotamixing":
		return &s.IotaMixing
	case "ireturn":
		return &s.Ireturn
	case "lll":
		return &s.Lll
	case "loggercheck":
		return &s.LoggerCheck
	case "maintidx":
		return &s.MaintIdx
	case "makezero":
		return &s.Makezero
	case "misspell":
		return &s.Misspell
	case "mnd":
		return &s.Mnd
	case "modernize":
		return &s.Modernize
	case "musttag":
		return &s.MustTag
	case "nakedret":
		return &s.Nakedret
	case "nestif":
		return &s.Nestif
	case "nilnil":
		return &s.NilNil
	case "nlreturn":
		return &s.Nlreturn
	case "nolintlint":
		return &s.NoLintLint
	case "nonamedreturns":
		return &s.NoNamedReturns
	case "paralleltest":
		return &s.ParallelTest
	case "perfsprint":
		return &s.PerfSprint
	case "prealloc":
		return &s.Prealloc
	case "predeclared":
		return &s.Predeclared
	case "promlinter":
		return &s.Promlinter
	case "protogetter":
		return &s.ProtoGetter
	case "reassign":
		return &s.Reassign
	case "recvcheck":
		return &s.Recvcheck
	case "revive":
		return &s.Revive
	case "rowserrcheck":
		return &s.RowsErrCheck
	case "sloglint":
		return &s.SlogLint
	case "spancheck":
		return &s.Spancheck
	case "staticcheck":
		return &s.Staticcheck
	case "tagalign":
		return &s.TagAlign
	case "tagliatelle":
		return &s.Tagliatelle
	case "testifylint":
		return &s.Testifylint
	case "testpackage":
		return &s.Testpackage
	case "thelper":
		return &s.Thelper
	case "unconvert":
		return &s.Unconvert
	case "unparam":
		return &s.Unparam
	case "unqueryvet":
		return &s.Unqueryvet
	case "unused":
		return &s.Unused
	case "usestdlibvars":
		return &s.UseStdlibVars
	case "usetesting":
		return &s.UseTesting
	case "varnamelen":
		return &s.Varnamelen
	case "whitespace":
		return &s.Whitespace
	case "wrapcheck":
		return &s.Wrapcheck
	case "wsl":
		return &s.WSL
	case "wsl_v5":
		return &s.WSLv5
	}
	// Custom plugin?
	if v, ok := s.Custom[name]; ok {
		return v
	}
	return nil
}
