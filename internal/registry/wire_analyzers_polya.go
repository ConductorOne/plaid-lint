// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/token"
	"io"
	"log"
	"strconv"
	"strings"

	depguardpass "github.com/OpenPeeDeeP/depguard/v2"
	forbidigopass "github.com/ashanbrown/forbidigo/v2/pkg/analyzer"
	exhaustivepass "github.com/nishanths/exhaustive"
	gosecpass "github.com/securego/gosec/v2"
	gosecanalyzers "github.com/securego/gosec/v2/analyzers"
	gosecissue "github.com/securego/gosec/v2/issue"
	gosecrules "github.com/securego/gosec/v2/rules"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsPolyBatchA attaches AnalyzerFns for the first
// polymorphic-config batch. Four linters wired
// (depguard, exhaustive, forbidigo, gosec); nolintlint dropped because
// its lint logic lives only inside golangci-lint's internal package and
// has no external importable module.
//
// Each linter accepts structured user config — rule maps, polymorphic
// pattern lists, include/exclude rule-ID slices — that doesn't fit the
// simple Flags.Set / single-constructor-arg shape used by earlier
// batches. Per-linter translation summarized in the playbook section
// "Polymorphic-config batch A additions".
//
// Translation patterns:
//
//   - depguard: typed map[string]*DepGuardList → upstream
//     map[string]*depguard.List (rule name keyed). Deny is flipped from
//     []DepGuardDeny{pkg, desc} to map[string]string{pkg→desc} the
//     upstream shape uses. Fallible constructor (validates the
//     compiled rule set); nil-Analyzer on construction failure.
//
//   - exhaustive: package-global Analyzer with Flags.Var bound checks.
//     Flag-set translation only when the user provided a non-zero
//     setting (landmine 17 — most knobs default to false upstream, but
//     CheckGeneratedFlag defaults to false here too; we leave that
//     untouched and let exclusions.generated own that surface).
//
//   - forbidigo: pkg/analyzer.NewAnalyzer returns a fresh per-call
//     Analyzer whose -p flag accepts either bare regex strings or
//     YAML-encoded {p, pkg, msg} maps; we yaml.Marshal each
//     ForbidigoPattern and Flags.Set("p", ...) once per pattern.
//     Mirrors golangci-lint's adapter shape exactly.
//
//   - gosec: wrap-pattern (sub-shape 2 / []*packages.Package
//     reconstruction — same shape as gochecksumtype in wrapbatch). The
//     gosec library exports *gosec.Analyzer (NOT *analysis.Analyzer),
//     so the wiring inlines an &analysis.Analyzer{Run: ...} whose
//     closure synthesizes a *packages.Package from the pass and
//     translates gosec issues back to pass.Report. Rule include/exclude
//     filtering happens via gosec's RuleFilter/AnalyzerFilter slices.
func wireAnalyzerFnsPolyBatchA(c *catalog) {
	wireNativeFn(c, "depguard", wireDepguard)
	wireNativeFn(c, "exhaustive", wireExhaustive)
	wireNativeFn(c, "forbidigo", wireForbidigo)
	wireNativeFn(c, "gosec", wireGosec)
}

// wireDepguard translates DepGuardSettings to the upstream
// depguard.LinterSettings shape and calls NewAnalyzer. The settings
// type is a map[string]*List keyed by rule name; each List carries
// ListMode/Files/Allow plus a Deny map (pkg→description). Our
// DepGuardDeny is a []{Pkg, Desc} pair list — flip to upstream's
// map[string]string. Empty settings is a valid zero state; the
// resulting empty LinterSettings doesn't enforce any rule but the
// analyzer is still wired (the engine surfaces zero diagnostics).
func wireDepguard(cfg any) []*analysis.Analyzer {
	settings := depguardpass.LinterSettings{}
	if s, ok := cfg.(*config.DepGuardSettings); ok && s != nil {
		for name, rule := range s.Rules {
			if rule == nil {
				continue
			}
			deny := make(map[string]string, len(rule.Deny))
			for _, d := range rule.Deny {
				if d.Pkg == "" {
					continue
				}
				deny[d.Pkg] = d.Desc
			}
			settings[name] = &depguardpass.List{
				ListMode: rule.ListMode,
				Files:    rule.Files,
				Allow:    rule.Allow,
				Deny:     deny,
			}
		}
	}
	a, err := depguardpass.NewAnalyzer(&settings)
	if err != nil {
		// Bad rule set (e.g. invalid file pattern). Surface as
		// nil-Analyzer no-op; the engine reports zero diagnostics
		// rather than panicking.
		return nil
	}
	return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
}

