// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/token"
	"os"
	"sync"

	goversion "github.com/hashicorp/go-version"
	reviveconfig "github.com/mgechev/revive/config"
	revivelint "github.com/mgechev/revive/lint"
	reviverule "github.com/mgechev/revive/rule"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsRevive attaches the AnalyzerFn for revive — the second
// polymorphic-config dispatch (after polybatchA's depguard/exhaustive/
// forbidigo/gosec). revive predates the analysis framework: it owns its
// own `lint.Linter` engine, `lint.Config` shape, and `lint.Failure`
// shape. No `*analysis.Analyzer` is exported anywhere in
// github.com/mgechev/revive. The wiring inlines an
// `&analysis.Analyzer{Run: ...}` whose closure:
//
//  1. Translates ReviveSettings → lint.Config (direct field assignment;
//     no TOML round-trip — our YAML pipeline is yaml.v3 which produces
//     map[string]any for nested arguments, the shape revive's rules
//     expect — see landmine 28).
//  2. Materializes the rule set via reviveconfig.GetLintingRules. This
//     calls Configure(ruleConfig.Arguments) on each rule pointer in
//     revive's package-global `allRules` slice — race-class with the
//     other Flags.Set / package-global linters (landmine 4 family).
//  3. Constructs `lint.New(os.ReadFile, MaxOpenFiles)` and calls
//     Lint(packages, rules, conf), draining the returned channel and
//     translating each lint.Failure to pass.Report.
//
// Per-Build-once setup (config translation + rule materialization +
// linter construction) is gated by sync.Once captured by the closure;
// rebuilds get a fresh Once because each wire-time call yields a new
// closure (landmine 24).
//
// Translation pattern per ReviveSettings field:
//
//	MaxOpenFiles          → lint.New maxOpenFiles arg          (int)
//	Confidence            → lint.Config.Confidence             (float64, default 0.8)
//	Severity              → lint.Config.Severity               (lint.Severity, default "warning")
//	EnableAllRules        → lint.Config.EnableAllRules         (bool)
//	EnableDefaultRules    → lint.Config.EnableDefaultRules     (bool)
//	ErrorCode             → lint.Config.ErrorCode              (int)
//	WarningCode           → lint.Config.WarningCode            (int)
//	Rules                 → lint.Config.Rules                  ([]ReviveRule → map[string]RuleConfig)
//	Directives            → lint.Config.Directives             ([]ReviveDirective → map[string]DirectiveConfig)
//	Go                    → lint.Config.GoVersion              (string → *goversion.Version)
//
// `IgnoreGeneratedHeader` is hardcoded to false (matches golangci-lint's
// wrapper) — generated-file gating is owned by
// exclusions.generated at the engine level. Our ReviveSettings doesn't
// surface it; landmine 28 documents the parity decision.
func wireAnalyzerFnsRevive(c *catalog) {
	wireNativeFn(c, "revive", wireRevive)
}

// wireRevive builds the revive Analyzer closure. The expensive setup
// (config translation, rule materialization, Linter construction) runs
// once per Build via sync.Once; each pass invocation reuses the
// resolved rule slice and lint.Config.
func wireRevive(cfg any) []*analysis.Analyzer {
	var (
		once         sync.Once
		linter       revivelint.Linter
		lintingRules []revivelint.Rule
		conf         *revivelint.Config
		setupErr     error
	)

	setup := func() {
		s, _ := cfg.(*config.ReviveSettings)
		if s == nil {
			s = &config.ReviveSettings{}
		}
		conf, setupErr = translateReviveConfig(s)
		if setupErr != nil {
			return
		}
		lintingRules, setupErr = reviveconfig.GetLintingRules(conf, nil)
		if setupErr != nil {
			return
		}
		linter = revivelint.New(os.ReadFile, s.MaxOpenFiles)
	}

	a := &analysis.Analyzer{
		Name: "revive",
		Doc:  "Fast, configurable, extensible, flexible, and beautiful linter for Go. Drop-in replacement of golint.",
		Run: func(pass *analysis.Pass) (any, error) {
			once.Do(setup)
			if setupErr != nil {
				return nil, fmt.Errorf("revive setup: %w", setupErr)
			}
			filenames := reviveFilenames(pass)
			if len(filenames) == 0 {
				return nil, nil
			}
			failures, err := linter.Lint([][]string{filenames}, lintingRules, *conf)
			if err != nil {
				return nil, fmt.Errorf("revive lint: %w", err)
			}
			for failure := range failures {
				if failure.Confidence < conf.Confidence {
					continue
				}
				pass.Report(analysis.Diagnostic{
					Pos:     posFromPosition(pass, failure.Position.Start),
					Message: fmt.Sprintf("%s: %s", failure.RuleName, failure.Failure),
				})
			}
			return nil, nil
		},
	}
	return []*analysis.Analyzer{a}
}

