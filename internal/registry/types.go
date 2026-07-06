// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import "golang.org/x/tools/go/analysis"

// Shape classifies how a linter integrates with plaid-lint's
// engine. The shape decides whether [Resolved.Analyzer] carries a
// usable pointer, whether the linter routes through `formatters`
// instead of `linters`, and whether enabling the linter is permitted
// at all under the current phase.
type Shape int

const (
	// ShapeNative is a pure go/analysis analyzer that already lives
	// in plaid-lint's existing module dependencies (the W7+W8 root
	// set plus the bundled-addable long tail under
	// `golang.org/x/tools/go/analysis/passes` and the honnef
	// simple/stylecheck/quickfix tables). [Resolved.Analyzer] points
	// at a real instance.
	ShapeNative Shape = iota

	// ShapeNativeFamily is a fan-out wrapper: a single user-facing
	// linter name (e.g. `staticcheck`) maps to many `*analysis.Analyzer`
	// pointers (the 95+18+12+35 honnef SA/ST/QF/S tables). Resolution
	// returns one [Resolved] per family member, all sharing the same
	// outer linter name.
	ShapeNativeFamily

	// ShapeRegistryOnly is a known upstream linter that plaid-lint
	// has the catalog entry for but no `*analysis.Analyzer` instance
	// yet — typically because pulling the linter's home module into
	// `go.mod` is a separate PR. Enabling such a linter resolves
	// cleanly (no did-you-mean error) and produces a [Resolved] with
	// [Resolved.Analyzer] == nil and [Resolved.Status] explaining
	// the gap.
	ShapeRegistryOnly

	// ShapeSubprocess is a subprocess-wrapper linter: `unused`,
	// `unparam`, `tracecheck`. Cannot be enabled in Phase 2; the
	// engine refuses with the "deferred to Phase 3" message and the
	// [Resolved] carries [Resolved.Status] == StatusDeferredPhase3.
	ShapeSubprocess

	// ShapeFormatter is a code formatter (gci/gofmt/gofumpt/goimports/
	// golines/swaggo). Enabling it under `linters.enable` is a
	// validation error owned by `config.Linters.Validate`; T2.3
	// surfaces the formatter slot via [Catalog.Formatters] so the
	// "did you mean to put this under formatters.enable?" suggestion
	// can be precise.
	ShapeFormatter
)

// String returns a stable name for the Shape suitable for embedding
// in user-facing diagnostics.
func (s Shape) String() string {
	switch s {
	case ShapeNative:
		return "native"
	case ShapeNativeFamily:
		return "native-family"
	case ShapeRegistryOnly:
		return "registry-only"
	case ShapeSubprocess:
		return "subprocess"
	case ShapeFormatter:
		return "formatter"
	default:
		return "unknown"
	}
}

// Status records why a [Resolved] is or is not runnable. The Status
// is informational — the engine consults it to decide whether to
// install the analyzer in its execution graph.
type Status int

const (
	// StatusEnabled means the linter is runnable and either has an
	// Analyzer pointer attached or is a registry-only entry whose
	// engine wiring will be handled separately (Phase 4 plugins,
	// future native wire-ups).
	StatusEnabled Status = iota

	// StatusDisabled means the linter is present in the catalog but
	// the resolved enable/disable rules removed it from the active
	// set. Returned only by [Registry.All]; [Registry.Enabled] omits
	// it.
	StatusDisabled

	// StatusDeferredPhase3 means the linter is one of the three
	// subprocess-wrapper holdouts and cannot currently be enabled.
	// The engine surfaces a friendly error if the user enables it.
	StatusDeferredPhase3

	// StatusNoAnalyzerWired means the linter is in the upstream
	// catalog but no `*analysis.Analyzer` is wired in plaid-lint
	// yet. Differs from StatusDeferredPhase3 in that there's no
	// architectural blocker — it's a "TODO add the dep" — and the
	// engine emits a warning rather than a hard error.
	StatusNoAnalyzerWired
)

// String returns a stable name for the Status.
func (s Status) String() string {
	switch s {
	case StatusEnabled:
		return "enabled"
	case StatusDisabled:
		return "disabled"
	case StatusDeferredPhase3:
		return "deferred-phase-3"
	case StatusNoAnalyzerWired:
		return "no-analyzer-wired"
	default:
		return "unknown"
	}
}

// Group is one of the v2 default-group names. v1's
// `enable-all`/`disable-all`/`fast` are collapsed by the T2.1
// legacy migrator into these names.
type Group string

