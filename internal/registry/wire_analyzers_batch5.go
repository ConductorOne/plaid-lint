// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"strconv"
	"strings"

	errorlintpass "codeberg.org/polyfloyd/go-errorlint/errorlint"
	tagalignpass "github.com/4meepo/tagalign"
	perfsprintpass "github.com/catenacyber/perfsprint/analyzer"
	protogetterpass "github.com/ghostiam/protogetter"
	usetestingpass "github.com/ldez/usetesting"
	grouperpass "github.com/leonklingele/grouper/pkg/analyzer"
	embeddedstructfieldcheckpass "github.com/manuelarte/embeddedstructfieldcheck/analyzer"
	recvcheckpass "github.com/raeperd/recvcheck"
	wrapcheckpass "github.com/tomarrell/wrapcheck/v2/wrapcheck"
	decorderpass "gitlab.com/bosi/decorder"
	musttagpass "go-simpler.org/musttag"
	sloglintpass "go-simpler.org/sloglint"
	"golang.org/x/tools/go/analysis"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsBatch5 attaches AnalyzerFns for the fifth long-tail
// wiring batch. Twelve entries: four simple-settings holdouts
// (embeddedstructfieldcheck, grouper, recvcheck, usetesting) plus
// eight moderate-complexity entries with []string maps, option
// variadics, or constructor structs (decorder, errorlint, musttag,
// perfsprint, protogetter, sloglint, tagalign, wrapcheck).
//
// New patterns surfaced and recorded in the playbook:
//
//   - Landmine 17: upstream defaults differ from struct zero-value.
//     decorder, errorlint, perfsprint, tagalign, usetesting all enable
//     some checks by default; an unconditional Flags.Set from a
//     zero-value settings struct silently disables them.
//
//   - Landmine 18: sloglint v0.12.0 renamed Options fields out from
//     under golangci-lint's wrapper. Our SlogLintSettings YAML keys
//     still match the wrapper's nomenclature; the translation crosses
//     the rename gap.
//
//   - Landmine 19: decorder's golangci-lint wrapper bakes in different
//     defaults from upstream. Mirror the wrapper's defaults rather
//     than upstream's to stay bug-for-bug compatible with what users
//     of v2 configs expect.
func wireAnalyzerFnsBatch5(c *catalog) {
	wireNativeFn(c, "embeddedstructfieldcheck", func(cfg any) []*analysis.Analyzer {
		a := embeddedstructfieldcheckpass.NewAnalyzer()
		if s, ok := cfg.(*config.EmbeddedStructFieldCheckSettings); ok && s != nil {
			if s.ForbidMutex {
				_ = a.Flags.Set(embeddedstructfieldcheckpass.ForbidMutexCheck, "true")
			}
			if s.EmptyLine {
				_ = a.Flags.Set(embeddedstructfieldcheckpass.EmptyLineCheck, "true")
			}
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "grouper", func(cfg any) []*analysis.Analyzer {
		// All grouper "require-X" flags default to false upstream and
		// are positive-bool semantics ("do enforce"). Zero-value
		// settings maps cleanly to upstream defaults; no any-non-zero
		// guard needed.
		a := grouperpass.New()
		if s, ok := cfg.(*config.GrouperSettings); ok && s != nil {
			_ = a.Flags.Set(grouperpass.FlagNameConstRequireSingleConst, strconv.FormatBool(s.ConstRequireSingleConst))
			_ = a.Flags.Set(grouperpass.FlagNameConstRequireGrouping, strconv.FormatBool(s.ConstRequireGrouping))
			_ = a.Flags.Set(grouperpass.FlagNameImportRequireSingleImport, strconv.FormatBool(s.ImportRequireSingleImport))
			_ = a.Flags.Set(grouperpass.FlagNameImportRequireGrouping, strconv.FormatBool(s.ImportRequireGrouping))
			_ = a.Flags.Set(grouperpass.FlagNameTypeRequireSingleType, strconv.FormatBool(s.TypeRequireSingleType))
			_ = a.Flags.Set(grouperpass.FlagNameTypeRequireGrouping, strconv.FormatBool(s.TypeRequireGrouping))
			_ = a.Flags.Set(grouperpass.FlagNameVarRequireSingleVar, strconv.FormatBool(s.VarRequireSingleVar))
			_ = a.Flags.Set(grouperpass.FlagNameVarRequireGrouping, strconv.FormatBool(s.VarRequireGrouping))
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "recvcheck", func(cfg any) []*analysis.Analyzer {
		var rs recvcheckpass.Settings
		if s, ok := cfg.(*config.RecvcheckSettings); ok && s != nil {
			rs.DisableBuiltin = s.DisableBuiltin
			rs.Exclusions = s.Exclusions
		}
		return []*analysis.Analyzer{recvcheckpass.NewAnalyzer(rs)}
	})

	wireNativeFn(c, "usetesting", func(cfg any) []*analysis.Analyzer {
		// Upstream defaults: oschdir, osmkdirtemp, oscreatetemp = true;
		// rest = false. Unconditional Flags.Set from a zero-value
		// settings struct would silently disable the three default-on
		// checks. Apply only when the user explicitly set at least one
		// field — matches landmine 6 (bidichk) and landmine 17.
		a := usetestingpass.NewAnalyzer()
		s, ok := cfg.(*config.UseTestingSettings)
		if !ok || s == nil {
			return []*analysis.Analyzer{a}
		}
		anyNonZero := s.ContextBackground || s.ContextTodo || s.OSChdir ||
			s.OSMkdirTemp || s.OSSetenv || s.OSTempDir || s.OSCreateTemp
		if !anyNonZero {
			return []*analysis.Analyzer{a}
		}
		_ = a.Flags.Set("contextbackground", strconv.FormatBool(s.ContextBackground))
		_ = a.Flags.Set("contexttodo", strconv.FormatBool(s.ContextTodo))
		_ = a.Flags.Set("oschdir", strconv.FormatBool(s.OSChdir))
		_ = a.Flags.Set("osmkdirtemp", strconv.FormatBool(s.OSMkdirTemp))
		_ = a.Flags.Set("ossetenv", strconv.FormatBool(s.OSSetenv))
		_ = a.Flags.Set("ostempdir", strconv.FormatBool(s.OSTempDir))
		_ = a.Flags.Set("oscreatetemp", strconv.FormatBool(s.OSCreateTemp))
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "perfsprint", func(cfg any) []*analysis.Analyzer {
		// Upstream defaults are mostly true (integer-format, error-format,
		// string-format, bool-format, hex-format, concat-loop, errorf,
		// sprintf1, strconcat, int-conversion). Zero-value
		// PerfSprintSettings would silently disable them all. Apply only
		// on any-non-zero.
		a := perfsprintpass.New()
		s, ok := cfg.(*config.PerfSprintSettings)
		if !ok || s == nil {
			return []*analysis.Analyzer{a}
		}
		anyNonZero := s.IntegerFormat || s.IntConversion || s.ErrorFormat ||
			s.ErrError || s.ErrorF || s.StringFormat || s.SprintF1 ||
			s.StrConcat || s.BoolFormat || s.HexFormat || s.ConcatLoop ||
			s.LoopOtherOps
		if !anyNonZero {
			return []*analysis.Analyzer{a}
		}
		_ = a.Flags.Set("integer-format", strconv.FormatBool(s.IntegerFormat))
		_ = a.Flags.Set("int-conversion", strconv.FormatBool(s.IntConversion))
		_ = a.Flags.Set("error-format", strconv.FormatBool(s.ErrorFormat))
		_ = a.Flags.Set("err-error", strconv.FormatBool(s.ErrError))
		_ = a.Flags.Set("errorf", strconv.FormatBool(s.ErrorF))
		_ = a.Flags.Set("string-format", strconv.FormatBool(s.StringFormat))
		_ = a.Flags.Set("sprintf1", strconv.FormatBool(s.SprintF1))
		_ = a.Flags.Set("strconcat", strconv.FormatBool(s.StrConcat))
		_ = a.Flags.Set("bool-format", strconv.FormatBool(s.BoolFormat))
		_ = a.Flags.Set("hex-format", strconv.FormatBool(s.HexFormat))
		_ = a.Flags.Set("concat-loop", strconv.FormatBool(s.ConcatLoop))
		_ = a.Flags.Set("loop-other-ops", strconv.FormatBool(s.LoopOtherOps))
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "decorder", func(cfg any) []*analysis.Analyzer {
		// Upstream decorder defaults every "disable-X" flag to false
		// (i.e. every check enabled). golangci-lint's wrapper flips
		// three to true so the de-facto behavior under v2 configs is:
		// only dec-order (declaration sequence) and the type/const/var
		// num-decl checks fire by default. Mirror the wrapper's
		// defaults; layer the user's overrides on top. Landmine 19.
		a := decorderpass.Analyzer
		// Reset to golangci-lint defaults every Build (the analyzer's
		// global flag set is stable across builds in our engine).
		_ = a.Flags.Set("ignore-underscore-vars", "false")
		_ = a.Flags.Set("disable-dec-num-check", "true")
		_ = a.Flags.Set("disable-type-dec-num-check", "false")
		_ = a.Flags.Set("disable-const-dec-num-check", "false")
		_ = a.Flags.Set("disable-var-dec-num-check", "false")
		_ = a.Flags.Set("disable-dec-order-check", "true")
		_ = a.Flags.Set("disable-init-func-first-check", "true")
		_ = a.Flags.Set("dec-order", "")
		// Layer the user's overrides only when the settings struct is
		// non-zero — a zero struct means "no opinion" and would
		// otherwise silently flip the golangci defaults back to false.
		if s, ok := cfg.(*config.DecorderSettings); ok && s != nil && isDecorderConfigured(s) {
			_ = a.Flags.Set("ignore-underscore-vars", strconv.FormatBool(s.IgnoreUnderscoreVars))
			_ = a.Flags.Set("disable-dec-num-check", strconv.FormatBool(s.DisableDecNumCheck))
			_ = a.Flags.Set("disable-type-dec-num-check", strconv.FormatBool(s.DisableTypeDecNumCheck))
			_ = a.Flags.Set("disable-const-dec-num-check", strconv.FormatBool(s.DisableConstDecNumCheck))
			_ = a.Flags.Set("disable-var-dec-num-check", strconv.FormatBool(s.DisableVarDecNumCheck))
			_ = a.Flags.Set("disable-dec-order-check", strconv.FormatBool(s.DisableDecOrderCheck))
			_ = a.Flags.Set("disable-init-func-first-check", strconv.FormatBool(s.DisableInitFuncFirstCheck))
			if len(s.DecOrder) > 0 {
				_ = a.Flags.Set("dec-order", strings.Join(s.DecOrder, ","))
			}
		}
		// decorder's Run reads only pass.Files (ranges over
		// each file's Decls; for GenDecl tracks token kind / counts and
		// checks declaration order, for FuncDecl checks init-first
		// ordering) and reports via pass.Reportf. No pass.TypesInfo,
		// pass.Pkg, or pass.ResultOf. Classified TypeUseSyntaxOnly.
		// Source-of-truth audit: gitlab.com/bosi/decorder@v0.4.2/analyzer.go.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "protogetter", func(cfg any) []*analysis.Analyzer {
		pc := &protogetterpass.Config{}
		if s, ok := cfg.(*config.ProtoGetterSettings); ok && s != nil {
			pc.SkipGeneratedBy = s.SkipGeneratedBy
			pc.SkipFiles = s.SkipFiles
			pc.SkipAnyGenerated = s.SkipAnyGenerated
			pc.ReplaceFirstArgInAppend = s.ReplaceFirstArgInAppend
		}
		return []*analysis.Analyzer{protogetterpass.NewAnalyzer(pc)}
	})

	wireNativeFn(c, "wrapcheck", func(cfg any) []*analysis.Analyzer {
		// Start from upstream's default config (includes DefaultIgnoreSigs).
		// User-provided IgnoreSigs replaces the default — match the
		// golangci-lint wrapper's "only-if-non-empty" semantics so an
		// empty IgnoreSigs falls back to the upstream default.
		wc := wrapcheckpass.NewDefaultConfig()
		if s, ok := cfg.(*config.WrapcheckSettings); ok && s != nil {
			wc.ExtraIgnoreSigs = s.ExtraIgnoreSigs
			wc.ReportInternalErrors = s.ReportInternalErrors
			if len(s.IgnoreSigs) > 0 {
				wc.IgnoreSigs = s.IgnoreSigs
			}
			if len(s.IgnoreSigRegexps) > 0 {
				wc.IgnoreSigRegexps = s.IgnoreSigRegexps
			}
			if len(s.IgnorePackageGlobs) > 0 {
				wc.IgnorePackageGlobs = s.IgnorePackageGlobs
			}
			if len(s.IgnoreInterfaceRegexps) > 0 {
				wc.IgnoreInterfaceRegexps = s.IgnoreInterfaceRegexps
			}
		}
		return []*analysis.Analyzer{wrapcheckpass.NewAnalyzer(wc)}
	})

	wireNativeFn(c, "tagalign", func(cfg any) []*analysis.Analyzer {
		// Upstream default: align=true, sort=false. Zero-value
		// TagAlignSettings has Align=false, Sort=false. To preserve
		// upstream's default-align behavior, only translate when the
		// user explicitly set at least one field.
		s, ok := cfg.(*config.TagAlignSettings)
		if !ok || s == nil ||
			(!s.Align && !s.Sort && len(s.Order) == 0 && !s.Strict) {
			return []*analysis.Analyzer{tagalignpass.NewAnalyzer()}
		}
		var opts []tagalignpass.Option
		opts = append(opts, tagalignpass.WithAlign(s.Align))
		if s.Sort || len(s.Order) > 0 {
			opts = append(opts, tagalignpass.WithSort(s.Order...))
		}
		if s.Strict && s.Align && s.Sort {
			opts = append(opts, tagalignpass.WithStrictStyle())
		}
		return []*analysis.Analyzer{tagalignpass.NewAnalyzer(opts...)}
	})

	wireNativeFn(c, "sloglint", func(cfg any) []*analysis.Analyzer {
		// sloglint v0.12.0 renamed several Options fields. Our
		// SlogLintSettings YAML keys preserve the older wrapper
		// vocabulary (NoMixedArgs, KVOnly, AttrOnly, NoGlobal,
		// StaticMsg, MsgStyle, NoRawKeys, ArgsOnSepLines) — the
		// translation crosses that rename gap. Landmine 18.
		//
		// Zero-value SlogLintSettings means "no opinion": pass nil so
		// upstream's New() applies its NoMixedArguments=true default.
		s, ok := cfg.(*config.SlogLintSettings)
		if !ok || s == nil || isSlogLintZero(s) {
			return []*analysis.Analyzer{sloglintpass.New(nil)}
		}
		opts := &sloglintpass.Options{
			NoMixedArguments:         s.NoMixedArgs,
			KeyValuePairsOnly:        s.KVOnly,
			AttributesOnly:           s.AttrOnly,
			NoGlobalLogger:           s.NoGlobal,
			ContextOnly:              s.Context,
			StaticMessage:            s.StaticMsg,
			MessageStyle:             s.MsgStyle,
			ConstantKeys:             s.NoRawKeys,
			KeyNamingCase:            s.KeyNamingCase,
			ForbiddenKeys:            s.ForbiddenKeys,
			ArgumentsOnSeparateLines: s.ArgsOnSepLines,
		}
		return []*analysis.Analyzer{sloglintpass.New(opts)}
	})

	wireNativeFn(c, "musttag", func(cfg any) []*analysis.Analyzer {
		var funcs []musttagpass.Func
		if s, ok := cfg.(*config.MustTagSettings); ok && s != nil {
			for _, fn := range s.Functions {
				funcs = append(funcs, musttagpass.Func{
					Name:   fn.Name,
					Tag:    fn.Tag,
					ArgPos: fn.ArgPos,
				})
			}
		}
		return []*analysis.Analyzer{musttagpass.New(funcs...)}
	})

	wireNativeFn(c, "errorlint", func(cfg any) []*analysis.Analyzer {
		// Upstream defaults: comparison=true, asserts=true, errorf=false,
		// errorf-multi=true. Apply Flags.Set only when at least one bool
		// is non-zero (landmine 17). AllowedErrors / AllowedErrorsWildcard
		// route through NewAnalyzer options because they're slice types
		// that don't have a flag-set representation.
		var opts []errorlintpass.Option
		s, sok := cfg.(*config.ErrorLintSettings)
		if sok && s != nil {
			if len(s.AllowedErrors) > 0 {
				opts = append(opts, errorlintpass.WithAllowedErrors(toErrorlintAllowPairs(s.AllowedErrors)))
			}
			if len(s.AllowedErrorsWildcard) > 0 {
				opts = append(opts, errorlintpass.WithAllowedWildcard(toErrorlintAllowPairs(s.AllowedErrorsWildcard)))
			}
		}
		a := errorlintpass.NewAnalyzer(opts...)
		if sok && s != nil {
			anyBoolNonZero := s.Errorf || s.ErrorfMulti || s.Asserts || s.Comparison
			if anyBoolNonZero {
				_ = a.Flags.Set("errorf", strconv.FormatBool(s.Errorf))
				_ = a.Flags.Set("errorf-multi", strconv.FormatBool(s.ErrorfMulti))
				_ = a.Flags.Set("asserts", strconv.FormatBool(s.Asserts))
				_ = a.Flags.Set("comparison", strconv.FormatBool(s.Comparison))
			}
		}
		return []*analysis.Analyzer{a}
	})
}

// isDecorderConfigured reports whether the user provided any non-zero
// decorder setting. Used to gate the layered override path; a zero
// struct means "no opinion" and should leave the golangci defaults
// baseline alone.
func isDecorderConfigured(s *config.DecorderSettings) bool {
	return len(s.DecOrder) > 0 || s.IgnoreUnderscoreVars ||
		s.DisableDecNumCheck || s.DisableTypeDecNumCheck ||
		s.DisableConstDecNumCheck || s.DisableVarDecNumCheck ||
		s.DisableDecOrderCheck || s.DisableInitFuncFirstCheck
}

// isSlogLintZero reports whether every field of SlogLintSettings is
// at the zero value. SlogLintSettings contains a []string slice which
// blocks struct equality, so the check is field-wise.
func isSlogLintZero(s *config.SlogLintSettings) bool {
	return !s.NoMixedArgs && !s.KVOnly && !s.AttrOnly && s.NoGlobal == "" &&
		s.Context == "" && !s.StaticMsg && s.MsgStyle == "" && !s.NoRawKeys &&
		s.KeyNamingCase == "" && len(s.ForbiddenKeys) == 0 && !s.ArgsOnSepLines
}

// toErrorlintAllowPairs converts our config slice into errorlint's
// AllowPair slice. The two structs are structurally identical.
func toErrorlintAllowPairs(in []config.ErrorLintAllowPair) []errorlintpass.AllowPair {
	out := make([]errorlintpass.AllowPair, len(in))
	for i, p := range in {
		out[i] = errorlintpass.AllowPair{Err: p.Err, Fun: p.Fun}
	}
	return out
}
