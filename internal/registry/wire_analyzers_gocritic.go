// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"go/types"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"

	gocriticcheckers "github.com/go-critic/go-critic/checkers"
	gocriticlinter "github.com/go-critic/go-critic/linter"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsGocritic attaches the AnalyzerFn for the `gocritic`
// catalog row. gocritic exposes one analyzer that fans 100+ sub-checks
// internally — Shape is ShapeNative (one *analysis.Analyzer), not
// ShapeNativeFamily (the sub-checks aren't *analysis.Analyzer-shaped
// and don't surface as independent analyzers).
//
// Translation surface — five GoCriticSettings fields:
//
//   - EnableAll / DisableAll: revoke the upstream "all stable tags"
//     default and start from "all checks" or "no checks".
//   - EnabledChecks / DisabledChecks: explicit by-name include /
//     exclude on top of the default-or-EnableAll/DisableAll base.
//   - EnabledTags / DisabledTags: tag-based include / exclude (tags
//     are `diagnostic`, `experimental`, `opinionated`, `performance`,
//     `security`, `style`).
//   - SettingsPerCheck: polymorphic map[checkName]map[paramName]any,
//     translated to checker-info Params overrides. Mutates the
//     prototype CheckerInfo.Params[k].Value pointer — process-global
//     mutation, fine for one Build per process; documented as
//     landmine 28.
//
// The default-enabled set mirrors upstream's
// `isEnabledByDefaultGoCriticChecker`: a check is enabled by default
// iff it has none of the experimental/opinionated/performance/security
// tags. The Go-version field comes from Settings.Go (populated by
// propagateGoVersion). Empty Go == "no version assumption", matching
// upstream defaults.
func wireAnalyzerFnsGocritic(c *catalog) {
	wireNativeFn(c, "gocritic", wireGocritic)
}

// gocriticInitEmbeddedOnce guards a one-time call to
// checkers.InitEmbeddedRules(). The ruleguard-based embedded checks
// register additional CheckerInfos at first call; without this the
// catalog of available checks is incomplete (the bare
// `_ "github.com/go-critic/go-critic/checkers"` import does NOT pull
// the embedded ruleguard rules in — InitEmbeddedRules is an explicit
// hook). Mirrors golangci-lint's gocriticWrapper.init sync.Once.
var gocriticInitEmbeddedOnce sync.Once

// gocriticAllTags is the canonical tag set, used when expanding
// EnabledTags/DisabledTags. Sourced from upstream's linter package
// constants — keeps us decoupled from the underlying tag-list
// representation on each CheckerInfo.
var gocriticAllTags = []string{
	gocriticlinter.DiagnosticTag,
	gocriticlinter.ExperimentalTag,
	gocriticlinter.OpinionatedTag,
	gocriticlinter.PerformanceTag,
	gocriticlinter.SecurityTag,
	gocriticlinter.StyleTag,
}

