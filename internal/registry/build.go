// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/conductorone/plaid-lint/internal/config"
)

// Registry is the post-resolution view of which linters the user's
// [config.Config] selects. Construct via [Build] (or
// [BuildFromConfig], the alias for the brief's spelling).
//
// The returned Registry is immutable; callers may read
// [Registry.Enabled] and [Registry.All] freely from multiple
// goroutines.
type Registry struct {
	// catalog is the underlying linter catalog used for resolution.
	// Always non-nil; defaults to the package-global catalog when
	// [Build] is called without a [WithCatalog] override.
	catalog *catalog

	// resolved is the per-linter outcome in stable alphabetical
	// order by linter name. Each Entry produces one Resolved row,
	// except ShapeNativeFamily entries which fan out across their
	// member analyzers (one Resolved per family member).
	resolved []Resolved

	// enabled is the subset of resolved with Status == StatusEnabled.
	// Materialized once at Build time so [Registry.Enabled] doesn't
	// re-walk.
	enabled []Resolved

	// warnings is the structured warning surface ([Build] users
	// pretty-print these the same way T2.1's [config.Warning] is
	// pretty-printed).
	warnings []Warning
}

// Build resolves cfg against the package-global catalog. Mutates the
// caller's *config.Config to apply the per-linter Go-version
// propagation and the gosimple/stylecheck→staticcheck checks
// consolidation, so a subsequent inspection of cfg sees the
// post-resolution shape. (Mutating cfg matches upstream's
// `Loader.handleGoVersion` behavior; the alternative — returning a
// modified Config copy — would let two callers see disagreeing
// shapes of "the same" config.)
//
// Returns an error only if the catalog cannot resolve a linter name
// (run [Validate] first to surface those uniformly).
func Build(cfg *config.Config) (*Registry, []Warning, error) {
	return buildWith(cfg, defaultCatalog)
}

// BuildFromConfig is an alias for [Build] matching the brief's
// spelling.
func BuildFromConfig(cfg *config.Config) (*Registry, []Warning, error) {
	return Build(cfg)
}

// buildWith is the test seam — [Build] is the production entry point
// that uses the package-global catalog; tests construct an isolated
// catalog and pass it here to verify alternate-catalog behavior.
func buildWith(cfg *config.Config, cat *catalog) (*Registry, []Warning, error) {
	if cfg == nil {
		cfg = config.NewDefault()
	}
	r := &Registry{catalog: cat}

	// 1. v1 staticcheck consolidation. Runs first because the rest
	//    of the resolution consumes the merged Staticcheck.Checks
	//    selector.
	if w := consolidateStaticcheckChecks(cfg); w != nil {
		r.warnings = append(r.warnings, *w)
	}

	// 2. Validate against the catalog — same path [Validate] uses but
	//    inlined here so we can surface a typed error.
	if errs := validateAgainstCatalog(cfg, cat); len(errs) > 0 {
		// Surface the first error; callers wanting the full set use
		// the standalone [Validate].
		return nil, r.warnings, errs[0]
	}

	// 3. Resolve enable/disable rules into the active linter set.
	active, activeWarnings := resolveActiveSet(cfg, cat)
	r.warnings = append(r.warnings, activeWarnings...)

	// 4. Per-linter Go-version fan-out. Mutates cfg in place.
	propagateGoVersion(cfg, active)

	// 5. Build the Resolved slice from active.
	r.resolved = make([]Resolved, 0, len(active))
	for _, name := range sortedNames(active) {
		e := active[name]
		settings := perLinterSettings(cfg, e.Name)
		switch e.Shape {
		case ShapeSubprocess:
			// Wired subprocess rows (Entry.SubprocessWired) resolve to
			// StatusEnabled so engine.planFromRegistry dispatches them.
			// Non-wired ShapeSubprocess holdouts (`unused`, `unparam`,
			// `tracecheck`) remain StatusDeferredPhase3.
			if e.SubprocessWired {
				r.resolved = append(r.resolved, Resolved{
					Name:     e.Name,
					Shape:    e.Shape,
					Status:   StatusEnabled,
					Settings: settings,
				})
			} else {
				r.resolved = append(r.resolved, Resolved{
					Name:     e.Name,
					Shape:    e.Shape,
					Status:   StatusDeferredPhase3,
					Settings: settings,
					Reason:   "deferred to Phase 3 (subprocess wrapper)",
				})
			}
		case ShapeFormatter:
			// Formatters in the active set are an error already
			// surfaced by config.Linters.Validate; if we got here, the
			// user actively listed it under linters.enable. Mark
			// disabled so the engine ignores it.
			r.resolved = append(r.resolved, Resolved{
				Name:     e.Name,
				Shape:    e.Shape,
				Status:   StatusDisabled,
				Settings: settings,
				Reason:   "formatters belong under formatters.enable; this linter row is informational",
			})
		case ShapeRegistryOnly:
			r.resolved = append(r.resolved, Resolved{
				Name:     e.Name,
				Shape:    e.Shape,
				Status:   StatusNoAnalyzerWired,
				Settings: settings,
				Reason:   "no *analysis.Analyzer wired in plaid-lint yet; add the module dep + AnalyzerFn",
			})
		case ShapeNative, ShapeNativeFamily:
			if e.AnalyzerFn == nil {
				// typecheck is the canonical example: it's
				// ShapeNative because the engine surfaces its
				// diagnostics directly, but there's no analyzer to
				// register.
				r.resolved = append(r.resolved, Resolved{
					Name:     e.Name,
					Shape:    e.Shape,
					Status:   StatusEnabled,
					Settings: settings,
				})
				continue
			}
			analyzers := e.AnalyzerFn(settings)
			if len(analyzers) == 0 {
				r.resolved = append(r.resolved, Resolved{
					Name:     e.Name,
					Shape:    e.Shape,
					Status:   StatusDisabled,
					Settings: settings,
					Reason:   "every family member disabled by user config",
				})
				continue
			}
			for _, a := range analyzers {
				r.resolved = append(r.resolved, Resolved{
					Name:     e.Name,
					Shape:    e.Shape,
					Status:   StatusEnabled,
					Analyzer: a,
					Settings: settings,
				})
			}
		}

		if e.Deprecated != "" {
			r.warnings = append(r.warnings, Warning{
				Field:   "linters.enable[" + e.Name + "]",
				Message: e.Deprecated,
			})
		}
	}

	// 6. Materialize the enabled set: rows the engine should try to
	//    run. ShapeRegistryOnly + ShapeSubprocess + StatusDisabled
	//    rows are surfaced via [Registry.All] for diagnostic purposes
	//    but not via [Registry.Enabled]. The engine consumes Enabled.
	for _, rr := range r.resolved {
		if rr.Status == StatusEnabled {
			r.enabled = append(r.enabled, rr)
		}
	}

	return r, r.warnings, nil
}