// wireExhaustive translates ExhaustiveSettings to Flags.Set calls on
// the package-global exhaustive.Analyzer. Race surface: like
// predeclared/nonamedreturns/gocognit/maintidx/inamedparam, the
// underlying flag-bound globals are package-level. Today's engine
// builds once per process. The CheckGenerated flag is deliberately
// left at its default — exclusions.generated owns the
// generated-file gate at the engine level, mirroring golangci-lint's
// "Should be managed with linters.exclusions.generated" stance.
func wireExhaustive(cfg any) []*analysis.Analyzer {
	a := exhaustivepass.Analyzer
	s, ok := cfg.(*config.ExhaustiveSettings)
	if !ok || s == nil {
		return []*analysis.Analyzer{a}
	}
	if len(s.Check) > 0 {
		// exhaustive's stringsFlag.Set appends rather than replaces
		// (similar to importas's aliasList — landmine 20). Reset with
		// an empty input first so the user's list replaces the
		// upstream default ({switch}) rather than unioning into it.
		// Race surface: the underlying fCheck is a package-level var
		// (landmine 4/exhaustive — race-class with predeclared etc.).
		_ = a.Flags.Set(exhaustivepass.CheckFlag, "")
		_ = a.Flags.Set(exhaustivepass.CheckFlag, strings.Join(s.Check, ","))
	}
	if s.DefaultSignifiesExhaustive {
		_ = a.Flags.Set(exhaustivepass.DefaultSignifiesExhaustiveFlag, "true")
	}
	if s.IgnoreEnumMembers != "" {
		_ = a.Flags.Set(exhaustivepass.IgnoreEnumMembersFlag, s.IgnoreEnumMembers)
	}
	if s.IgnoreEnumTypes != "" {
		_ = a.Flags.Set(exhaustivepass.IgnoreEnumTypesFlag, s.IgnoreEnumTypes)
	}
	if s.PackageScopeOnly {
		_ = a.Flags.Set(exhaustivepass.PackageScopeOnlyFlag, "true")
	}
	if s.ExplicitExhaustiveMap {
		_ = a.Flags.Set(exhaustivepass.ExplicitExhaustiveMapFlag, "true")
	}
	if s.ExplicitExhaustiveSwitch {
		_ = a.Flags.Set(exhaustivepass.ExplicitExhaustiveSwitchFlag, "true")
	}
	if s.DefaultCaseRequired {
		_ = a.Flags.Set(exhaustivepass.DefaultCaseRequiredFlag, "true")
	}
	return []*analysis.Analyzer{a}
}

// wireForbidigo translates ForbidigoSettings to a fresh
// per-call forbidigo Analyzer. Each ForbidigoPattern is YAML-marshaled
// to the polymorphic upstream format and pushed via Flags.Set("p", …);
// upstream's listVar.Set appends each call, so multiple patterns
// accumulate into one analyzer's pattern list. examples/analyze_types/
// permit are also threaded via Flags.Set. If yaml.Marshal fails for
// any pattern (shouldn't — the struct is plain strings), that pattern
// is silently dropped so the others still take effect.
func wireForbidigo(cfg any) []*analysis.Analyzer {
	a := forbidigopass.NewAnalyzer()
	s, ok := cfg.(*config.ForbidigoSettings)
	if !ok || s == nil {
		return []*analysis.Analyzer{a}
	}
	for _, pat := range s.Forbid {
		arg, err := marshalForbidigoPattern(pat)
		if err != nil {
			continue
		}
		_ = a.Flags.Set("p", arg)
	}
	if s.ExcludeGodocExamples {
		_ = a.Flags.Set("examples", "true")
	}
	if s.AnalyzeTypes {
		_ = a.Flags.Set("analyze_types", "true")
	}
	return []*analysis.Analyzer{a}
}