// translateReviveConfig converts ReviveSettings → *lint.Config and
// applies revive's normalization (default rule set, severity
// propagation) without the TOML round-trip golangci-lint uses. yaml.v3
// already decodes nested map arguments as map[string]any (the shape
// revive's Configure methods expect), so direct field assignment works
// for every rule revive ships — see landmine 28 for the map[any]any
// fallback we still cover defensively.
//
// Defaults applied here mirror revive's `normalizeConfig`/`defaultConfig`:
//
//   - Confidence defaults to 0.8 (revive's defaultConfidence) when
//     unset, matching golangci-lint's behavior.
//   - Severity defaults to "warning" when unset.
//   - When neither Rules nor EnableAllRules nor EnableDefaultRules is
//     set, revive's `normalizeConfig` leaves Rules empty and zero
//     diagnostics emit — we surface that explicitly by enabling
//     revive's default rule set, matching the contract a user would
//     reasonably expect from `linters.enable: [revive]` (it's a
//     drop-in replacement for golint and should at least run golint's
//     historical defaults).
func translateReviveConfig(s *config.ReviveSettings) (*revivelint.Config, error) {
	conf := &revivelint.Config{
		// IgnoreGeneratedHeader is intentionally left false — matches
		// golangci-lint's wrapper. exclusions.generated owns the
		// generated-file gate at the engine level.
		IgnoreGeneratedHeader: false,
		Confidence:            s.Confidence,
		Severity:              revivelint.Severity(s.Severity),
		EnableAllRules:        s.EnableAllRules,
		EnableDefaultRules:    s.EnableDefaultRules,
		ErrorCode:             s.ErrorCode,
		WarningCode:           s.WarningCode,
	}

	if conf.Confidence == 0 {
		conf.Confidence = reviveDefaultConfidence
	}
	if conf.Severity == "" {
		conf.Severity = revivelint.SeverityWarning
	}

	// Rules: ReviveRule slice → map[string]RuleConfig. Argument list is
	// copied through; map elements get a defensive map[any]any →
	// map[string]any conversion (landmine 28).
	conf.Rules = make(revivelint.RulesConfig, len(s.Rules))
	for _, r := range s.Rules {
		if r.Name == "" {
			continue
		}
		rc := revivelint.RuleConfig{
			Arguments: normalizeReviveArguments(r.Arguments),
			Severity:  revivelint.Severity(r.Severity),
			Disabled:  r.Disabled,
			Exclude:   r.Exclude,
		}
		if err := rc.Initialize(); err != nil {
			return nil, fmt.Errorf("revive rule %q: %w", r.Name, err)
		}
		conf.Rules[r.Name] = rc
	}
	// Empty-settings shortcut: when no explicit Rules / EnableAllRules /
	// EnableDefaultRules is provided, opt into revive's default rule
	// set so `linters.enable: [revive]` runs the historical golint
	// defaults rather than producing zero diagnostics. Setting only the
	// flag is not enough — reviveconfig.GetLintingRules walks
	// conf.Rules to materialize the active set, so we have to
	// pre-populate the map (mirrors upstream normalizeConfig's
	// `addRules` behavior, which we can't call because it's
	// unexported).
	if len(s.Rules) == 0 && !s.EnableAllRules && !s.EnableDefaultRules {
		conf.EnableDefaultRules = true
	}

	if len(s.Directives) > 0 {
		conf.Directives = make(revivelint.DirectivesConfig, len(s.Directives))
		for _, d := range s.Directives {
			if d.Name == "" {
				continue
			}
			conf.Directives[d.Name] = revivelint.DirectiveConfig{
				Severity: revivelint.Severity(d.Severity),
			}
		}
	}

	if s.Go != "" {
		v, err := goversion.NewVersion(s.Go)
		if err != nil {
			return nil, fmt.Errorf("revive go-version %q: %w", s.Go, err)
		}
		conf.GoVersion = v
	}

	// Mirror revive's normalizeConfig: ensure Rules map non-nil, add the
	// default/all rule lists if requested, and propagate top-level
	// severity to each rule/directive that doesn't override it.
	reviveNormalize(conf)

	return conf, nil
}

const reviveDefaultConfidence = 0.8

