// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"sort"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/appends"
	"golang.org/x/tools/go/analysis/passes/asmdecl"
	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/atomic"
	"golang.org/x/tools/go/analysis/passes/atomicalign"
	"golang.org/x/tools/go/analysis/passes/bools"
	"golang.org/x/tools/go/analysis/passes/buildtag"
	"golang.org/x/tools/go/analysis/passes/cgocall"
	"golang.org/x/tools/go/analysis/passes/composite"
	"golang.org/x/tools/go/analysis/passes/copylock"
	"golang.org/x/tools/go/analysis/passes/deepequalerrors"
	"golang.org/x/tools/go/analysis/passes/defers"
	"golang.org/x/tools/go/analysis/passes/directive"
	"golang.org/x/tools/go/analysis/passes/errorsas"
	"golang.org/x/tools/go/analysis/passes/fieldalignment"
	"golang.org/x/tools/go/analysis/passes/findcall"
	"golang.org/x/tools/go/analysis/passes/framepointer"
	"golang.org/x/tools/go/analysis/passes/hostport"
	"golang.org/x/tools/go/analysis/passes/httpmux"
	"golang.org/x/tools/go/analysis/passes/httpresponse"
	"golang.org/x/tools/go/analysis/passes/ifaceassert"
	"golang.org/x/tools/go/analysis/passes/loopclosure"
	"golang.org/x/tools/go/analysis/passes/lostcancel"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/nilness"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/reflectvaluecompare"
	"golang.org/x/tools/go/analysis/passes/shadow"
	"golang.org/x/tools/go/analysis/passes/shift"
	"golang.org/x/tools/go/analysis/passes/sigchanyzer"
	"golang.org/x/tools/go/analysis/passes/slog"
	"golang.org/x/tools/go/analysis/passes/sortslice"
	"golang.org/x/tools/go/analysis/passes/stdmethods"
	"golang.org/x/tools/go/analysis/passes/stdversion"
	"golang.org/x/tools/go/analysis/passes/stringintconv"
	"golang.org/x/tools/go/analysis/passes/structtag"
	"golang.org/x/tools/go/analysis/passes/testinggoroutine"
	"golang.org/x/tools/go/analysis/passes/tests"
	"golang.org/x/tools/go/analysis/passes/timeformat"
	"golang.org/x/tools/go/analysis/passes/unmarshal"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/analysis/passes/unsafeptr"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
	"golang.org/x/tools/go/analysis/passes/unusedwrite"
	"golang.org/x/tools/go/analysis/passes/usesgenerics"
	"golang.org/x/tools/go/analysis/passes/waitgroup"

	ineffassignpass "github.com/gordonklaus/ineffassign/pkg/ineffassign"
	ifaceidentical "github.com/uudashr/iface/identical"
	ifaceopaque "github.com/uudashr/iface/opaque"
	ifaceunexported "github.com/uudashr/iface/unexported"
	ifaceunused "github.com/uudashr/iface/unused"
	ifaceunusedmethod "github.com/uudashr/iface/unusedmethod"

	"honnef.co/go/tools/quickfix"
	"honnef.co/go/tools/simple"
	"honnef.co/go/tools/staticcheck"
	"honnef.co/go/tools/stylecheck"

	"github.com/conductorone/plaid-lint/internal/config"
)