// wireGocritic returns a single *analysis.Analyzer that fans the
// enabled-check set inside one Run. The Run closure mirrors
// upstream's checkers/analyzer/run.go but takes the enabled list and
// per-check params from our typed settings instead of analyzer
// flags. Caching of the enabled set across passes is intentional —
// the set is purely a function of settings, not pass state.
func wireGocritic(cfg any) []*analysis.Analyzer {
	settings, _ := cfg.(*config.GoCriticSettings)

	// Initialize embedded ruleguard rules (registers extra
	// CheckerInfos). Once-per-process; subsequent Builds share the
	// extended registry.
	gocriticInitEmbeddedOnce.Do(func() {
		// Error path is non-fatal — embedded rules failing only
		// removes the ruleguard-derived checks; the static checkers
		// still run. Matches golangci-lint's logger.Fatalf escape
		// hatch in spirit but without the process-kill.
		_ = gocriticcheckers.InitEmbeddedRules()
	})

	enabledNames := gocriticEnabledChecks(settings)

	// Apply SettingsPerCheck to the prototype CheckerInfo.Params
	// values. This is process-global mutation — see landmine 28.
	// We do it once at wire time (before Run) rather than per-pass
	// because the constructor closure inside upstream's checker
	// init() reads info.Params at NewChecker time; mutating between
	// passes would race against any concurrent Build.
	gocriticApplyParams(settings)

	goVersion := ""
	if settings != nil {
		goVersion = settings.Go
	}
	sizes := types.SizesFor("gc", runtime.GOARCH)

	a := &analysis.Analyzer{
		Name: "gocritic",
		Doc:  "Provides diagnostics that check for bugs, performance and style issues.",
		Run: func(pass *analysis.Pass) (any, error) {
			ctx := gocriticlinter.NewContext(pass.Fset, sizes)
			ctx.SetGoVersion(goVersion)
			ctx.SetPackageInfo(pass.TypesInfo, pass.Pkg)

			// Build the per-pass checker instances. NewChecker reads
			// info.Params under the hood; we've already applied
			// user-supplied param overrides above.
			infos := gocriticlinter.GetCheckersInfo()
			checkers := make([]*gocriticlinter.Checker, 0, len(enabledNames))
			needFileInfo := false
			for _, info := range infos {
				if !enabledNames[info.Name] {
					continue
				}
				ch, err := gocriticlinter.NewChecker(ctx, info)
				if err != nil {
					return nil, fmt.Errorf("gocritic: init %s: %w", info.Name, err)
				}
				checkers = append(checkers, ch)
				// importShadow needs per-file `name -> *ast.File`
				// state set via SetFileInfo (see upstream's
				// importShadow_checker for the file-name lookup).
				if strings.EqualFold(info.Name, "importShadow") {
					needFileInfo = true
				}
			}
			if len(checkers) == 0 {
				return nil, nil
			}

			for _, f := range pass.Files {
				if needFileInfo {
					ctx.SetFileInfo(f.Name.Name, f)
				}
				for _, ch := range checkers {
					for _, w := range ch.Check(f) {
						diag := analysis.Diagnostic{
							Pos:      w.Pos,
							Category: ch.Info.Name,
							Message:  fmt.Sprintf("%s: %s", ch.Info.Name, w.Text),
						}
						if w.HasQuickFix() {
							diag.SuggestedFixes = []analysis.SuggestedFix{{
								TextEdits: []analysis.TextEdit{{
									Pos:     w.Suggestion.From,
									End:     w.Suggestion.To,
									NewText: w.Suggestion.Replacement,
								}},
							}}
						}
						pass.Report(diag)
					}
				}
			}
			return nil, nil
		},
	}
	return []*analysis.Analyzer{a}
}

// gocriticEnabledChecks computes the active check-name set from the
// typed settings. Mirrors golangci-lint's settingsWrapper.inferEnabledChecks:
//
//  1. Base = enabled-by-default (no experimental/opinionated/
//     performance/security tag) OR all-checks (EnableAll) OR
//     empty (DisableAll).
//  2. Add by EnabledTags (expand tag -> checks).
//  3. Add by EnabledChecks (explicit list).
//  4. Remove by DisabledTags.
//  5. Remove by DisabledChecks.
//
// Order of operations matches upstream: tags expand before explicit
// names, and disables apply after enables. Unknown check / tag names
// are silently dropped (mirrors our other wirings — registry-level
// validation of these would belong in [config.Validate] and is out
// of scope for the wiring closure).
func gocriticEnabledChecks(s *config.GoCriticSettings) map[string]bool {
	all := gocriticlinter.GetCheckersInfo()
	enabled := make(map[string]bool, len(all))

	if s == nil {
		s = &config.GoCriticSettings{}
	}

	switch {
	case s.DisableAll:
		// Start empty.
	case s.EnableAll:
		for _, info := range all {
			enabled[info.Name] = true
		}
	default:
		for _, info := range all {
			if isEnabledByDefaultGocritic(info) {
				enabled[info.Name] = true
			}
		}
	}

	if len(s.EnabledTags) > 0 {
		for _, info := range all {
			if hasAnyTag(info, s.EnabledTags) {
				enabled[info.Name] = true
			}
		}
	}
	for _, name := range s.EnabledChecks {
		enabled[name] = true
	}
	if len(s.DisabledTags) > 0 {
		for _, info := range all {
			if hasAnyTag(info, s.DisabledTags) {
				delete(enabled, info.Name)
			}
		}
	}
	for _, name := range s.DisabledChecks {
		delete(enabled, name)
	}

	return enabled
}