// reviveNormalize mirrors github.com/mgechev/revive/config.normalizeConfig.
// We can't call the upstream helper because it's unexported, but the
// two pieces we need are small:
//
//  1. When EnableAllRules / EnableDefaultRules is set, add empty
//     RuleConfig{} entries to conf.Rules for every rule in revive's
//     allRules / defaultRules tables. reviveconfig.GetLintingRules
//     consumes conf.Rules to pick the active set, so a flag without
//     map population is a no-op.
//
//  2. Propagate the top-level Severity to every rule/directive that
//     doesn't carry an explicit per-entry override.
//
// The rule lists themselves (reviveDefaultRules / reviveAllRules) are
// mirrored from upstream config.go because both `defaultRules` and
// `allRules` are package-private there.
func reviveNormalize(conf *revivelint.Config) {
	if conf.Rules == nil {
		conf.Rules = revivelint.RulesConfig{}
	}
	addRules := func(rules []revivelint.Rule) {
		for _, r := range rules {
			name := r.Name()
			if _, ok := conf.Rules[name]; !ok {
				conf.Rules[name] = revivelint.RuleConfig{}
			}
		}
	}
	switch {
	case conf.EnableAllRules:
		addRules(reviveAllRules)
	case conf.EnableDefaultRules:
		addRules(reviveDefaultRules)
	}
	severity := conf.Severity
	if severity == "" {
		return
	}
	for k, v := range conf.Rules {
		if v.Severity == "" {
			v.Severity = severity
		}
		conf.Rules[k] = v
	}
	for k, v := range conf.Directives {
		if v.Severity == "" {
			v.Severity = severity
		}
		conf.Directives[k] = v
	}
}

// reviveDefaultRules mirrors github.com/mgechev/revive/config.defaultRules
// (the 23 golint-historical rules revive enables by default when
// `enableDefaultRules` is set or as part of the empty-settings
// shortcut). The list isn't exported from revive's config package; we
// mirror it here. Drift between minor versions is documented in
// landmine 30 — pin go.mod to revive@v1.15.0 and update this slice in
// the same PR when bumping.
var reviveDefaultRules = []revivelint.Rule{
	&reviverule.VarDeclarationsRule{},
	&reviverule.PackageCommentsRule{},
	&reviverule.DotImportsRule{},
	&reviverule.BlankImportsRule{},
	&reviverule.ExportedRule{},
	&reviverule.VarNamingRule{},
	&reviverule.IndentErrorFlowRule{},
	&reviverule.RangeRule{},
	&reviverule.ErrorfRule{},
	&reviverule.ErrorNamingRule{},
	&reviverule.ErrorStringsRule{},
	&reviverule.ReceiverNamingRule{},
	&reviverule.IncrementDecrementRule{},
	&reviverule.ErrorReturnRule{},
	&reviverule.UnexportedReturnRule{},
	&reviverule.TimeNamingRule{},
	&reviverule.ContextKeysType{},
	&reviverule.ContextAsArgumentRule{},
	&reviverule.EmptyBlockRule{},
	&reviverule.SuperfluousElseRule{},
	&reviverule.UnusedParamRule{},
	&reviverule.UnreachableCodeRule{},
	&reviverule.RedefinesBuiltinIDRule{},
}