// wireGosec wraps gosec's library API in an inline
// &analysis.Analyzer{Run: ...}. gosec.NewAnalyzer returns
// *gosec.Analyzer (not *analysis.Analyzer); the closure synthesizes
// a *packages.Package per pass and translates gosec issues to
// pass.Report. Include/exclude lists drive RuleFilter and
// AnalyzerFilter slices that gosec applies at rule-loading time.
// Severity / Confidence filtering happens post-scan (matching
// golangci-lint's behavior).
func wireGosec(cfg any) []*analysis.Analyzer {
	s, _ := cfg.(*config.GoSecSettings)
	conf := gosecpass.NewConfig()
	var ruleFilters []gosecrules.RuleFilter
	var analyzerFilters []gosecanalyzers.AnalyzerFilter
	concurrency := 0
	if s != nil {
		// Build rule + analyzer filters from the include/exclude lists.
		// `action=false` means "keep only these rules"; `true` means
		// "exclude these rules".
		if len(s.Includes) > 0 {
			ruleFilters = append(ruleFilters, gosecrules.NewRuleFilter(false, s.Includes...))
			analyzerFilters = append(analyzerFilters, gosecanalyzers.NewAnalyzerFilter(false, s.Includes...))
		}
		if len(s.Excludes) > 0 {
			ruleFilters = append(ruleFilters, gosecrules.NewRuleFilter(true, s.Excludes...))
			analyzerFilters = append(analyzerFilters, gosecanalyzers.NewAnalyzerFilter(true, s.Excludes...))
		}
		// Translate the polymorphic `config:` map. Uppercased keys
		// match upstream's reader (it lowercases on read; values are
		// case-preserving).
		for k, v := range s.Config {
			if k == gosecpass.Globals {
				if m, ok := v.(map[string]any); ok {
					for gk, gv := range m {
						option := gosecpass.GlobalOption(gk)
						// `nosec` global is set only when truthy —
						// matches gosec analyzer.go behavior.
						if option == gosecpass.Nosec && gv == false {
							continue
						}
						conf.SetGlobal(option, fmt.Sprintf("%v", gv))
					}
				}
				continue
			}
			conf.Set(strings.ToUpper(k), v)
		}
		concurrency = s.Concurrency
	}

	severity := gosecScore(s)
	confidence := gosecConfidence(s)

	// gosec analyzers that exist in plaid's pinned v2.26.1 but NOT
	// in golangci-lint v2.9's pinned v2.22.11 — drop unconditionally
	// for diagnostic parity. golangci also drops G407 (see upstream
	// pkg/golinters/gosec/gosec.go line 39); we match plus add the
	// analyzers added in gosec v2.23+ that golangci v2.9 cannot emit:
	// G702 (command-injection taint), G703 (path-traversal taint), and
	// G124 (insecure HTTP cookie configuration, added in v2.26).
	analyzerFilters = append(analyzerFilters,
		gosecanalyzers.NewAnalyzerFilter(true, "G124", "G407", "G702", "G703"))

	ruleDefs := gosecrules.Generate(false, ruleFilters...)
	analyzerDefs := gosecanalyzers.Generate(false, analyzerFilters...)
	logger := log.New(io.Discard, "", 0)

	// gosec.NewAnalyzer is built per-pass (inside the closure) because
	// it carries mutable issue state. Constructing once and sharing
	// across passes would conflate issues across packages.
	a := &analysis.Analyzer{
		Name: "gosec",
		Doc:  "Inspects source code for security problems",
		Run: func(pass *analysis.Pass) (any, error) {
			gosecAnalyzer := gosecpass.NewAnalyzer(conf, true, false, false, concurrency, logger)
			gosecAnalyzer.LoadRules(ruleDefs.RulesInfo())
			gosecAnalyzer.LoadAnalyzers(analyzerDefs.AnalyzersInfo())

			pkg := &packages.Package{
				Fset:      pass.Fset,
				Syntax:    pass.Files,
				Types:     pass.Pkg,
				TypesInfo: pass.TypesInfo,
			}
			gosecAnalyzer.CheckRules(pkg)
			gosecAnalyzer.CheckAnalyzers(pkg)

			secIssues, _, _ := gosecAnalyzer.Report()
			for _, iss := range secIssues {
				if iss.Severity < severity || iss.Confidence < confidence {
					continue
				}
				reportGosecIssue(pass, iss)
			}
			return nil, nil
		},
	}
	return []*analysis.Analyzer{a}
}

