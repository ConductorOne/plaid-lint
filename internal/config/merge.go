// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

// Merge layers overlay on top of base and returns the result. The base
// and overlay are not mutated. nil arguments are treated as the
// zero-value config (so `Merge(nil, x)` returns a copy of x).
//
// Merge semantics (overlay wins for the disputed field):
//
//   - **Scalars** (string, int, bool, time.Duration): overlay wins
//     when non-zero; base survives when overlay is zero.
//   - **Slices**: append (overlay appended to base). Dedup is the
//     caller's job; upstream doesn't dedup either.
//   - **Maps**: union (overlay wins on key conflict).
//   - **Nested structs**: recursive merge.
//
// The "append-for-slices" rule matches upstream's viper-+-CLI
// behavior, where `--enable=a` plus `enable: [b]` in the file yields
// `[b, a]`. The order is base-first, overlay-second.
//
// Merge is intentionally NOT a deep copy of every field — it mutates
// the returned [Config] by reusing slices/maps from base/overlay
// where safe. Callers that need full independence should
// Marshal+Unmarshal a copy. The bench harness doesn't need that
// guarantee.
func Merge(base, overlay *Config) *Config {
	if base == nil {
		base = &Config{}
	}
	if overlay == nil {
		// Return a shallow copy of base.
		out := *base
		return &out
	}

	out := *base

	out.Version = pickString(base.Version, overlay.Version)
	out.Run = mergeRun(base.Run, overlay.Run)
	out.Output = mergeOutput(base.Output, overlay.Output)
	out.Linters = mergeLinters(base.Linters, overlay.Linters)
	out.Issues = mergeIssues(base.Issues, overlay.Issues)
	out.Severity = mergeSeverity(base.Severity, overlay.Severity)
	out.Formatters = mergeFormatters(base.Formatters, overlay.Formatters)

	// Preserve cfgDir/sourcePath from the overlay (CLI -> file -> env
	// pipeline normally has the on-disk file as the overlay).
	if overlay.cfgDir != "" {
		out.cfgDir = overlay.cfgDir
	}
	if overlay.sourcePath != "" {
		out.sourcePath = overlay.sourcePath
	}
	return &out
}

func mergeRun(base, overlay Run) Run {
	if overlay.Timeout != 0 {
		base.Timeout = overlay.Timeout
	}
	if overlay.Concurrency != 0 {
		base.Concurrency = overlay.Concurrency
	}
	base.Go = pickString(base.Go, overlay.Go)
	base.RelativePathMode = pickString(base.RelativePathMode, overlay.RelativePathMode)
	base.BuildTags = appendUnique(base.BuildTags, overlay.BuildTags)
	base.ModulesDownloadMode = pickString(base.ModulesDownloadMode, overlay.ModulesDownloadMode)
	if overlay.EnableBuildVCS {
		base.EnableBuildVCS = true
	}
	if overlay.ExitCodeIfIssuesFound != 0 {
		base.ExitCodeIfIssuesFound = overlay.ExitCodeIfIssuesFound
	}
	if overlay.AnalyzeTests != nil {
		v := *overlay.AnalyzeTests
		base.AnalyzeTests = &v
	}
	if overlay.AllowParallelRunners {
		base.AllowParallelRunners = true
	}
	if overlay.AllowSerialRunners {
		base.AllowSerialRunners = true
	}
	return base
}

func mergeOutput(base, overlay Output) Output {
	base.Formats = mergeFormats(base.Formats, overlay.Formats)
	base.SortOrder = appendUnique(base.SortOrder, overlay.SortOrder)
	if overlay.ShowStats {
		base.ShowStats = true
	}
	base.PathPrefix = pickString(base.PathPrefix, overlay.PathPrefix)
	base.PathMode = pickString(base.PathMode, overlay.PathMode)
	return base
}

func mergeFormats(base, overlay Formats) Formats {
	if overlay.Text.Path != "" {
		base.Text.Path = overlay.Text.Path
	}
	if overlay.Text.PrintLinterName {
		base.Text.PrintLinterName = true
	}
	if overlay.Text.PrintIssuedLine {
		base.Text.PrintIssuedLine = true
	}
	if overlay.Text.Colors {
		base.Text.Colors = true
	}
	if overlay.JSON.Path != "" {
		base.JSON.Path = overlay.JSON.Path
	}
	if overlay.Tab.Path != "" {
		base.Tab.Path = overlay.Tab.Path
	}
	if overlay.Tab.PrintLinterName {
		base.Tab.PrintLinterName = true
	}
	if overlay.Tab.Colors {
		base.Tab.Colors = true
	}
	if overlay.HTML.Path != "" {
		base.HTML.Path = overlay.HTML.Path
	}
	if overlay.Checkstyle.Path != "" {
		base.Checkstyle.Path = overlay.Checkstyle.Path
	}
	if overlay.CodeClimate.Path != "" {
		base.CodeClimate.Path = overlay.CodeClimate.Path
	}
	if overlay.JUnitXML.Path != "" {
		base.JUnitXML.Path = overlay.JUnitXML.Path
	}
	if overlay.JUnitXML.Extended {
		base.JUnitXML.Extended = true
	}
	if overlay.TeamCity.Path != "" {
		base.TeamCity.Path = overlay.TeamCity.Path
	}
	if overlay.Sarif.Path != "" {
		base.Sarif.Path = overlay.Sarif.Path
	}
	return base
}