// reviveAllRules mirrors github.com/mgechev/revive/config.allRules
// (default + opt-in). Used only when EnableAllRules is set. Kept in
// the same order as upstream so a future audit can `diff` the lists.
// Same drift-on-upgrade caveat as reviveDefaultRules (landmine 30).
var reviveAllRules = append([]revivelint.Rule{
	&reviverule.ArgumentsLimitRule{},
	&reviverule.CyclomaticRule{},
	&reviverule.FileHeaderRule{},
	&reviverule.ConfusingNamingRule{},
	&reviverule.GetReturnRule{},
	&reviverule.ModifiesParamRule{},
	&reviverule.ConfusingResultsRule{},
	&reviverule.DeepExitRule{},
	&reviverule.AddConstantRule{},
	&reviverule.FlagParamRule{},
	&reviverule.UnnecessaryStmtRule{},
	&reviverule.StructTagRule{},
	&reviverule.ModifiesValRecRule{},
	&reviverule.ConstantLogicalExprRule{},
	&reviverule.BoolLiteralRule{},
	&reviverule.ImportsBlocklistRule{},
	&reviverule.FunctionResultsLimitRule{},
	&reviverule.MaxPublicStructsRule{},
	&reviverule.RangeValInClosureRule{},
	&reviverule.RangeValAddress{},
	&reviverule.WaitGroupByValueRule{},
	&reviverule.AtomicRule{},
	&reviverule.EmptyLinesRule{},
	&reviverule.LineLengthLimitRule{},
	&reviverule.CallToGCRule{},
	&reviverule.DuplicatedImportsRule{},
	&reviverule.ImportShadowingRule{},
	&reviverule.BareReturnRule{},
	&reviverule.UnusedReceiverRule{},
	&reviverule.UnhandledErrorRule{},
	&reviverule.CognitiveComplexityRule{},
	&reviverule.StringOfIntRule{},
	&reviverule.StringFormatRule{},
	&reviverule.EarlyReturnRule{},
	&reviverule.UnconditionalRecursionRule{},
	&reviverule.IdenticalBranchesRule{},
	&reviverule.DeferRule{},
	&reviverule.UnexportedNamingRule{},
	&reviverule.FunctionLength{},
	&reviverule.NestedStructs{},
	&reviverule.UselessBreak{},
	&reviverule.UncheckedTypeAssertionRule{},
	&reviverule.TimeEqualRule{},
	&reviverule.TimeDateRule{},
	&reviverule.BannedCharsRule{},
	&reviverule.OptimizeOperandsOrderRule{},
	&reviverule.UseAnyRule{},
	&reviverule.DataRaceRule{},
	&reviverule.CommentSpacingsRule{},
	&reviverule.IfReturnRule{},
	&reviverule.RedundantImportAlias{},
	&reviverule.ImportAliasNamingRule{},
	&reviverule.EnforceMapStyleRule{},
	&reviverule.EnforceRepeatedArgTypeStyleRule{},
	&reviverule.EnforceSliceStyleRule{},
	&reviverule.MaxControlNestingRule{},
	&reviverule.CommentsDensityRule{},
	&reviverule.FileLengthLimitRule{},
	&reviverule.FilenameFormatRule{},
	&reviverule.RedundantBuildTagRule{},
	&reviverule.UseErrorsNewRule{},
	&reviverule.RedundantTestMainExitRule{},
	&reviverule.UnnecessaryFormatRule{},
	&reviverule.UseFmtPrintRule{},
	&reviverule.EnforceSwitchStyleRule{},
	&reviverule.IdenticalSwitchConditionsRule{},
	&reviverule.IdenticalIfElseIfConditionsRule{},
	&reviverule.IdenticalIfElseIfBranchesRule{},
	&reviverule.IdenticalSwitchBranchesRule{},
	&reviverule.UselessFallthroughRule{},
	&reviverule.PackageDirectoryMismatchRule{},
	&reviverule.UseWaitGroupGoRule{},
	&reviverule.UnsecureURLSchemeRule{},
	&reviverule.InefficientMapLookupRule{},
	&reviverule.ForbiddenCallInWgGoRule{},
	&reviverule.UnnecessaryIfRule{},
	&reviverule.EpochNamingRule{},
	&reviverule.UseSlicesSort{},
	&reviverule.PackageNamingRule{},
}, reviveDefaultRules...)

// normalizeReviveArguments returns a copy of args with any nested
// map[any]any (the yaml.v2 mapping shape) converted to map[string]any
// (the shape revive's rule Configure methods expect — see landmine 28).
// yaml.v3, used by plaid-lint's config loader, already produces
// map[string]any for nested maps, so this is a defensive pass that's a
// no-op for the common path; non-map and non-(map[any]any) elements
// pass through unchanged.
func normalizeReviveArguments(args []any) revivelint.Arguments {
	if len(args) == 0 {
		return nil
	}
	out := make(revivelint.Arguments, len(args))
	for i, v := range args {
		out[i] = normalizeReviveArgValue(v)
	}
	return out
}

func normalizeReviveArgValue(v any) any {
	switch m := v.(type) {
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			key, ok := k.(string)
			if !ok {
				key = fmt.Sprintf("%v", k)
			}
			out[key] = normalizeReviveArgValue(val)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = normalizeReviveArgValue(val)
		}
		return out
	case []any:
		out := make([]any, len(m))
		for i, e := range m {
			out[i] = normalizeReviveArgValue(e)
		}
		return out
	default:
		return v
	}
}

// reviveFilenames returns the file paths backing a pass's *ast.Files.
// Mirrors golangci-lint's `internal.GetGoFileNames` minus the goanalysis
// position helper (we resolve via the pass FileSet directly). Files
// that don't have a real on-disk filename — e.g. test packages with
// virtual files — are skipped: revive needs to os.ReadFile each entry,
// so a virtual file would produce a read error and abort the pass.
func reviveFilenames(pass *analysis.Pass) []string {
	if pass == nil || len(pass.Files) == 0 {
		return nil
	}
	out := make([]string, 0, len(pass.Files))
	for _, f := range pass.Files {
		if f == nil || f.Pos() == token.NoPos {
			continue
		}
		name := pass.Fset.Position(f.Pos()).Filename
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}