// reportGosecIssue translates one *gosec.Issue to pass.Report. gosec
// emits Line as either "N" (single line) or "N-M" (range); we use the
// start line. Column is "N". Filename is resolved via posFromPosition.
// Bad-format lines/columns are dropped silently — matches golangci-lint
// behavior of logging a warning and skipping the issue.
func reportGosecIssue(pass *analysis.Pass, iss *gosecissue.Issue) {
	line, err := strconv.Atoi(iss.Line)
	if err != nil {
		// "N-M" range — use the start line.
		var from, to int
		if n, _ := fmt.Sscanf(iss.Line, "%d-%d", &from, &to); n != 2 {
			return
		}
		line = from
	}
	col, err := strconv.Atoi(iss.Col)
	if err != nil {
		return
	}
	pos := posFromPosition(pass, token.Position{
		Filename: iss.File,
		Line:     line,
		Column:   col,
	})
	pass.Report(analysis.Diagnostic{
		Pos:     pos,
		Message: fmt.Sprintf("%s: %s", iss.RuleID, iss.What),
	})
}

// gosecScore converts the user-supplied severity string to a gosec
// Score. Empty / unknown values fall back to Low (gosec's lenient
// default), matching golangci-lint's wrapper.
func gosecScore(s *config.GoSecSettings) gosecissue.Score {
	if s == nil {
		return gosecissue.Low
	}
	switch strings.ToLower(s.Severity) {
	case "medium":
		return gosecissue.Medium
	case "high":
		return gosecissue.High
	default:
		return gosecissue.Low
	}
}

// marshalForbidigoPattern renders one ForbidigoPattern in the shape
// upstream's forbidigo.parse() / yamlPattern.UnmarshalYAML accepts.
// A bare-regex pattern (no Package, no Msg) is returned verbatim so
// upstream's legacy bare-string path applies; a structured pattern is
// YAML-marshaled to a {p, pkg, msg} mapping. The
// config.ForbidigoPattern struct's yaml tags (p, pkg, msg) align with
// the upstream pattern type, so direct yaml.Marshal yields the
// expected mapping shape.
func marshalForbidigoPattern(pat config.ForbidigoPattern) (string, error) {
	if pat.Package == "" && pat.Msg == "" {
		return pat.Pattern, nil
	}
	buf, err := yaml.Marshal(pat)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// gosecConfidence mirrors gosecScore for the Confidence threshold.
func gosecConfidence(s *config.GoSecSettings) gosecissue.Score {
	if s == nil {
		return gosecissue.Low
	}
	switch strings.ToLower(s.Confidence) {
	case "medium":
		return gosecissue.Medium
	case "high":
		return gosecissue.High
	default:
		return gosecissue.Low
	}
}