// isEnabledByDefaultGocritic mirrors upstream's
// isEnabledByDefaultGoCriticChecker: a checker is on by default iff
// it carries none of the experimental / opinionated / performance /
// security tags. The remaining diagnostic + style tags are the
// "stable default".
func isEnabledByDefaultGocritic(info *gocriticlinter.CheckerInfo) bool {
	return !info.HasTag(gocriticlinter.ExperimentalTag) &&
		!info.HasTag(gocriticlinter.OpinionatedTag) &&
		!info.HasTag(gocriticlinter.PerformanceTag) &&
		!info.HasTag(gocriticlinter.SecurityTag)
}

func hasAnyTag(info *gocriticlinter.CheckerInfo, tags []string) bool {
	for _, t := range tags {
		if info.HasTag(t) {
			return true
		}
	}
	return false
}

// gocriticApplyParams walks SettingsPerCheck and writes each
// user-supplied value onto the matching CheckerInfo.Params[k].Value.
// Two case-insensitivity rules from upstream:
//
//   - Check name lookup is case-insensitive (upstream's
//     GetLowerCasedParams). golangci-lint lowercases all settings
//     keys at parse time; plaid-lint's YAML parser preserves case,
//     so we must do the case-insensitive match here.
//   - Param-name lookup inside a check is case-insensitive too
//     (upstream's setCheckerParams lowercases both sides).
//
// Value normalization matches golangci-lint's
// normalizeCheckerParamsValue: reflect on the user value's kind and
// convert int-family / bool to gocritic's expected types
// (gocritic asserts info.Params[k].Value is exactly int / bool /
// string — see linter/helpers.go validateCheckerInfo).
//
// SECURITY/CORRECTNESS NOTE: this mutates the prototype CheckerInfo,
// which is a process-global registered by upstream's per-checker
// init() blocks. The mutation persists for the lifetime of the
// process and across subsequent Builds — landmine 28. Today's engine
// runs one Build per process so this is benign; if Build ever runs
// concurrently with conflicting gocritic.SettingsPerCheck inputs,
// the wiring needs a sync.Mutex + reset-to-defaults pattern (similar
// to decorder's two-phase translate-then-set — landmine 19).
func gocriticApplyParams(s *config.GoCriticSettings) {
	if s == nil || len(s.SettingsPerCheck) == 0 {
		return
	}
	// Build a lowercased index of user keys -> raw params map.
	lcUser := make(map[string]config.GoCriticCheckSettings, len(s.SettingsPerCheck))
	for k, v := range s.SettingsPerCheck {
		lcUser[strings.ToLower(k)] = v
	}

	for _, info := range gocriticlinter.GetCheckersInfo() {
		params, ok := lcUser[strings.ToLower(info.Name)]
		if !ok {
			continue
		}
		// Build a lowercased index of param names on this checker
		// pointing at the (shared) *CheckerParam.
		lcInfoParams := make(map[string]*gocriticlinter.CheckerParam, len(info.Params))
		for pname, p := range info.Params {
			lcInfoParams[strings.ToLower(pname)] = p
		}
		for k, raw := range params {
			cp, ok := lcInfoParams[strings.ToLower(k)]
			if !ok {
				// Unknown param — silently drop (matches our other
				// polymorphic-config wirings; validation belongs in
				// config.Validate, not the wiring closure).
				continue
			}
			cp.Value = normalizeGocriticParamValue(raw)
		}
	}
}

// normalizeGocriticParamValue coerces YAML-parsed scalars into the
// int / bool / string set upstream's per-checker constructors expect.
// gocritic's helpers.go validateCheckerInfo will panic at NewChecker
// time if Value isn't exactly one of those three types. YAML/JSON
// parsers commonly hand us int64 / uint / float64 for numeric
// literals, so the int-family coercion is load-bearing.
func normalizeGocriticParamValue(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return v
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(rv.Uint())
	case reflect.Float32, reflect.Float64:
		// YAML decoders sometimes hand integer values as float64. If
		// the fractional part is zero, accept as int.
		f := rv.Float()
		if f == float64(int(f)) {
			return int(f)
		}
		return v
	case reflect.Bool:
		return rv.Bool()
	case reflect.String:
		return rv.String()
	default:
		return v
	}
}

// gocriticAllTagsSorted returns a stable-sorted copy of
// gocriticAllTags, used by tests asserting on the tag set.
func gocriticAllTagsSorted() []string {
	out := slices.Clone(gocriticAllTags)
	slices.Sort(out)
	return out
}