func mergeLinters(base, overlay Linters) Linters {
	base.Default = pickString(base.Default, overlay.Default)
	// When the overlay resets the default group to "none" (via
	// `--default=none` or the exclusive `--enable-only` flag), the
	// effective enable set must start empty: the base/file-config
	// enabled linters must not leak through. Replace rather than append
	// so `--enable-only X` runs exactly X, matching golangci-lint v2's
	// exclusive semantics. Any other default (standard/all/fast or
	// unset) keeps the additive `--enable` behavior.
	if overlay.Default == GroupNone {
		base.Enable = appendUnique(nil, overlay.Enable)
	} else {
		base.Enable = appendUnique(base.Enable, overlay.Enable)
	}
	base.Disable = appendUnique(base.Disable, overlay.Disable)
	if overlay.FastOnly {
		base.FastOnly = true
	}
	// Per-linter settings: overlay wins on a per-block basis.
	// For now we treat the whole LintersSettings as scalar — this is
	// over-aggressive but matches upstream's "last-file-wins" merge
	// for per-linter blocks. Refining to per-block diff is T2.3's call.
	if !isZeroLintersSettings(overlay.Settings) {
		base.Settings = overlay.Settings
	}
	base.Exclusions = mergeLinterExclusions(base.Exclusions, overlay.Exclusions)
	return base
}

func mergeLinterExclusions(base, overlay LinterExclusions) LinterExclusions {
	base.Generated = pickString(base.Generated, overlay.Generated)
	if overlay.WarnUnused {
		base.WarnUnused = true
	}
	base.Presets = appendUnique(base.Presets, overlay.Presets)
	base.Rules = append(base.Rules, overlay.Rules...)
	base.Paths = appendUnique(base.Paths, overlay.Paths)
	base.PathsExcept = appendUnique(base.PathsExcept, overlay.PathsExcept)
	return base
}

func mergeIssues(base, overlay Issues) Issues {
	if overlay.MaxIssuesPerLinter != 0 {
		base.MaxIssuesPerLinter = overlay.MaxIssuesPerLinter
	}
	if overlay.MaxSameIssues != 0 {
		base.MaxSameIssues = overlay.MaxSameIssues
	}
	if overlay.UniqByLine {
		base.UniqByLine = true
	}
	base.DiffFromRevision = pickString(base.DiffFromRevision, overlay.DiffFromRevision)
	base.DiffFromMergeBase = pickString(base.DiffFromMergeBase, overlay.DiffFromMergeBase)
	base.DiffPatchFilePath = pickString(base.DiffPatchFilePath, overlay.DiffPatchFilePath)
	if overlay.WholeFiles {
		base.WholeFiles = true
	}
	if overlay.Diff {
		base.Diff = true
	}
	if overlay.NeedFix {
		base.NeedFix = true
	}
	return base
}

func mergeSeverity(base, overlay Severity) Severity {
	base.Default = pickString(base.Default, overlay.Default)
	base.Rules = append(base.Rules, overlay.Rules...)
	return base
}

func mergeFormatters(base, overlay Formatters) Formatters {
	base.Enable = appendUnique(base.Enable, overlay.Enable)
	if !isZeroFormatterSettings(overlay.Settings) {
		base.Settings = overlay.Settings
	}
	base.Exclusions = mergeFormatterExclusions(base.Exclusions, overlay.Exclusions)
	return base
}

func mergeFormatterExclusions(base, overlay FormatterExclusions) FormatterExclusions {
	base.Generated = pickString(base.Generated, overlay.Generated)
	base.Paths = appendUnique(base.Paths, overlay.Paths)
	if overlay.WarnUnused {
		base.WarnUnused = true
	}
	return base
}

// pickString returns overlay if non-empty, else base. Captures the
// "overlay-wins-when-set" semantics for string-valued scalars.
func pickString(base, overlay string) string {
	if overlay != "" {
		return overlay
	}
	return base
}

// appendUnique appends overlay to base, skipping entries already in
// base. Preserves base's order; new entries come from overlay in order.
func appendUnique(base, overlay []string) []string {
	if len(overlay) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	for _, b := range base {
		seen[b] = true
	}
	out := base
	for _, o := range overlay {
		if !seen[o] {
			out = append(out, o)
			seen[o] = true
		}
	}
	return out
}

// isZeroLintersSettings reports whether s is the zero value (no field
// has been set). The check is conservative — any non-zero field marks
// the block as set.
func isZeroLintersSettings(s LintersSettings) bool {
	z := LintersSettings{}
	return structuralEqual(s, z)
}

func isZeroFormatterSettings(s FormatterSettings) bool {
	z := FormatterSettings{}
	return structuralEqual(s, z)
}

// structuralEqual is a cheap equality check via yaml round-trip. Used
// only for the "did overlay set this block?" gate; not perf-critical.
func structuralEqual(a, b any) bool {
	aB, errA := marshalForCompare(a)
	bB, errB := marshalForCompare(b)
	if errA != nil || errB != nil {
		return false
	}
	if len(aB) != len(bB) {
		return false
	}
	for i := range aB {
		if aB[i] != bB[i] {
			return false
		}
	}
	return true
}
