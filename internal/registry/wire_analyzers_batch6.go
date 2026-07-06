// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strconv"
	"strings"

	testifylintpass "github.com/Antonboom/testifylint/analyzer"
	exhaustructpass "github.com/GaijinEntertainment/go-exhaustruct/v4/analyzer"
	varnamelenpass "github.com/blizzy78/varnamelen"
	wslpass "github.com/bombsimon/wsl/v5"
	ireturnpass "github.com/butuzov/ireturn/analyzer"
	goheaderpass "github.com/denis-tingaikin/go-header"
	spancheckpass "github.com/jjti/go-spancheck"
	importaspass "github.com/julz/importas"
	thelperpass "github.com/kulti/thelper/pkg/analyzer"
	tagliatellepass "github.com/ldez/tagliatelle"
	loggercheckpass "github.com/timonwong/loggercheck"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch6 attaches AnalyzerFns for the sixth long-tail
// wiring batch. Eleven entries spanning the moderate-complexity middle
// of the remaining ShapeRegistryOnly pool: factories with constructor
// configs (tagliatelle, spancheck, wsl_v5, goheader), factories with
// flag-set settings (varnamelen, ireturn, thelper, testifylint),
// variadic-options factories (loggercheck), the fallible-constructor
// shape (exhaustruct), and importas's package-global flag layout.
//
// New patterns surfaced and recorded in the playbook:
//
//   - Landmine 20: importas package-global config accumulation. The
//     `aliasList.Set` flag value appends to the package-level `config`
//     global; multiple Builds layer their alias lists. Today's engine
//     builds once per process and accepts the accumulation; if that
//     ever changes the layered baseline-and-override approach from
//     decorder (landmine 19) is the closest reusable shape.
//
//   - Landmine 21: pointer-bool nested option blocks. `thelper` and
//     `testifylint.formatter.check-format-string` express tri-state
//     (nil / true / false) via `*bool` fields where nil means "use
//     upstream default". Pointer-aware translation; converting `*bool`
//     to a Flags.Set string only fires when the pointer is non-nil.
//
//   - Eighth Analyzer-export shape: `Config.FillSettings(*Settings)`
//     helper. goheader exposes a yaml-shaped `Config` that translates
//     into a runtime `*Settings` via `FillSettings`. Treat it as a
//     constructor-arg path; the YAML→Settings conversion is owned by
//     upstream.
func wireAnalyzerFnsBatch6(c *catalog) {
	wireNativeFn(c, "varnamelen", func(cfg any) []*analysis.Analyzer {
		// varnamelen.NewAnalyzer returns a fresh Analyzer per call with
		// its own per-Analyzer flag set (no package globals). camelCase
		// flag names (landmine 16). All Flags.Set defaults are zero
		// upstream — no any-non-zero guard needed.
		a := varnamelenpass.NewAnalyzer()
		s, ok := cfg.(*config.VarnamelenSettings)
		if !ok || s == nil {
			return []*analysis.Analyzer{a}
		}
		if s.MaxDistance > 0 {
			_ = a.Flags.Set("maxDistance", strconv.Itoa(s.MaxDistance))
		}
		if s.MinNameLength > 0 {
			_ = a.Flags.Set("minNameLength", strconv.Itoa(s.MinNameLength))
		}
		if len(s.IgnoreNames) > 0 {
			_ = a.Flags.Set("ignoreNames", strings.Join(s.IgnoreNames, ","))
		}
		if len(s.IgnoreDecls) > 0 {
			_ = a.Flags.Set("ignoreDecls", strings.Join(s.IgnoreDecls, ","))
		}
		if s.CheckReceiver {
			_ = a.Flags.Set("checkReceiver", "true")
		}
		if s.CheckReturn {
			_ = a.Flags.Set("checkReturn", "true")
		}
		if s.CheckTypeParam {
			_ = a.Flags.Set("checkTypeParam", "true")
		}
		if s.IgnoreTypeAssertOk {
			_ = a.Flags.Set("ignoreTypeAssertOk", "true")
		}
		if s.IgnoreMapIndexOk {
			_ = a.Flags.Set("ignoreMapIndexOk", "true")
		}
		if s.IgnoreChanRecvOk {
			_ = a.Flags.Set("ignoreChanRecvOk", "true")
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "wsl_v5", func(cfg any) []*analysis.Analyzer {
		// wsl_v5 takes a *Configuration value. Default-checks resolves
		// the upstream default set; the Configuration zero-value carries
		// nil Checks which upstream interprets as the default set.
		s, ok := cfg.(*config.WSLv5Settings)
		if !ok || s == nil ||
			(!s.AllowFirstInBlock && !s.AllowWholeBlock &&
				s.BranchMaxLines == 0 && s.CaseMaxLines == 0 &&
				s.Default == "" && len(s.Enable) == 0 && len(s.Disable) == 0) {
			return []*analysis.Analyzer{wslpass.NewAnalyzer(wslpass.NewConfig())}
		}
		cfgw := wslpass.NewConfig()
		cfgw.AllowFirstInBlock = s.AllowFirstInBlock
		cfgw.AllowWholeBlock = s.AllowWholeBlock
		if s.BranchMaxLines > 0 {
			cfgw.BranchMaxLines = s.BranchMaxLines
		}
		if s.CaseMaxLines > 0 {
			cfgw.CaseMaxLines = s.CaseMaxLines
		}
		// Compose Checks via NewCheckSet — upstream-owned grammar handles
		// "all"/"default"/"none" alongside an enable/disable list.
		def := s.Default
		if def == "" {
			def = "default"
		}
		checks, err := wslpass.NewCheckSet(def, s.Enable, s.Disable)
		if err == nil {
			cfgw.Checks = checks
		}
		return []*analysis.Analyzer{wslpass.NewAnalyzer(cfgw)}
	})

	wireNativeFn(c, "tagliatelle", func(cfg any) []*analysis.Analyzer {
		// tagliatelle.New(Config) constructor; Config is a value (not
		// pointer). Empty Rules + empty Overrides falls back to upstream
		// defaults inside New.
		tc := tagliatellepass.Config{}
		s, ok := cfg.(*config.TagliatelleSettings)
		if ok && s != nil {
			tc.Base = toTagliatelleBase(s.Case.TagliatelleBase)
			for _, ov := range s.Case.Overrides {
				tc.Overrides = append(tc.Overrides, tagliatellepass.Overrides{
					Base:    toTagliatelleBase(ov.TagliatelleBase),
					Package: ov.Package,
				})
				// Ignore field is per-override; map by reusing Base.Ignore.
				if ov.Ignore {
					tc.Overrides[len(tc.Overrides)-1].Base.Ignore = true
				}
			}
		}
		return []*analysis.Analyzer{tagliatellepass.New(tc)}
	})

	wireNativeFn(c, "spancheck", func(cfg any) []*analysis.Analyzer {
		// spancheck.NewAnalyzerWithConfig(*Config). NewDefaultConfig
		// carries the default EnabledChecks (EndCheck) plus the
		// default StartSpanMatchersSlice. Layer user fields only when
		// non-empty so the upstream defaults survive a zero-value
		// SpancheckSettings.
		sc := spancheckpass.NewDefaultConfig()
		if s, ok := cfg.(*config.SpancheckSettings); ok && s != nil {
			if len(s.Checks) > 0 {
				sc.EnabledChecks = s.Checks
			}
			if len(s.IgnoreCheckSignatures) > 0 {
				sc.IgnoreChecksSignaturesSlice = s.IgnoreCheckSignatures
			}
			if len(s.ExtraStartSpanSignatures) > 0 {
				sc.StartSpanMatchersSlice = append(sc.StartSpanMatchersSlice, s.ExtraStartSpanSignatures...)
			}
		}
		return []*analysis.Analyzer{spancheckpass.NewAnalyzerWithConfig(sc)}
	})

	wireNativeFn(c, "ireturn", func(cfg any) []*analysis.Analyzer {
		// ireturn.NewAnalyzer returns a fresh Analyzer with its own
		// per-Analyzer flag set. Flags: allow/reject (comma-separated
		// strings), nonolint (bool). Defaults are empty so per-field
		// guards are sufficient.
		a := ireturnpass.NewAnalyzer()
		if s, ok := cfg.(*config.IreturnSettings); ok && s != nil {
			if len(s.Allow) > 0 {
				_ = a.Flags.Set("allow", strings.Join(s.Allow, ","))
			}
			if len(s.Reject) > 0 {
				_ = a.Flags.Set("reject", strings.Join(s.Reject, ","))
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "goheader", func(cfg any) []*analysis.Analyzer {
		// goheader.New(*Settings). The yaml-shaped Config has its own
		// FillSettings translator that produces a runtime *Settings —
		// reuse it rather than duplicating the values→Value compilation.
		// Eighth Analyzer-export shape.
		settings := &goheaderpass.Settings{}
		if s, ok := cfg.(*config.GoHeaderSettings); ok && s != nil &&
			(s.Template != "" || s.TemplatePath != "" || len(s.Values) > 0) {
			gc := &goheaderpass.Config{
				Values:       s.Values,
				Template:     s.Template,
				TemplatePath: s.TemplatePath,
			}
			// FillSettings returns nil on success; on template/values
			// parse error the analyzer is built with empty settings and
			// upstream's run-time reports the parse failure as a
			// diagnostic. Mirroring golangci-lint's tolerant behavior.
			_ = gc.FillSettings(settings)
		}
		// goheader's Run reads only pass.Files and
		// pass.Fset (analyzer.go:102-161 fans out pass.Files to a
		// worker pool, each worker resolves the filename via
		// pass.Fset.PositionFor and pass.Fset.File, parses the file's
		// leading comment block against the template, and reports
		// header-mismatch diagnostics via pass.Report). No
		// pass.TypesInfo, pass.Pkg, or pass.ResultOf. Classified
		// TypeUseSyntaxOnly. Source-of-truth audit:
		// github.com/denis-tingaikin/go-header@v1.0.0/analyzer.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(goheaderpass.New(settings), 1)}
	})

	wireNativeFn(c, "thelper", func(cfg any) []*analysis.Analyzer {
		// thelper exposes a single "checks" comma-string flag whose
		// default is every check enabled. ThelperOptions fields are
		// *bool with nil-means-default semantics. Translate by building
		// the enable list explicitly when the user provided any pointer
		// non-nil; otherwise leave upstream's default in place.
		a := thelperpass.NewAnalyzer()
		s, ok := cfg.(*config.ThelperSettings)
		if !ok || s == nil || !thelperConfigured(s) {
			return []*analysis.Analyzer{a}
		}
		enabled := buildThelperChecks(s)
		_ = a.Flags.Set("checks", strings.Join(enabled, ","))
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "testifylint", func(cfg any) []*analysis.Analyzer {
		// testifylint.New() binds its Config to the Analyzer's flag set
		// via internal/config.BindToFlags. The flag names are dotted
		// (e.g. "formatter.check-format-string"). Several upstream
		// defaults are true (formatter.check-format-string,
		// formatter.require-string-msg) — landmine 17 applies. Only
		// translate when the user provided a non-zero setting.
		a := testifylintpass.New()
		s, ok := cfg.(*config.TestifylintSettings)
		if !ok || s == nil || !testifylintConfigured(s) {
			return []*analysis.Analyzer{a}
		}
		if s.EnableAll {
			_ = a.Flags.Set("enable-all", "true")
		}
		if s.DisableAll {
			_ = a.Flags.Set("disable-all", "true")
		}
		if len(s.EnabledCheckers) > 0 {
			_ = a.Flags.Set("enable", strings.Join(s.EnabledCheckers, ","))
		}
		if len(s.DisabledCheckers) > 0 {
			_ = a.Flags.Set("disable", strings.Join(s.DisabledCheckers, ","))
		}
		if s.BoolCompare.IgnoreCustomTypes {
			_ = a.Flags.Set("bool-compare.ignore-custom-types", "true")
		}
		if s.ExpectedActual.ExpVarPattern != "" {
			_ = a.Flags.Set("expected-actual.pattern", s.ExpectedActual.ExpVarPattern)
		}
		if s.Formatter.CheckFormatString != nil {
			_ = a.Flags.Set("formatter.check-format-string",
				strconv.FormatBool(*s.Formatter.CheckFormatString))
		}
		if s.Formatter.RequireFFuncs {
			_ = a.Flags.Set("formatter.require-f-funcs", "true")
		}
		// require-string-msg upstream default is true; only override on
		// explicit non-zero. Cannot distinguish "user set false" from
		// "zero value" without a pointer; preserve upstream default.
		if s.Formatter.RequireStringMsg {
			_ = a.Flags.Set("formatter.require-string-msg", "true")
		}
		if s.GoRequire.IgnoreHTTPHandlers {
			_ = a.Flags.Set("go-require.ignore-http-handlers", "true")
		}
		if s.RequireError.FnPattern != "" {
			_ = a.Flags.Set("require-error.fn-pattern", s.RequireError.FnPattern)
		}
		if s.SuiteExtraAssertCall.Mode != "" {
			_ = a.Flags.Set("suite-extra-assert-call.mode", s.SuiteExtraAssertCall.Mode)
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "loggercheck", func(cfg any) []*analysis.Analyzer {
		// loggercheck.NewAnalyzer(...Option). Positive bool semantics
		// in our settings (Kitlog=true means enabled) map to the
		// negative WithDisable list. Zero-value settings struct means
		// "use upstream defaults" — landmine 17. Upstream default
		// disables kitlog only.
		s, ok := cfg.(*config.LoggerCheckSettings)
		if !ok || s == nil || !loggercheckConfigured(s) {
			return []*analysis.Analyzer{loggercheckpass.NewAnalyzer()}
		}
		var opts []loggercheckpass.Option
		// Compute disable list from positive flags. If the user
		// configured at least one logger flag, build the disable list
		// explicitly; otherwise keep upstream defaults.
		if loggercheckAnyLoggerSet(s) {
			var disable []string
			for _, lg := range []struct {
				name    string
				enabled bool
			}{
				{"kitlog", s.Kitlog},
				{"klog", s.Klog},
				{"logr", s.Logr},
				{"slog", s.Slog},
				{"zap", s.Zap},
			} {
				if !lg.enabled {
					disable = append(disable, lg.name)
				}
			}
			opts = append(opts, loggercheckpass.WithDisable(disable))
		}
		if s.RequireStringKey {
			opts = append(opts, loggercheckpass.WithRequireStringKey(true))
		}
		if s.NoPrintfLike {
			opts = append(opts, loggercheckpass.WithNoPrintfLike(true))
		}
		if len(s.Rules) > 0 {
			opts = append(opts, loggercheckpass.WithRules(s.Rules))
		}
		return []*analysis.Analyzer{loggercheckpass.NewAnalyzer(opts...)}
	})

	wireNativeFn(c, "exhaustruct", func(cfg any) []*analysis.Analyzer {
		// exhaustruct v4 NewAnalyzer(Config) returns (Analyzer, error)
		// — fallible constructor (landmine: batch2 asasalint shape).
		// Bad regex in IncludeRx/ExcludeRx fails Prepare; surface the
		// nil-on-failure semantic.
		var ec exhaustructpass.Config
		if s, ok := cfg.(*config.ExhaustructSettings); ok && s != nil {
			ec.IncludeRx = s.Include
			ec.ExcludeRx = s.Exclude
			ec.AllowEmpty = s.AllowEmpty
			ec.AllowEmptyRx = s.AllowEmptyRx
			ec.AllowEmptyReturns = s.AllowEmptyReturns
			ec.AllowEmptyDeclarations = s.AllowEmptyDeclarations
		}
		a, err := exhaustructpass.NewAnalyzer(ec)
		if err != nil {
			return nil
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "importas", func(cfg any) []*analysis.Analyzer {
		// importas.Analyzer is a package-level var bound to a
		// package-level `config` global via Flags. Each Flags.Set("alias",
		// "path:alias") *appends* to that global; a second Build with a
		// different alias list would accumulate the union. Today's
		// engine runs one Build per process so the accumulation is
		// harmless. Landmine 20.
		a := importaspass.Analyzer
		if s, ok := cfg.(*config.ImportAsSettings); ok && s != nil {
			if s.NoUnaliased {
				_ = a.Flags.Set("no-unaliased", "true")
			}
			if s.NoExtraAliases {
				_ = a.Flags.Set("no-extra-aliases", "true")
			}
			for _, al := range s.Alias {
				if al.Pkg == "" || al.Alias == "" {
					continue
				}
				_ = a.Flags.Set("alias", al.Pkg+":"+al.Alias)
			}
		}
		return []*analysis.Analyzer{a}
	})
}

// toTagliatelleBase copies our TagliatelleBase verbatim. The structs
// are field-for-field identical; the conversion exists because they're
// declared in separate modules.
func toTagliatelleBase(in config.TagliatelleBase) tagliatellepass.Base {
	out := tagliatellepass.Base{
		Rules:         in.Rules,
		UseFieldName:  in.UseFieldName,
		IgnoredFields: in.IgnoredFields,
	}
	if len(in.ExtendedRules) > 0 {
		out.ExtendedRules = make(map[string]tagliatellepass.ExtendedRule, len(in.ExtendedRules))
		for k, v := range in.ExtendedRules {
			out.ExtendedRules[k] = tagliatellepass.ExtendedRule{
				Case:                v.Case,
				ExtraInitialisms:    v.ExtraInitialisms,
				InitialismOverrides: v.InitialismOverrides,
			}
		}
	}
	return out
}

// thelperConfigured reports whether any thelper sub-block holds a
// non-nil pointer. A nil pointer is "no opinion" — upstream's full
// twelve-check enable list stays in place.
func thelperConfigured(s *config.ThelperSettings) bool {
	for _, o := range []config.ThelperOptions{s.Test, s.Fuzz, s.Benchmark, s.TB} {
		if o.First != nil || o.Name != nil || o.Begin != nil {
			return true
		}
	}
	return false
}

// buildThelperChecks renders the comma-separated `checks` flag value
// from the ThelperSettings sub-blocks. Each (kind, field) tuple maps
// to a flag token; a nil pointer is treated as enabled (matching
// upstream's "all on by default" baseline once the user has opted into
// explicit configuration).
func buildThelperChecks(s *config.ThelperSettings) []string {
	type tup struct {
		token string
		val   *bool
	}
	tokens := []tup{
		{"t_begin", s.Test.Begin}, {"t_first", s.Test.First}, {"t_name", s.Test.Name},
		{"f_begin", s.Fuzz.Begin}, {"f_first", s.Fuzz.First}, {"f_name", s.Fuzz.Name},
		{"b_begin", s.Benchmark.Begin}, {"b_first", s.Benchmark.First}, {"b_name", s.Benchmark.Name},
		{"tb_begin", s.TB.Begin}, {"tb_first", s.TB.First}, {"tb_name", s.TB.Name},
	}
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		// nil = upstream default = enabled.
		if t.val == nil || *t.val {
			out = append(out, t.token)
		}
	}
	return out
}

// testifylintConfigured reports whether the user provided any non-zero
// testifylint field. Used as the landmine-17 any-non-zero gate before
// touching the analyzer's flag set (whose defaults include
// formatter.check-format-string=true and formatter.require-string-msg=true).
func testifylintConfigured(s *config.TestifylintSettings) bool {
	if s.EnableAll || s.DisableAll {
		return true
	}
	if len(s.EnabledCheckers) > 0 || len(s.DisabledCheckers) > 0 {
		return true
	}
	if s.BoolCompare.IgnoreCustomTypes {
		return true
	}
	if s.ExpectedActual.ExpVarPattern != "" {
		return true
	}
	if s.Formatter.CheckFormatString != nil || s.Formatter.RequireFFuncs || s.Formatter.RequireStringMsg {
		return true
	}
	if s.GoRequire.IgnoreHTTPHandlers {
		return true
	}
	if s.RequireError.FnPattern != "" {
		return true
	}
	if s.SuiteExtraAssertCall.Mode != "" {
		return true
	}
	return false
}

// loggercheckConfigured reports whether any LoggerCheckSettings field
// is non-zero. Mirrors the landmine-17 guard.
func loggercheckConfigured(s *config.LoggerCheckSettings) bool {
	return s.Kitlog || s.Klog || s.Logr || s.Slog || s.Zap ||
		s.RequireStringKey || s.NoPrintfLike || len(s.Rules) > 0
}

// loggercheckAnyLoggerSet reports whether the user toggled any of the
// per-logger positive flags. Used to decide whether to compute and
// apply the disable list at all — if every logger flag is at its
// (false) zero value, we leave upstream's default ("disable kitlog
// only") untouched.
func loggercheckAnyLoggerSet(s *config.LoggerCheckSettings) bool {
	return s.Kitlog || s.Klog || s.Logr || s.Slog || s.Zap
}