// Enabled returns the resolved rows whose Status is StatusEnabled.
// Stable order: alphabetical by linter Name; within a fan-out family
// (e.g. staticcheck), alphabetical by analyzer name.
func (r *Registry) Enabled() []Resolved {
	if r == nil {
		return nil
	}
	out := make([]Resolved, len(r.enabled))
	copy(out, r.enabled)
	return out
}

// All returns every Resolved row, including disabled / deferred /
// no-analyzer-wired entries. Useful for the CLI's "list-linters"
// surface.
func (r *Registry) All() []Resolved {
	if r == nil {
		return nil
	}
	out := make([]Resolved, len(r.resolved))
	copy(out, r.resolved)
	return out
}

// Warnings returns the structured warnings emitted during Build.
// Returned slice is a copy; callers may sort or filter freely.
func (r *Registry) Warnings() []Warning {
	if r == nil {
		return nil
	}
	out := make([]Warning, len(r.warnings))
	copy(out, r.warnings)
	return out
}

// resolveActiveSet computes the post-enable/disable linter set,
// keyed by canonical name. Implements:
//
//  1. `linters.default` selects the seed set.
//  2. `linters.enable[]` unions with the seed.
//  3. `linters.disable[]` subtracts.
//  4. `linters.settings.custom` adds plugin entries.
//
// Returns the active set + warnings about resolution decisions.
func resolveActiveSet(cfg *config.Config, cat *catalog) (map[string]*Entry, []Warning) {
	active := make(map[string]*Entry)
	var warnings []Warning

	group := defaultGroup(cfg)
	for _, e := range cat.entries() {
		if e.Shape == ShapeFormatter {
			continue // formatters never join the linters set
		}
		if e.InGroup[group] {
			active[e.Name] = e
		}
	}

	for i, n := range cfg.Linters.Enable {
		e, ok := cat.resolve(n)
		if !ok {
			// Defensive; validateAgainstCatalog rejects this case.
			continue
		}
		if e.Shape == ShapeFormatter {
			// Already surfaced by config.Linters.Validate; do nothing.
			continue
		}
		active[e.Name] = e

		// Surface aliased v1 names as warnings so the user sees that
		// gosimple → staticcheck happened.
		if !strings.EqualFold(e.Name, n) {
			warnings = append(warnings, Warning{
				Field:   fmt.Sprintf("linters.enable[%d]", i),
				Message: fmt.Sprintf("%q resolves to %q (v1 alias)", n, e.Name),
			})
		}
	}

	for _, n := range cfg.Linters.Disable {
		e, ok := cat.resolve(n)
		if !ok {
			continue
		}
		delete(active, e.Name)
	}

	// Custom plugins from linters.settings.custom. Plugin loading
	// happens engine-side (Phase 4); the registry surfaces the slot.
	for name := range cfg.Linters.Settings.Custom {
		// Custom names live outside the catalog; synthesize an Entry
		// scoped to this Registry's active set. Plugin Settings are
		// preserved on Resolved.Settings via perLinterSettings.
		active[name] = &Entry{
			Name:    name,
			Shape:   ShapeRegistryOnly,
			InGroup: map[Group]bool{},
		}
		warnings = append(warnings, Warning{
			Field:   "linters.settings.custom[" + name + "]",
			Message: "custom plugin registered; engine loads .so / module at run time (Phase 4)",
		})
	}

	return active, warnings
}

// defaultGroup resolves cfg.Linters.Default into the canonical Group
// value. Empty string → GroupStandard (upstream's default).
func defaultGroup(cfg *config.Config) Group {
	switch cfg.Linters.Default {
	case "":
		return GroupStandard
	case config.GroupNone:
		return GroupNone
	case config.GroupStandard:
		return GroupStandard
	case config.GroupFast:
		return GroupFast
	case config.GroupAll:
		return GroupAll
	default:
		// validateAgainstCatalog rejects unknown default values; if
		// we got here despite that, default to "standard" so the
		// resolution still produces something useful.
		return GroupStandard
	}
}

// sortedNames returns the keys of the map in deterministic order.
func sortedNames(m map[string]*Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