// govetSubAnalyzers is the x/tools-shaped sub-analyzer set govet
// exposes under its `enable` map upstream. The list mirrors
// `pkg/lint/lintersdb/builder_linter.go`'s registration of
// "vetcheck" sub-analyzers, modulo:
//
//   - `lostcancel` is included (upstream considers it govet-internal).
//   - `unreachable` is included (same).
//   - `shadow` is included as an opt-in (default-disabled per govet
//     semantics; the user must list it under `govet.enable`).
//   - Sub-analyzers that need cgo / asm (`asmdecl`, `cgocall`,
//     `framepointer`) are included; the engine decides whether to
//     run them based on the target package's content.
//
// Keyed by upstream sub-analyzer name (the YAML token a user puts
// under `govet.enable`).
var govetSubAnalyzers = map[string]*analysis.Analyzer{
	"appends":             appends.Analyzer,
	"asmdecl":             asmdecl.Analyzer,
	"assign":              assign.Analyzer,
	"atomic":              atomic.Analyzer,
	"atomicalign":         atomicalign.Analyzer,
	"bools":               bools.Analyzer,
	"buildtag":            buildtag.Analyzer,
	"cgocall":             cgocall.Analyzer,
	"composites":          composite.Analyzer,
	"copylocks":           copylock.Analyzer,
	"deepequalerrors":     deepequalerrors.Analyzer,
	"defers":              defers.Analyzer,
	"directive":           directive.Analyzer,
	"errorsas":            errorsas.Analyzer,
	"fieldalignment":      fieldalignment.Analyzer,
	"findcall":            findcall.Analyzer,
	"framepointer":        framepointer.Analyzer,
	"hostport":            hostport.Analyzer,
	"httpmux":             httpmux.Analyzer,
	"httpresponse":        httpresponse.Analyzer,
	"ifaceassert":         ifaceassert.Analyzer,
	"loopclosure":         loopclosure.Analyzer,
	"lostcancel":          lostcancel.Analyzer,
	"nilfunc":             nilfunc.Analyzer,
	"nilness":             nilness.Analyzer,
	"printf":              printf.Analyzer,
	"reflectvaluecompare": reflectvaluecompare.Analyzer,
	"shadow":              shadow.Analyzer,
	"shift":               shift.Analyzer,
	"sigchanyzer":         sigchanyzer.Analyzer,
	"slog":                slog.Analyzer,
	"sortslice":           sortslice.Analyzer,
	"stdmethods":          stdmethods.Analyzer,
	"stdversion":          stdversion.Analyzer,
	"stringintconv":       stringintconv.Analyzer,
	"structtag":           structtag.Analyzer,
	"testinggoroutine":    testinggoroutine.Analyzer,
	"tests":               tests.Analyzer,
	"timeformat":          timeformat.Analyzer,
	"unmarshal":           unmarshal.Analyzer,
	"unreachable":         unreachable.Analyzer,
	"unsafeptr":           unsafeptr.Analyzer,
	"unusedresult":        unusedresult.Analyzer,
	"unusedwrite":         unusedwrite.Analyzer,
	"usesgenerics":        usesgenerics.Analyzer,
	"waitgroup":           waitgroup.Analyzer,
}

// govetDefaultEnabled mirrors x/tools' cmd/vet default-on set. These
// sub-analyzers run when `govet` is enabled without an explicit
// `enable` list. Mirrors upstream's defaultAnalyzers in vet/main.go;
// `fieldalignment`, `shadow`, and the cgo/asm trio are NOT in this
// set (default-off, opt-in via `govet.enable`).
var govetDefaultEnabled = []string{
	"appends",
	"asmdecl",
	"assign",
	"atomic",
	"bools",
	"buildtag",
	"cgocall",
	"composites",
	"copylocks",
	"defers",
	"directive",
	"errorsas",
	"framepointer",
	"httpresponse",
	"ifaceassert",
	"loopclosure",
	"lostcancel",
	"nilfunc",
	"printf",
	"shift",
	"sigchanyzer",
	"slog",
	"stdmethods",
	"stringintconv",
	"structtag",
	"testinggoroutine",
	"tests",
	"timeformat",
	"unmarshal",
	"unreachable",
	"unsafeptr",
	"unusedresult",
	"waitgroup",
}

// govetAnalyzers returns the sub-analyzers enabled under the user's
// govet config. Honors the same enable-all / disable-all /
// enable[] / disable[] semantics upstream uses; the rules are
// validated by [config.GovetSettings.Validate] at parse time.
func govetAnalyzers(cfg any) []*analysis.Analyzer {
	gv, _ := cfg.(*config.GovetSettings)
	if gv == nil {
		gv = &config.GovetSettings{}
	}

	// Resolve the active sub-analyzer name set.
	active := make(map[string]bool, len(govetSubAnalyzers))
	switch {
	case gv.EnableAll:
		for n := range govetSubAnalyzers {
			active[n] = true
		}
		for _, n := range gv.Disable {
			delete(active, n)
		}
	case gv.DisableAll:
		for _, n := range gv.Enable {
			active[n] = true
		}
	default:
		for _, n := range govetDefaultEnabled {
			active[n] = true
		}
		for _, n := range gv.Enable {
			active[n] = true
		}
		for _, n := range gv.Disable {
			delete(active, n)
		}
	}

	// Stable output order: alphabetical by sub-analyzer name.
	names := make([]string, 0, len(active))
	for n := range active {
		names = append(names, n)
	}
	sortStrings(names)

	out := make([]*analysis.Analyzer, 0, len(names))
	for _, n := range names {
		if a, ok := govetSubAnalyzers[n]; ok && a != nil {
			out = append(out, a)
		}
	}
	return out
}

