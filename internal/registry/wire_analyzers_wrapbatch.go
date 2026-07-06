// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"sync"

	sumtypepass "github.com/alecthomas/go-check-sumtype"
	preallocpass "github.com/alexkohler/prealloc/pkg"
	godoclintcompose "github.com/godoc-lint/godoc-lint/pkg/compose"
	godoclintconfig "github.com/godoc-lint/godoc-lint/pkg/config"
	misspellpass "github.com/golangci/misspell"
	goconstpass "github.com/jgautheron/goconst"
	gomoddirectivespass "github.com/ldez/gomoddirectives"
	gomodguardpass "github.com/ryancurrah/gomodguard"
	godotpass "github.com/tetafro/godot"
	promlinterpass "github.com/yeya24/promlinter"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/conductorone/plaid-lint/internal/analyzers"
	"github.com/conductorone/plaid-lint/internal/config"
)

// wireAnalyzerFnsWrapBatch attaches AnalyzerFns for the wrap-pattern
// batch. Nine entries whose upstream packages publish a library API
// but never wrap it in `*analysis.Analyzer`. Each wiring constructs an
// inline `&analysis.Analyzer{Run: …}` whose closure adapts the upstream
// shape to `*analysis.Pass`, then translates the upstream diagnostic
// shape back to `pass.Report`.
//
// Four wrap sub-shapes encountered (codified in the playbook as the
// landmine-14 expansion):
//
//   - *Pass-direct: upstream takes `*analysis.Pass` and calls
//     `pass.Report` itself (prealloc, gomoddirectives via AnalyzePass).
//     The wrap is one closure line.
//
//   - []*packages.Package reconstruction: upstream takes a `*packages.Package`
//     slice or analog, we synthesize one from `pass.Fset/Files/Pkg/TypesInfo`
//     (gochecksumtype). Translate upstream errors back to `pass.Report`.
//
//   - Library-only: upstream exposes a per-file or per-input library that
//     returns `[]Issue` with `token.Position`. We loop over `pass.Files`,
//     call the library, and translate Position to token.Pos via
//     `posFromPosition` (godot, promlinter, goconst, misspell).
//
//   - Custom-Analyzer-adapter: upstream wraps its own analyzer behind an
//     interface; we extract the underlying `*analysis.Analyzer` via the
//     interface's GetAnalyzer accessor (godoclint).
//
// For per-Build-once work (gomoddirectives reads go.mod, gomodguard
// constructs a Processor) the closure uses `sync.Once` so a multi-pass
// invocation doesn't re-do the expensive setup. Each per-linter Once
// lives inside the AnalyzerFn closure, so re-wiring (a new Build) gets a
// fresh Once — there's no cross-Build state leak.
func wireAnalyzerFnsWrapBatch(c *catalog) {
	wireNativeFn(c, "prealloc", func(cfg any) []*analysis.Analyzer {
		// *Pass-direct shape. prealloc.Check calls pass.Report internally;
		// no diagnostic translation needed.
		s, _ := cfg.(*config.PreallocSettings)
		simple, rangeLoops, forLoops := false, false, false
		if s != nil {
			simple = s.Simple
			rangeLoops = s.RangeLoops
			forLoops = s.ForLoops
		}
		a := &analysis.Analyzer{
			Name: "prealloc",
			Doc:  "Find slice declarations that could potentially be pre-allocated.",
			Run: func(pass *analysis.Pass) (any, error) {
				preallocpass.Check(pass, simple, rangeLoops, forLoops)
				return nil, nil
			},
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "gochecksumtype", func(cfg any) []*analysis.Analyzer {
		// []*packages.Package reconstruction shape. We synthesize a single
		// packages.Package per pass from the pass's Fset/Files/Pkg/TypesInfo
		// and translate gochecksumtype.Error to pass.Report.
		var sc sumtypepass.Config
		if s, ok := cfg.(*config.GoChecksumTypeSettings); ok && s != nil {
			sc.DefaultSignifiesExhaustive = s.DefaultSignifiesExhaustive
			sc.IncludeSharedInterfaces = s.IncludeSharedInterfaces
		}
		a := &analysis.Analyzer{
			Name: "gochecksumtype",
			Doc:  `Run exhaustiveness checks on Go "sum types".`,
			Run: func(pass *analysis.Pass) (any, error) {
				pkg := &packages.Package{
					Fset:      pass.Fset,
					Syntax:    pass.Files,
					Types:     pass.Pkg,
					TypesInfo: pass.TypesInfo,
				}
				for _, err := range sumtypepass.Run([]*packages.Package{pkg}, sc) {
					sterr, ok := err.(sumtypepass.Error)
					if !ok {
						continue
					}
					msg := strings.TrimPrefix(sterr.Error(), sterr.Pos().String()+": ")
					pass.Report(analysis.Diagnostic{
						Pos:     posFromPosition(pass, sterr.Pos()),
						Message: msg,
					})
				}
				return nil, nil
			},
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "misspell", func(cfg any) []*analysis.Analyzer {
		// Library-only shape. Replacer is built at wire-time (compiles
		// the dictionary once per Build); the closure runs Replace per
		// file and translates Diff to pass.Report. If the user's locale
		// or extra-words list is malformed, the linter degrades to a
		// no-op (nil Analyzer) rather than panicking.
		s, _ := cfg.(*config.MisspellSettings)
		replacer, mode, err := buildMisspellReplacer(s)
		if err != nil || replacer == nil {
			return nil
		}
		a := &analysis.Analyzer{
			Name: "misspell",
			Doc:  "Finds commonly misspelled English words.",
			Run: func(pass *analysis.Pass) (any, error) {
				for _, file := range pass.Files {
					tf := pass.Fset.File(file.Pos())
					if tf == nil {
						continue
					}
					contents, rerr := pass.ReadFile(tf.Name())
					if rerr != nil {
						continue
					}
					var diffs []misspellpass.Diff
					if strings.EqualFold(mode, "restricted") {
						_, diffs = replacer.ReplaceGo(string(contents))
					} else {
						_, diffs = replacer.Replace(string(contents))
					}
					for _, diff := range diffs {
						if diff.Line < 1 || diff.Line > tf.LineCount() {
							continue
						}
						start := tf.LineStart(diff.Line) + token.Pos(diff.Column)
						end := start + token.Pos(len(diff.Original))
						pass.Report(analysis.Diagnostic{
							Pos:     start,
							End:     end,
							Message: "`" + diff.Original + "` is a misspelling of `" + diff.Corrected + "`",
							SuggestedFixes: []analysis.SuggestedFix{{
								TextEdits: []analysis.TextEdit{{
									Pos:     start,
									End:     end,
									NewText: []byte(diff.Corrected),
								}},
							}},
						})
					}
				}
				return nil, nil
			},
		}
		// misspell's Run iterates pass.Files, reads each
		// file via pass.ReadFile (workspace-relative), runs the
		// dictionary string-replacer on the contents, and reports.
		// No pass.TypesInfo, pass.Pkg, or pass.ResultOf access.
		// Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "gomoddirectives", func(cfg any) []*analysis.Analyzer {
		// *Pass-direct shape via AnalyzePass. Wrapped in sync.Once because
		// go.mod is module-global, not per-package; running once per
		// Build is sufficient and avoids duplicate diagnostics across
		// passes.
		var opts gomoddirectivespass.Options
		if s, ok := cfg.(*config.GoModDirectivesSettings); ok && s != nil {
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
		}
		var once sync.Once
		a := &analysis.Analyzer{
			Name: "gomoddirectives",
			Doc:  "Manage the use of 'replace', 'retract', and 'excludes' directives in go.mod.",
			Run: func(pass *analysis.Pass) (any, error) {
				once.Do(func() {
					results, err := gomoddirectivespass.AnalyzePass(pass, opts)
					if err != nil {
						return
					}
					for _, r := range results {
						pass.Report(analysis.Diagnostic{
							Pos:     posFromPosition(pass, r.Start),
							Message: r.Reason,
						})
					}
				})
				return nil, nil
			},
		}
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "godot", func(cfg any) []*analysis.Analyzer {
		// Library-only shape. godot.Run takes (file, fset, settings) and
		// returns []Issue; loop over pass.Files and translate Position to
		// token.Pos with line-end spans (matching golangci-lint's wrap).
		var gs godotpass.Settings
		if s, ok := cfg.(*config.GodotSettings); ok && s != nil {
			gs.Scope = godotpass.Scope(s.Scope)
			gs.Exclude = s.Exclude
			gs.Period = s.Period
			gs.Capital = s.Capital
		}
		if gs.Scope == "" {
			gs.Scope = godotpass.DeclScope
		}
		a := &analysis.Analyzer{
			Name: "godot",
			Doc:  "Check if comments end in a period.",
			Run: func(pass *analysis.Pass) (any, error) {
				for _, file := range pass.Files {
					issues, err := godotpass.Run(file, pass.Fset, gs)
					if err != nil {
						continue
					}
					tf := pass.Fset.File(file.Pos())
					if tf == nil {
						continue
					}
					for _, iss := range issues {
						start := tf.Pos(iss.Pos.Offset)
						end := endOfLinePos(tf, iss.Pos.Line)
						pass.Report(analysis.Diagnostic{
							Pos:     start,
							End:     end,
							Message: iss.Message,
							SuggestedFixes: []analysis.SuggestedFix{{
								TextEdits: []analysis.TextEdit{{
									Pos:     start,
									End:     end,
									NewText: []byte(iss.Replacement),
								}},
							}},
						})
					}
				}
				return nil, nil
			},
		}
		// godot's Run reads pass.Files + pass.Fset and
		// delegates to godotpass.Run(file, fset, settings) whose
		// signature precludes any pass.TypesInfo / pass.Pkg / Requires
		// access by construction. Classified TypeUseSyntaxOnly.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "promlinter", func(cfg any) []*analysis.Analyzer {
		// Library-only shape. RunLint(fset, files, setting) returns
		// []Issue with token.Position; per-pass call, per-issue
		// pass.Report translation.
		var ps promlinterpass.Setting
		if s, ok := cfg.(*config.PromlinterSettings); ok && s != nil {
			ps.Strict = s.Strict
			ps.DisabledLintFuncs = s.DisabledLinters
		}
		a := &analysis.Analyzer{
			Name: "promlinter",
			Doc:  "Check Prometheus metrics naming via promlint.",
			Run: func(pass *analysis.Pass) (any, error) {
				issues := promlinterpass.RunLint(pass.Fset, pass.Files, ps)
				for _, iss := range issues {
					pass.Report(analysis.Diagnostic{
						Pos:     posFromPosition(pass, iss.Pos),
						Message: "Metric: " + iss.Metric + " Error: " + iss.Text,
					})
				}
				return nil, nil
			},
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "godoclint", func(cfg any) []*analysis.Analyzer {
		// Custom-Analyzer-adapter shape. godoclint composes its analyzer
		// through pkg/compose.Compose; the returned Composition exposes
		// the runtime *analysis.Analyzer via model.Analyzer.GetAnalyzer().
		// Settings are owned by upstream via PlainConfig — we map our
		// nested GodoclintSettings into PlainRuleOptions field-by-field.
		var pcfg godoclintconfig.PlainConfig
		if s, ok := cfg.(*config.GodoclintSettings); ok && s != nil && godoclintConfigured(s) {
			pcfg = godoclintconfig.PlainConfig{
				Default: s.Default,
				Enable:  s.Enable,
				Disable: s.Disable,
				Options: &godoclintconfig.PlainRuleOptions{
					MaxLenLength:                     s.Options.MaxLen.Length,
					MaxLenIncludeTests:               boolPtr(true),
					PkgDocIncludeTests:               boolPtr(false),
					SinglePkgDocIncludeTests:         boolPtr(true),
					RequirePkgDocIncludeTests:        boolPtr(false),
					RequireDocIncludeTests:           boolPtr(true),
					RequireDocIgnoreExported:         s.Options.RequireDoc.IgnoreExported,
					RequireDocIgnoreUnexported:       s.Options.RequireDoc.IgnoreUnexported,
					StartWithNameIncludeTests:        boolPtr(false),
					StartWithNameIncludeUnexported:   s.Options.StartWithName.IncludeUnexported,
					RequireStdlibDoclinkIncludeTests: boolPtr(true),
					NoUnusedLinkIncludeTests:         boolPtr(true),
				},
			}
		}
		composition := godoclintcompose.Compose(godoclintcompose.CompositionConfig{
			BaseDirPlainConfig: &pcfg,
			ExitFunc:           func(int, error) {},
		})
		if composition == nil || composition.Analyzer == nil {
			return nil
		}
		a := composition.Analyzer.GetAnalyzer()
		if a == nil {
			return nil
		}
		// godoclint's Run (analysis/analyzer.go:58-85)
		// reads pass.Files, pass.Fset, pass.ReadFile, and
		// pass.ResultOf[<own internal inspector analyzer>] — its
		// own inspector (pkg/inspect/inspector.go) walks pass.Files
		// over ast.File / file.Comments to build FileInspection maps;
		// no pass.TypesInfo, pass.Pkg, types.Object, or types.Type
		// reads anywhere in pkg/check/*/*.go. Reports via pass.Report*.
		// Note: pkg/check/stdlib_doclink/internal/gen/main.go does
		// touch pass.Pkg but that file is a code-generator tool, not
		// the analyzer runtime. Classified TypeUseSyntaxOnly.
		// Source-of-truth audit:
		// github.com/godoc-lint/godoc-lint@v0.11.2/pkg/{analysis,inspect,check}.
		return []*analysis.Analyzer{analyzers.RegisterSyntaxOnly(a, 1)}
	})

	wireNativeFn(c, "goconst", func(cfg any) []*analysis.Analyzer {
		// Library-only shape. goconst.Run takes (files, fset, info, *Config)
		// and returns []Issue with token.Position.
		//
		// Defaults (MinStringLen=3, MinOccurrencesCount=3, NumberMin=3,
		// NumberMax=3, MatchWithConstants=true, IgnoreCalls=true) are
		// injected by config.applyLinterSettingsDefaults during config
		// load — see internal/config/config.go. The library's own zero
		// values would otherwise cause every string with >=0 occurrences
		// to fire.
		gc := &goconstpass.Config{}
		if s, ok := cfg.(*config.GoConstSettings); ok && s != nil {
			gc.IgnoreStrings = s.IgnoreStringValues
			gc.MatchWithConstants = s.MatchWithConstants
			gc.MinStringLength = s.MinStringLen
			gc.MinOccurrences = s.MinOccurrencesCount
			gc.ParseNumbers = s.ParseNumbers
			gc.NumberMin = s.NumberMin
			gc.NumberMax = s.NumberMax
			gc.FindDuplicates = s.FindDuplicates
			gc.EvalConstExpressions = s.EvalConstExpressions
			// IgnoreCalls excludes string literals passed as function-call
			// arguments — a noisy class of false positives ("foo" used
			// across ten log.Info("foo", ...) sites). Upstream wires this
			// through ExcludeTypes[Call]=true; mirror exactly.
			if s.IgnoreCalls {
				if gc.ExcludeTypes == nil {
					gc.ExcludeTypes = map[goconstpass.Type]bool{}
				}
				gc.ExcludeTypes[goconstpass.Call] = true
			}
		}
		// goconst v1.10 added a CompositeLit visitor that fires on
		// strings inside `[]string{...}`, `map[string]string{...}`, and
		// struct-literal fields. golangci-lint v2.9 pins goconst v1.8
		// which has no such visitor, so for diagnostic parity we mask
		// CompositeLit by default. Removing this exclusion would
		// surface roughly 1.5K extra diagnostics on c1 that upstream
		// silently drops.
		if gc.ExcludeTypes == nil {
			gc.ExcludeTypes = map[goconstpass.Type]bool{}
		}
		gc.ExcludeTypes[goconstpass.CompositeLit] = true
		a := &analysis.Analyzer{
			Name: "goconst",
			Doc:  "Finds repeated strings that could be replaced by a constant.",
			Run: func(pass *analysis.Pass) (any, error) {
				issues, err := goconstpass.Run(pass.Files, pass.Fset, pass.TypesInfo, gc)
				if err != nil {
					// Tolerate upstream parse failure; surface no diagnostics.
					return nil, nil
				}
				for _, iss := range issues {
					var text string
					switch {
					case iss.OccurrencesCount > 0:
						text = "string `" + iss.Str + "` has " + strconv.Itoa(iss.OccurrencesCount) + " occurrences"
						if iss.MatchingConst == "" {
							text += ", make it a constant"
						} else {
							text += ", but such constant `" + iss.MatchingConst + "` already exists"
						}
					case iss.DuplicateConst != "":
						text = "This constant is a duplicate of `" + iss.DuplicateConst + "` at " + iss.DuplicatePos.String()
					default:
						continue
					}
					pass.Report(analysis.Diagnostic{
						Pos:     posFromPosition(pass, iss.Pos),
						Message: text,
					})
				}
				return nil, nil
			},
		}
		return []*analysis.Analyzer{a}
	})

	wireNativeFn(c, "gomodguard", func(cfg any) []*analysis.Analyzer {
		// Library-only shape with one-time Processor setup. ProcessFiles
		// takes filenames (resolved per-pass) and returns []Issue with
		// token.Position. Processor is built lazily inside sync.Once so
		// startup failures (no modfile, etc.) degrade to silent no-op
		// rather than panicking.
		processorCfg := &gomodguardpass.Configuration{}
		if s, ok := cfg.(*config.GoModGuardSettings); ok && s != nil {
			processorCfg.Allowed.Modules = s.Allowed.Modules
			processorCfg.Allowed.Domains = s.Allowed.Domains
			processorCfg.Blocked.LocalReplaceDirectives = s.Blocked.LocalReplaceDirectives
			for _, m := range s.Blocked.Modules {
				row := map[string]gomodguardpass.BlockedModule{}
				for k, v := range m {
					row[k] = gomodguardpass.BlockedModule{
						Recommendations: v.Recommendations,
						Reason:          v.Reason,
					}
				}
				processorCfg.Blocked.Modules = append(processorCfg.Blocked.Modules, row)
			}
			for _, v := range s.Blocked.Versions {
				row := map[string]gomodguardpass.BlockedVersion{}
				for k, ver := range v {
					row[k] = gomodguardpass.BlockedVersion{
						Version: ver.Version,
						Reason:  ver.Reason,
					}
				}
				processorCfg.Blocked.Versions = append(processorCfg.Blocked.Versions, row)
			}
		}
		var (
			once      sync.Once
			processor *gomodguardpass.Processor
		)
		a := &analysis.Analyzer{
			Name: "gomodguard",
			Doc:  "Allow and block list linter for direct Go module dependencies.",
			Run: func(pass *analysis.Pass) (any, error) {
				once.Do(func() {
					p, err := gomodguardpass.NewProcessor(processorCfg)
					if err != nil {
						return
					}
					processor = p
				})
				if processor == nil {
					return nil, nil
				}
				var filenames []string
				for _, file := range pass.Files {
					tf := pass.Fset.File(file.Pos())
					if tf == nil {
						continue
					}
					filenames = append(filenames, tf.Name())
				}
				if len(filenames) == 0 {
					return nil, nil
				}
				for _, iss := range processor.ProcessFiles(filenames) {
					pass.Report(analysis.Diagnostic{
						Pos:     posFromPosition(pass, iss.Position),
						Message: iss.Reason,
					})
				}
				return nil, nil
			},
		}
		return []*analysis.Analyzer{a}
	})
}

// posFromPosition resolves a token.Position back to a token.Pos in the
// pass's FileSet by looking up the token.File whose name matches
// pos.Filename. Returns token.NoPos when the file is not in the pass
// (e.g. position points at go.mod for gomoddirectives) or when the line
// is out of range. Diagnostics emitted with NoPos still surface — the
// engine displays them at file:0 — but pos round-trips are best-effort.
func posFromPosition(pass *analysis.Pass, pos token.Position) token.Pos {
	if !pos.IsValid() {
		return token.NoPos
	}
	var found *token.File
	pass.Fset.Iterate(func(f *token.File) bool {
		if f.Name() == pos.Filename {
			found = f
			return false
		}
		return true
	})
	if found == nil {
		return token.NoPos
	}
	if pos.Line < 1 || pos.Line > found.LineCount() {
		return token.NoPos
	}
	off := token.Pos(pos.Column - 1)
	if pos.Column < 1 {
		off = 0
	}
	return found.LineStart(pos.Line) + off
}

// endOfLinePos returns the token.Pos just before the next line's start.
// Mirrors golangci-lint's goanalysis.EndOfLinePos for godot's
// line-ending diagnostic spans.
func endOfLinePos(f *token.File, line int) token.Pos {
	if line >= f.LineCount() {
		return f.Pos(f.Size())
	}
	return f.LineStart(line+1) - token.Pos(1)
}

// boolPtr returns a pointer to v. Used by godoclint's PlainRuleOptions
// fields, which take *bool to distinguish "false" from "unset".
func boolPtr(v bool) *bool { return &v }

// buildMisspellReplacer compiles the misspell dictionary from the
// settings sub-block. Returns the Replacer plus the chosen mode
// ("default" or "restricted"), or nil on settings-malformed paths
// (unknown locale, malformed extra-words). Mirrors golangci-lint's
// createMisspellReplacer, except we tolerate errors silently rather
// than fatal-logging.
func buildMisspellReplacer(s *config.MisspellSettings) (*misspellpass.Replacer, string, error) {
	r := &misspellpass.Replacer{Replacements: misspellpass.DictMain}
	mode := ""
	if s != nil {
		switch strings.ToUpper(s.Locale) {
		case "":
			// nothing
		case "US":
			r.AddRuleList(misspellpass.DictAmerican)
		case "UK", "GB":
			r.AddRuleList(misspellpass.DictBritish)
		default:
			return nil, "", errUnsupportedLocale{locale: s.Locale}
		}
		if len(s.ExtraWords) > 0 {
			extra := make([]string, 0, len(s.ExtraWords)*2)
			for _, w := range s.ExtraWords {
				if w.Typo == "" || w.Correction == "" {
					continue
				}
				extra = append(extra, strings.ToLower(w.Typo), strings.ToLower(w.Correction))
			}
			r.AddRuleList(extra)
		}
		if len(s.IgnoreRules) > 0 {
			r.RemoveRule(s.IgnoreRules)
		}
		mode = s.Mode
	}
	r.Compile()
	return r, mode, nil
}

type errUnsupportedLocale struct{ locale string }

func (e errUnsupportedLocale) Error() string { return "misspell: unknown locale: " + e.locale }

// godoclintConfigured reports whether the user supplied any non-zero
// godoclint settings. When false, we still construct an empty
// PlainConfig — godoclint's defaults are the desired baseline.
func godoclintConfigured(s *config.GodoclintSettings) bool {
	if s.Default != nil || len(s.Enable) > 0 || len(s.Disable) > 0 {
		return true
	}
	if s.Options.MaxLen.Length != nil {
		return true
	}
	if s.Options.RequireDoc.IgnoreExported != nil || s.Options.RequireDoc.IgnoreUnexported != nil {
		return true
	}
	if s.Options.StartWithName.IncludeUnexported != nil {
		return true
	}
	return false
}
