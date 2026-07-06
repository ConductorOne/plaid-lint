// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"sort"

	unqueryvetpass "github.com/MirrexOne/unqueryvet"
	unqueryvetconfig "github.com/MirrexOne/unqueryvet/pkg/config"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/modernize"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsCleanup attaches AnalyzerFns for the final cleanup
// batch, closing out the long-tail wiring story. Three linters are
// wired here; one remains explicitly skipped because it has no
// upstream module.
//
//   - nolintlint: reimplemented in-tree (see
//     wire_analyzers_nolintlint.go). The upstream lint logic lives at
//     `github.com/golangci/golangci-lint/v2/pkg/golinters/nolintlint/internal`,
//     which is unimportable from outside the golangci-lint module
//     (landmine 26). The in-tree analyzer honors c1's load-bearing
//     fields: RequireExplanation, RequireSpecific, AllowNoExplanation.
//     AllowUnused is deferred — the engine does not currently surface
//     a per-diagnostic feedback hook to compute "which //nolint
//     directives never matched a real diagnostic", and a faithful
//     implementation needs that hook.
//
//   - modernize: wired as ShapeNativeFamily over `modernize.Suite` from
//     `golang.org/x/tools/go/analysis/passes/modernize@v0.44.0`. The
//     22-member Suite shape is the natural family fan-out (compare
//     iface's 5 sub-analyzers); ModernizeSettings.Disable lists
//     individual sub-analyzer names to drop from the Suite.
//
//   - unqueryvet: wired as ShapeNative via `unqueryvet.NewWithConfig`
//     from `github.com/MirrexOne/unqueryvet@v1.5.4`. Our typed
//     UnqueryvetSettings translates field-by-field into upstream's
//     `pkg/config.UnqueryvetSettings`. Upstream's `DefaultSettings`
//     defines a useful baseline (every check on, severity=warning,
//     SELECT * FROM information_schema/pg_catalog/sys whitelisted) —
//     we preserve it when our settings are the zero value.
//
//   - iotamixing: deliberately NOT wired. Has no upstream module — the
//     name only appears in golangci-lint's internal catalog and there
//     is no external `github.com/<author>/iotamixing` to import. The
//     seed row stays at ShapeRegistryOnly with a clarifying comment.
func wireAnalyzerFnsCleanup(c *catalog) {
	wireAnalyzerFnsNolintlint(c)

	wireNativeFamilyFn(c, "modernize", func(cfg any) []*analysis.Analyzer {
		disable := map[string]bool{}
		if s, ok := cfg.(*config.ModernizeSettings); ok && s != nil {
			for _, n := range s.Disable {
				disable[n] = true
			}
		}
		out := make([]*analysis.Analyzer, 0, len(modernize.Suite))
		for _, a := range modernize.Suite {
			if a == nil || disable[a.Name] {
				continue
			}
			out = append(out, a)
		}
		// Stable order: alphabetical by analyzer name.
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	})

	wireNativeFn(c, "unqueryvet", func(cfg any) []*analysis.Analyzer {
		s, _ := cfg.(*config.UnqueryvetSettings)
		if s == nil || isUnqueryvetZero(s) {
			// Upstream's DefaultSettings turns every check on with a
			// curated AllowedPatterns whitelist; matching golangci-lint's
			// wrapper, we surface that when the user has no opinion.
			return []*analysis.Analyzer{unqueryvetpass.New()}
		}
		ucfg := translateUnqueryvetConfig(s)
		return []*analysis.Analyzer{unqueryvetpass.NewWithConfig(&ucfg)}
	})
}

// wireNativeFamilyFn attaches an AnalyzerFn to a ShapeNativeFamily
// entry. Mirrors wireNativeFn but checks the family shape instead.
// modernize is the first long-tail family — iface, govet, staticcheck
// live directly in wire_analyzers.go alongside the always-on natives.
func wireNativeFamilyFn(c *catalog, name string, fn func(any) []*analysis.Analyzer) {
	e, ok := c.resolve(name)
	if !ok {
		panic("registry: wireNativeFamilyFn: missing catalog entry " + name)
	}
	if e.Shape != ShapeNativeFamily {
		panic("registry: wireNativeFamilyFn: entry " + name + " is not ShapeNativeFamily; update seed.go")
	}
	e.AnalyzerFn = fn
}

// isUnqueryvetZero reports whether s carries no user opinion (every
// field at its Go zero value, including the nested SQLBuilders block).
// Used to keep upstream's DefaultSettings active when the user hasn't
// supplied an `unqueryvet:` block in their config. Mirrors the pattern
// landmine 6 / 17 / 18 codifies (zero-value-means-no-opinion).
func isUnqueryvetZero(s *config.UnqueryvetSettings) bool {
	if s.CheckSQLBuilders || s.CheckAliasedWildcard || s.CheckStringConcat ||
		s.CheckFormatStrings || s.CheckStringBuilder || s.CheckSubqueries ||
		s.CheckN1 || s.CheckSQLInjection || s.CheckTxLeak {
		return false
	}
	if len(s.AllowedPatterns) != 0 || len(s.IgnoredFunctions) != 0 ||
		len(s.Allow) != 0 || len(s.CustomRules) != 0 {
		return false
	}
	if s.SQLBuilders != (config.UnqueryvetSQLBuildersSettings{}) {
		return false
	}
	return true
}

// translateUnqueryvetConfig maps our typed UnqueryvetSettings to
// upstream's pkg/config.UnqueryvetSettings field-by-field. Field
// shapes align 1:1 except CustomRules (our shape is a subset — we
// surface ID, Pattern, Patterns, When, Message, Action; upstream also
// carries Severity and Fix which we leave at their upstream zero values).
func translateUnqueryvetConfig(s *config.UnqueryvetSettings) unqueryvetconfig.UnqueryvetSettings {
	out := unqueryvetconfig.UnqueryvetSettings{
		CheckSQLBuilders:             s.CheckSQLBuilders,
		AllowedPatterns:              s.AllowedPatterns,
		IgnoredFunctions:             s.IgnoredFunctions,
		CheckAliasedWildcard:         s.CheckAliasedWildcard,
		CheckStringConcat:            s.CheckStringConcat,
		CheckFormatStrings:           s.CheckFormatStrings,
		CheckStringBuilder:           s.CheckStringBuilder,
		CheckSubqueries:              s.CheckSubqueries,
		N1DetectionEnabled:           s.CheckN1,
		SQLInjectionDetectionEnabled: s.CheckSQLInjection,
		TxLeakDetectionEnabled:       s.CheckTxLeak,
		Allow:                        s.Allow,
		SQLBuilders: unqueryvetconfig.SQLBuildersConfig{
			Squirrel:  s.SQLBuilders.Squirrel,
			GORM:      s.SQLBuilders.GORM,
			SQLx:      s.SQLBuilders.SQLx,
			Ent:       s.SQLBuilders.Ent,
			PGX:       s.SQLBuilders.PGX,
			Bun:       s.SQLBuilders.Bun,
			SQLBoiler: s.SQLBuilders.SQLBoiler,
			Jet:       s.SQLBuilders.Jet,
		},
	}
	if len(s.CustomRules) > 0 {
		out.CustomRules = make([]unqueryvetconfig.CustomRule, 0, len(s.CustomRules))
		for _, r := range s.CustomRules {
			out.CustomRules = append(out.CustomRules, unqueryvetconfig.CustomRule{
				ID:       r.ID,
				Pattern:  r.Pattern,
				Patterns: r.Patterns,
				When:     r.When,
				Message:  r.Message,
				Action:   r.Action,
			})
		}
	}
	return out
}