// staticcheckAnalyzers returns the honnef analyzers gated by the
// user's `staticcheck.checks` selector. The selector grammar is
// honnef's own (`all`, `none`, `inherit`, glob `SA1*`, negation
// `-SA1019`). Without a config-driven selector the function returns
// every member of the four tables (default "all").
//
// The selector matching is deliberately simple — honnef itself
// re-parses the selector inside each individual SA-* analyzer, so
// passing the full set here and letting honnef filter is faithful
// to upstream. We materialize the analyzer pointers; selector
// evaluation is engine-side.
func staticcheckAnalyzers(cfg any) []*analysis.Analyzer {
	// We deliberately return every staticcheck family member. The
	// `Staticcheck.Checks` selector is honnef-evaluated per-check at
	// run time via each analyzer's Flag set; the registry's job is
	// to surface every member so the engine can wire them. The
	// selector is preserved on [Resolved.Settings] for the engine to
	// thread through.
	all := make([]*analysis.Analyzer, 0,
		len(staticcheck.Analyzers)+len(stylecheck.Analyzers)+
			len(simple.Analyzers)+len(quickfix.Analyzers))
	for _, a := range staticcheck.Analyzers {
		if a.Analyzer != nil {
			all = append(all, a.Analyzer)
		}
	}
	for _, a := range stylecheck.Analyzers {
		if a.Analyzer != nil {
			all = append(all, a.Analyzer)
		}
	}
	for _, a := range simple.Analyzers {
		if a.Analyzer != nil {
			all = append(all, a.Analyzer)
		}
	}
	for _, a := range quickfix.Analyzers {
		if a.Analyzer != nil {
			all = append(all, a.Analyzer)
		}
	}
	// Stable order: alphabetical by analyzer name.
	sortByName(all)
	return all
}

// ifaceSubAnalyzers is the sub-analyzer set `github.com/uudashr/iface`
// exposes under `iface.enable`. Each entry is the package-global
// `Analyzer` variable from the corresponding `uudashr/iface/<name>`
// sub-package. Keyed by the YAML enable-list token (matches the
// upstream Analyzer.Name and golangci-lint's `iface.enable` tokens).
//
// Race surface: each `*analysis.Analyzer` here is a package global
// (`var Analyzer = newAnalyzer()`), and `newAnalyzer` is unexported.
// Same constraint as the govet sub-analyzers — fine for one Build per
// process, race-class with predeclared/nonamedreturns/inamedparam/
// gocognit/maintidx if two Builds ever overlap.
var ifaceSubAnalyzers = map[string]*analysis.Analyzer{
	"identical":    ifaceidentical.Analyzer,
	"opaque":       ifaceopaque.Analyzer,
	"unexported":   ifaceunexported.Analyzer,
	"unused":       ifaceunused.Analyzer,
	"unusedmethod": ifaceunusedmethod.Analyzer,
}

// ifaceDefaultEnabled mirrors upstream's default-on set. Per the
// uudashr/iface README: "By default it only enables the `identical`
// analyzer". The other four (unused, unusedmethod, opaque, unexported)
// are opt-in via `iface.enable`.
var ifaceDefaultEnabled = []string{
	"identical",
}

// ifaceAnalyzers returns the sub-analyzers enabled under the user's
// iface config. IfaceSettings has only an `Enable []string` list (no
// enable-all/disable-all — the surface is narrower than govet's): an
// empty list falls back to [ifaceDefaultEnabled]; any non-empty list
// is taken as the exact active set.
//
// IfaceSettings.Settings (`map[string]map[string]any`) is intentionally
// not translated to Flags.Set here. The sub-analyzers expose only the
// `nerd` (debug) and `exclude` (string, unused/unusedmethod) flags;
// neither is currently surfaced through the typed settings API. The
// raw map is still carried on [Resolved.Settings] for the engine to
// thread through if a future setting needs it.
func ifaceAnalyzers(cfg any) []*analysis.Analyzer {
	is, _ := cfg.(*config.IfaceSettings)
	if is == nil {
		is = &config.IfaceSettings{}
	}

	active := make(map[string]bool, len(ifaceSubAnalyzers))
	if len(is.Enable) == 0 {
		for _, n := range ifaceDefaultEnabled {
			active[n] = true
		}
	} else {
		for _, n := range is.Enable {
			active[n] = true
		}
	}

	names := make([]string, 0, len(active))
	for n := range active {
		names = append(names, n)
	}
	sortStrings(names)

	out := make([]*analysis.Analyzer, 0, len(names))
	for _, n := range names {
		if a, ok := ifaceSubAnalyzers[n]; ok && a != nil {
			out = append(out, a)
		}
	}
	return out
}