const (
	GroupNone     Group = "none"
	GroupStandard Group = "standard"
	GroupFast     Group = "fast"
	GroupAll      Group = "all"
)

// Entry is one row in the static linter catalog. The catalog is
// populated at package-init time and copied (not pointer-shared) into
// each [Registry] returned by [Build] so resolution can adjust
// per-config fields without scribbling on the global.
type Entry struct {
	// Name is the canonical upstream linter name (lowercase, the same
	// string a user puts under `linters.enable`).
	Name string

	// Aliases is the set of names accepted in `linters.enable` that
	// resolve to this entry. Today's catalog uses Aliases for the
	// v1→v2 staticcheck consolidation (`gosimple` and `stylecheck`
	// alias `staticcheck`). Empty for most entries.
	Aliases []string

	// Shape is the integration shape; see [Shape] for the closed set.
	Shape Shape

	// InGroup reports whether this linter is in a named default group.
	// Maps from the four v2 groups to a bool. The catalog defines the
	// upstream membership; [Build] consults it during default-group
	// expansion. Always contains entries for all four groups.
	InGroup map[Group]bool

	// Deprecated, if non-empty, is the deprecation message printed
	// when the linter resolves to enabled. The user-facing behavior
	// is "still works, with a one-line warning" — same as upstream.
	Deprecated string

	// HasGoVersion reports whether this linter's settings sub-block
	// carries a `Go` / `LangVersion` field that wants `Run.Go`
	// propagation. The exact field name is fixed per linter; see
	// [propagateGoVersion].
	HasGoVersion bool

	// AnalyzerFn returns the `*analysis.Analyzer` instance(s) for
	// this entry, given the per-linter config block (a typed pointer
	// into [config.LintersSettings]). For ShapeNative this returns
	// exactly one analyzer; for ShapeNativeFamily it returns many
	// (e.g. the SA-* / ST-* / S-* / QF-* tables for `staticcheck`).
	// nil for ShapeRegistryOnly / ShapeSubprocess / ShapeFormatter.
	//
	// AnalyzerFn is allowed to return nil if the per-linter settings
	// disable every member; e.g. `gocritic.disable-all: true` with
	// no overrides yields an empty slice.
	AnalyzerFn func(cfg any) []*analysis.Analyzer

	// SubprocessWired reports whether a ShapeSubprocess linter has a
	// concrete Runner wired in engine.planFromRegistry. Wired rows
	// resolve to StatusEnabled so the engine fan-out dispatches them;
	// non-wired ShapeSubprocess rows (the historical `unused` /
	// `unparam` / `tracecheck` holdouts) resolve to
	// StatusDeferredPhase3 as before.
	SubprocessWired bool
}

// Resolved is one row of [Registry.Enabled] — the per-linter result of
// applying [config.Config] to the static catalog. One Entry produces
// one or more Resolved rows (Entry.Shape == ShapeNativeFamily fans out;
// every other shape yields a single Resolved).
type Resolved struct {
	// Name is the linter name as it appeared in the user's
	// `linters.enable` / `linters.disable` (canonicalized through
	// alias resolution). For native-family fan-out, every entry in
	// a family shares the same Name.
	Name string

	// Shape mirrors the catalog Entry.Shape.
	Shape Shape

	// Status records why this Resolved is (or isn't) runnable.
	Status Status

	// Analyzer is the `*analysis.Analyzer` instance to wire into the
	// engine. nil for ShapeRegistryOnly / ShapeSubprocess /
	// ShapeFormatter (the engine handles these specially) and for
	// ShapeNativeFamily rows that disabled every check.
	Analyzer *analysis.Analyzer

	// Settings is the typed per-linter settings sub-block from
	// [config.Config.Linters.Settings]. Carried as `any` because the
	// concrete type varies per linter; T2.4 / engine code uses a
	// type-switch keyed on Name to recover the typed shape.
	Settings any

	// Reason, for Status == StatusNoAnalyzerWired or
	// StatusDeferredPhase3, is the human-readable explanation
	// surfaced to the user.
	Reason string
}

// Warning is the structured warning shape T2.1 introduced; the
// registry surfaces its own (e.g. v1 alias usage, deprecated linter
// enable) in the same shape so the caller can pretty-print them
// uniformly.
type Warning struct {
	// Field is a path-like locator (`linters.enable[3]`,
	// `linters.settings.staticcheck.checks`) so callers can point at
	// the offending source location.
	Field string

	// Message is the human-readable warning text. One line, no
	// trailing newline.
	Message string
}