// wireAnalyzerFns attaches the AnalyzerFn closure to every catalog
// entry whose Shape is ShapeNative or ShapeNativeFamily. Entries
// without an explicit wire stay nil, which is the correct behavior
// for ShapeRegistryOnly / ShapeSubprocess / ShapeFormatter.
func wireAnalyzerFns(c *catalog) {
	wireFn(c, "errcheck", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{errcheckAnalyzer()}
	})
	wireFn(c, "ineffassign", func(_ any) []*analysis.Analyzer {
		return []*analysis.Analyzer{ineffassignpass.Analyzer}
	})
	wireFn(c, "govet", govetAnalyzers)
	wireFn(c, "staticcheck", staticcheckAnalyzers)
	wireFn(c, "iface", ifaceAnalyzers)
	// typecheck is engine-internal: no AnalyzerFn. The engine
	// surfaces parse/type errors directly; the catalog entry exists
	// so `linters.disable: typecheck` semantics resolve.

	// Long-tail wirings — each batch flips a handful of
	// ShapeRegistryOnly rows to ShapeNative.
	wireAnalyzerFnsBatch1(c)
	wireAnalyzerFnsBatch2(c)
	wireAnalyzerFnsBatch3(c)
	wireAnalyzerFnsBatch4(c)
	wireAnalyzerFnsBatch5(c)
	wireAnalyzerFnsBatch6(c)
	wireAnalyzerFnsWrapBatch(c)
	wireAnalyzerFnsPolyBatchA(c)
	wireAnalyzerFnsGocritic(c)
	wireAnalyzerFnsRevive(c)
	wireAnalyzerFnsTracecheck(c)
	wireAnalyzerFnsCleanup(c)

	// Tier 1 inline ports — three subprocess Runners replaced
	// by in-process *analysis.Analyzer implementations. See
	// wire_<linter>_native.go.
	wireAnalyzerFnsGochecknoinitsNative(c)
	wireAnalyzerFnsDogsledNative(c)
	wireAnalyzerFnsLllNative(c)

	// Tier 2 library-wrap ports — four subprocess Runners
	// replaced by in-process *analysis.Analyzer implementations that
	// wrap the upstream linter libraries directly. See
	// wire_<linter>_native.go.
	wireAnalyzerFnsGodoxNative(c)
	wireAnalyzerFnsGocycloNative(c)
	wireAnalyzerFnsNestifNative(c)
	wireAnalyzerFnsUnconvertNative(c)

	// Tier 3 library-wrap ports — two subprocess Runners
	// (unused/U1000 and unparam) replaced by in-process Analyzers
	// wrapping the upstream honnef.co/go/tools/unused and
	// mvdan.cc/unparam/check libraries. See wire_<linter>_native.go.
	wireAnalyzerFnsUnusedNative(c)
	wireAnalyzerFnsUnparamNative(c)

	// dupl port — subprocess Runner replaced by in-process
	// per-pass Analyzer backed by github.com/golangci/dupl/lib.
	wireAnalyzerFnsDuplNative(c)

	// Whitespace re-implementation with per-file
	// line-offset cache. Replaces the upstream wire in batch2.
	wireAnalyzerFnsWhitespaceNative(c)
}

// wireFn sets fn as the AnalyzerFn on the named entry. Panics if the
// name is missing or the entry's Shape is incompatible with an
// analyzer wire-up; both are programming errors during catalog init.
func wireFn(c *catalog, name string, fn func(any) []*analysis.Analyzer) {
	e, ok := c.resolve(name)
	if !ok {
		panic("registry: wireFn: missing catalog entry " + name)
	}
	if e.Shape != ShapeNative && e.Shape != ShapeNativeFamily {
		panic("registry: wireFn: entry " + name + " is not a native shape")
	}
	e.AnalyzerFn = fn
}

func sortStrings(s []string) { sort.Strings(s) }

func sortByName(s []*analysis.Analyzer) {
	sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
}
