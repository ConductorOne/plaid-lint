// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
)

// runFlags carries the parsed shape of `plaid-lint run`.
//
// Tristate flags (set / unset / explicit-zero) are stored as
// *intPtrValue / *boolPtrValue so the file config's value survives
// when the CLI didn't pass the flag. Plain values are used for
// "scalar overlays" where Go's zero-value semantics aren't ambiguous
// with "unset".
type runFlags struct {
	// Config discovery.
	ConfigPath string
	NoConfig   bool

	// Linter selection.
	Default    string
	Disable    csvSlice
	Enable     csvSlice
	EnableOnly csvSlice
	Analyzers  csvSlice
	FastOnly   bool

	// Run.
	Concurrency          int
	ModulesDownloadMode  string
	IssuesExitCode       *intPtrValue
	BuildTags            csvSlice
	Timeout              time.Duration
	Tests                *boolPtrValue
	AllowParallelRunners bool
	AllowSerialRunners   bool

	// Output paths / modes.
	PathPrefix string
	PathMode   string
	ShowStats  *boolPtrValue

	// Per-format output paths.
	OutputTextPath            string
	OutputTextPrintLinterName *boolPtrValue
	OutputTextPrintIssuedLine *boolPtrValue
	OutputTextColors          *boolPtrValue
	OutputJSONPath            string
	OutputTabPath             string
	OutputTabPrintLinterName  *boolPtrValue
	OutputTabColors           *boolPtrValue
	OutputHTMLPath            string
	OutputCheckstylePath      string
	OutputCodeClimatePath     string
	OutputJUnitXMLPath        string
	OutputJUnitXMLExtended    bool
	OutputTeamCityPath        string
	OutputSarifPath           string

	// Issues / diff.
	MaxIssuesPerLinter *intPtrValue
	MaxSameIssues      *intPtrValue
	UniqByLine         *boolPtrValue
	NewIssues          bool
	NewFromRev         string
	NewFromPatch       string
	NewFromMergeBase   string
	WholeFiles         bool
	Fix                bool

	// Profile / trace.
	CPUProfilePath string
	MemProfilePath string
	TracePath      string

	// MetricsJSONPath, when non-empty, names a file the CLI writes a
	// JSON snapshot of L0/L1/L2 cache counters + diagnostic counts to
	// on run completion. Consumed by the bench runner to capture
	// hit-rate data. Empty disables the dump.
	MetricsJSONPath string

	// Positional args (target paths). May be empty (defaults to `./...`).
	Args []string

	// setFlags records the names of flags that were explicitly passed
	// on the command line. Used to distinguish "unset" from "explicit
	// zero" for non-tristate flags during overlay construction.
	setFlags map[string]bool
}

// csvSlice is a flag.Value that accepts comma-separated values and is
// repeatable. Mirrors upstream's `[]string` flag semantics where both
// `--enable=a,b` and `--enable=a --enable=b` produce `[a, b]`.
type csvSlice []string

func (c *csvSlice) String() string { return strings.Join(*c, ",") }

func (c *csvSlice) Set(v string) error {
	if v == "" {
		return nil
	}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			*c = append(*c, p)
		}
	}
	return nil
}

// bindRunFlags attaches every `run` flag to fs and returns a struct
// the caller fills in by parsing.
//
// Aliases (-c, -D, -E, -j, -n) are bound to the same target as the
// long form via flag.Var on a shared collector; upstream uses Cobra's
// shorthand support, but the stdlib `flag` semantics get us there
// with two-step bindings for slice flags and `flag.StringVar` repeats
// for scalars.
func bindRunFlags(fs *flag.FlagSet) *runFlags {
	rf := &runFlags{setFlags: map[string]bool{}}

	// Config discovery.
	fs.StringVar(&rf.ConfigPath, "config", "", "Read config from file path PATH")
	fs.StringVar(&rf.ConfigPath, "c", "", "alias for --config")
	fs.BoolVar(&rf.NoConfig, "no-config", false, "Don't read config file")

	// Linter selection.
	fs.StringVar(&rf.Default, "default", "", "Default set of linters to enable (standard|all|fast|none)")
	fs.Var(&rf.Disable, "disable", "Disable specific linter (repeatable, comma-separated)")
	fs.Var(&rf.Disable, "D", "alias for --disable")
	fs.Var(&rf.Enable, "enable", "Enable specific linter (repeatable, comma-separated)")
	fs.Var(&rf.Enable, "E", "alias for --enable")
	fs.Var(&rf.EnableOnly, "enable-only", "Override config to only run the specific linter(s)")
	fs.Var(&rf.Analyzers, "enable-only-analyzer", "Only run the named analysis analyzer(s) (repeatable, comma-separated)")
	fs.BoolVar(&rf.FastOnly, "fast-only", false, "Filter enabled linters to only fast linters")

	// Run.
	fs.IntVar(&rf.Concurrency, "concurrency", 0, "Number of CPUs to use (0 = auto)")
	fs.IntVar(&rf.Concurrency, "j", 0, "alias for --concurrency")
	fs.StringVar(&rf.ModulesDownloadMode, "modules-download-mode", "", "Modules download mode (mod|readonly|vendor)")
	rf.IssuesExitCode = bindIntPtr(fs, "issues-exit-code", "Exit code when issues were found (default 1)")
	fs.Var(&rf.BuildTags, "build-tags", "Build tags (repeatable, comma-separated)")
	fs.DurationVar(&rf.Timeout, "timeout", 0, "Timeout for total work (e.g. 5m). Disabled by default")
	rf.Tests = bindBoolPtr(fs, "tests", "Analyze tests (*_test.go) (default true)")
	fs.BoolVar(&rf.AllowParallelRunners, "allow-parallel-runners", false, "Allow multiple parallel plaid-lint instances running")
	fs.BoolVar(&rf.AllowSerialRunners, "allow-serial-runners", false, "Allow multiple plaid-lint instances running, but serialize them around a lock")

	// Output paths / modes.
	fs.StringVar(&rf.PathPrefix, "path-prefix", "", "Path prefix to add to output")
	fs.StringVar(&rf.PathMode, "path-mode", "", "Path mode (\"\"|abs)")
	rf.ShowStats = bindBoolPtr(fs, "show-stats", "Show statistics per linter (default true)")

	// Per-format output paths.
	fs.StringVar(&rf.OutputTextPath, "output.text.path", "", "text printer output path")
	rf.OutputTextPrintLinterName = bindBoolPtr(fs, "output.text.print-linter-name", "text printer: print linter name (default true)")
	rf.OutputTextPrintIssuedLine = bindBoolPtr(fs, "output.text.print-issued-lines", "text printer: print issued lines (default true)")
	rf.OutputTextColors = bindBoolPtr(fs, "output.text.colors", "text printer: use colors (default true)")
	fs.StringVar(&rf.OutputJSONPath, "output.json.path", "", "json printer output path")
	fs.StringVar(&rf.OutputTabPath, "output.tab.path", "", "tab printer output path")
	rf.OutputTabPrintLinterName = bindBoolPtr(fs, "output.tab.print-linter-name", "tab printer: print linter name (default true)")
	rf.OutputTabColors = bindBoolPtr(fs, "output.tab.colors", "tab printer: use colors (default true)")
	fs.StringVar(&rf.OutputHTMLPath, "output.html.path", "", "html printer output path")
	fs.StringVar(&rf.OutputCheckstylePath, "output.checkstyle.path", "", "checkstyle printer output path")
	fs.StringVar(&rf.OutputCodeClimatePath, "output.code-climate.path", "", "code-climate printer output path")
	fs.StringVar(&rf.OutputJUnitXMLPath, "output.junit-xml.path", "", "junit-xml printer output path")
	fs.BoolVar(&rf.OutputJUnitXMLExtended, "output.junit-xml.extended", false, "junit-xml printer: support extra fields")
	fs.StringVar(&rf.OutputTeamCityPath, "output.teamcity.path", "", "teamcity printer output path")
	fs.StringVar(&rf.OutputSarifPath, "output.sarif.path", "", "sarif printer output path")

	// Issues / diff.
	rf.MaxIssuesPerLinter = bindIntPtr(fs, "max-issues-per-linter", "Maximum issues per linter (0 disables)")
	rf.MaxSameIssues = bindIntPtr(fs, "max-same-issues", "Maximum count of issues with the same text (0 disables)")
	rf.UniqByLine = bindBoolPtr(fs, "uniq-by-line", "Make issues output unique by line (default true)")
	fs.BoolVar(&rf.NewIssues, "new", false, "Show only new issues")
	fs.BoolVar(&rf.NewIssues, "n", false, "alias for --new")
	fs.StringVar(&rf.NewFromRev, "new-from-rev", "", "Show only new issues created after git revision REV")
	fs.StringVar(&rf.NewFromPatch, "new-from-patch", "", "Show only new issues created in git patch with file path PATH")
	fs.StringVar(&rf.NewFromMergeBase, "new-from-merge-base", "", "Show only new issues created after the best common ancestor against HEAD")
	fs.BoolVar(&rf.WholeFiles, "whole-files", false, "Show issues in any part of updated files")
	fs.BoolVar(&rf.Fix, "fix", false, "Apply the fixes detected by the linters")

	// Profile / trace.
	fs.StringVar(&rf.CPUProfilePath, "cpu-profile-path", "", "Path to CPU profile output file (pprof format)")
	fs.StringVar(&rf.MemProfilePath, "mem-profile-path", "", "Path to memory profile output file (pprof heap, captured at run exit after a forced GC)")
	fs.StringVar(&rf.TracePath, "trace-path", "", "Path to runtime/trace output file (load with `go tool trace`)")
	fs.StringVar(&rf.MetricsJSONPath, "metrics-json", "", "Path to write a JSON snapshot of cache/run metrics on run completion")

	return rf
}

// bindBoolPtr installs a tristate bool flag and returns the underlying
// value object. Caller reads .Value() after fs.Parse to get *bool.
func bindBoolPtr(fs *flag.FlagSet, name, usage string) *boolPtrValue {
	v := &boolPtrValue{}
	fs.Var(v, name, usage)
	return v
}

// bindIntPtr installs a tristate int flag and returns the underlying
// value object. Caller reads .Value() after fs.Parse to get *int.
func bindIntPtr(fs *flag.FlagSet, name, usage string) *intPtrValue {
	v := &intPtrValue{}
	fs.Var(v, name, usage)
	return v
}

// boolPtrValue is a flag.Value backed by an optional bool.
type boolPtrValue struct {
	v *bool
}

func (b *boolPtrValue) String() string {
	if b == nil || b.v == nil {
		return ""
	}
	if *b.v {
		return "true"
	}
	return "false"
}

func (b *boolPtrValue) Set(v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "true", "1", "yes", "on":
		t := true
		b.v = &t
	case "false", "0", "no", "off":
		f := false
		b.v = &f
	default:
		return fmt.Errorf("invalid boolean %q", v)
	}
	return nil
}

// IsBoolFlag tells the flag package this is a bool flag (so `--name`
// without a value defaults to true).
func (b *boolPtrValue) IsBoolFlag() bool { return true }

// Value returns the parsed value or nil if the flag was never set.
// nil-safe to keep call sites simple in subcommands that only bind a
// subset of the run flags.
func (b *boolPtrValue) Value() *bool {
	if b == nil {
		return nil
	}
	return b.v
}

// intPtrValue is a flag.Value backed by an optional int.
type intPtrValue struct {
	v *int
}

func (i *intPtrValue) String() string {
	if i == nil || i.v == nil {
		return ""
	}
	return fmt.Sprintf("%d", *i.v)
}

func (i *intPtrValue) Set(v string) error {
	n, err := parseInt(v)
	if err != nil {
		return err
	}
	i.v = &n
	return nil
}

func (i *intPtrValue) Value() *int {
	if i == nil {
		return nil
	}
	return i.v
}

func parseInt(v string) (int, error) {
	n := 0
	sign := 1
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("empty integer")
	}
	if v[0] == '-' {
		sign = -1
		v = v[1:]
	} else if v[0] == '+' {
		v = v[1:]
	}
	if v == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", v)
		}
		n = n*10 + int(r-'0')
	}
	return sign * n, nil
}

// recordSetFlags walks the parsed flag set and records every flag
// that was explicitly passed. Called after fs.Parse so the overlay
// builder can consult it.
func (rf *runFlags) recordSetFlags(fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		rf.setFlags[f.Name] = true
	})
}

// wasSet reports whether the user passed any of the supplied flag
// names (or their short aliases). Pass both long and short forms;
// the function ORs them.
func (rf *runFlags) wasSet(names ...string) bool {
	for _, n := range names {
		if rf.setFlags[n] {
			return true
		}
	}
	return false
}

// applyOverlay builds a *config.Config that, when Merge'd as the
// overlay on top of the file-loaded config, yields the effective
// configuration. The overlay carries only the flags the user
// explicitly set.
//
// This is the precedence enforcement point: built-in defaults first
// (struct zero + Loader.finalize), then file config (already in
// `base`), then this overlay.
func (rf *runFlags) applyOverlay(base *config.Config) *config.Config {
	overlay := &config.Config{}

	// Linter selection. --enable-only replaces; --enable appends.
	switch {
	case len(rf.EnableOnly) > 0:
		overlay.Linters.Default = "none"
		overlay.Linters.Enable = append([]string{}, rf.EnableOnly...)
	default:
		if rf.wasSet("default") {
			overlay.Linters.Default = rf.Default
		}
		if len(rf.Enable) > 0 {
			overlay.Linters.Enable = append([]string{}, rf.Enable...)
		}
	}
	if len(rf.Disable) > 0 {
		overlay.Linters.Disable = append([]string{}, rf.Disable...)
	}
	if rf.FastOnly {
		overlay.Linters.FastOnly = true
	}

	// Run.
	if rf.wasSet("concurrency", "j") {
		overlay.Run.Concurrency = rf.Concurrency
	}
	if rf.wasSet("modules-download-mode") {
		overlay.Run.ModulesDownloadMode = rf.ModulesDownloadMode
	}
	if v := rf.IssuesExitCode.Value(); v != nil {
		overlay.Run.ExitCodeIfIssuesFound = *v
	}
	if len(rf.BuildTags) > 0 {
		overlay.Run.BuildTags = append([]string{}, rf.BuildTags...)
	}
	if rf.wasSet("timeout") {
		overlay.Run.Timeout = config.Duration(rf.Timeout)
	}
	if v := rf.Tests.Value(); v != nil {
		b := *v
		overlay.Run.AnalyzeTests = &b
	}
	if rf.AllowParallelRunners {
		overlay.Run.AllowParallelRunners = true
	}
	if rf.AllowSerialRunners {
		overlay.Run.AllowSerialRunners = true
	}

	// Output / paths.
	if rf.wasSet("path-prefix") {
		overlay.Output.PathPrefix = rf.PathPrefix
	}
	if rf.wasSet("path-mode") {
		overlay.Output.PathMode = rf.PathMode
	}
	if v := rf.ShowStats.Value(); v != nil {
		overlay.Output.ShowStats = *v
	}

	// Per-format output paths.
	if rf.wasSet("output.text.path") {
		overlay.Output.Formats.Text.Path = rf.OutputTextPath
	}
	if v := rf.OutputTextPrintLinterName.Value(); v != nil {
		overlay.Output.Formats.Text.PrintLinterName = *v
	}
	if v := rf.OutputTextPrintIssuedLine.Value(); v != nil {
		overlay.Output.Formats.Text.PrintIssuedLine = *v
	}
	if v := rf.OutputTextColors.Value(); v != nil {
		overlay.Output.Formats.Text.Colors = *v
	}
	if rf.wasSet("output.json.path") {
		overlay.Output.Formats.JSON.Path = rf.OutputJSONPath
	}
	if rf.wasSet("output.tab.path") {
		overlay.Output.Formats.Tab.Path = rf.OutputTabPath
	}
	if v := rf.OutputTabPrintLinterName.Value(); v != nil {
		overlay.Output.Formats.Tab.PrintLinterName = *v
	}
	if v := rf.OutputTabColors.Value(); v != nil {
		overlay.Output.Formats.Tab.Colors = *v
	}
	if rf.wasSet("output.html.path") {
		overlay.Output.Formats.HTML.Path = rf.OutputHTMLPath
	}
	if rf.wasSet("output.checkstyle.path") {
		overlay.Output.Formats.Checkstyle.Path = rf.OutputCheckstylePath
	}
	if rf.wasSet("output.code-climate.path") {
		overlay.Output.Formats.CodeClimate.Path = rf.OutputCodeClimatePath
	}
	if rf.wasSet("output.junit-xml.path") {
		overlay.Output.Formats.JUnitXML.Path = rf.OutputJUnitXMLPath
	}
	if rf.OutputJUnitXMLExtended {
		overlay.Output.Formats.JUnitXML.Extended = true
	}
	if rf.wasSet("output.teamcity.path") {
		overlay.Output.Formats.TeamCity.Path = rf.OutputTeamCityPath
	}
	if rf.wasSet("output.sarif.path") {
		overlay.Output.Formats.Sarif.Path = rf.OutputSarifPath
	}

	// Issues / diff.
	if v := rf.MaxIssuesPerLinter.Value(); v != nil {
		overlay.Issues.MaxIssuesPerLinter = *v
	}
	if v := rf.MaxSameIssues.Value(); v != nil {
		overlay.Issues.MaxSameIssues = *v
	}
	if v := rf.UniqByLine.Value(); v != nil {
		overlay.Issues.UniqByLine = *v
	}
	if rf.NewIssues {
		overlay.Issues.Diff = true
	}
	if rf.wasSet("new-from-rev") {
		overlay.Issues.DiffFromRevision = rf.NewFromRev
	}
	if rf.wasSet("new-from-patch") {
		overlay.Issues.DiffPatchFilePath = rf.NewFromPatch
	}
	if rf.wasSet("new-from-merge-base") {
		overlay.Issues.DiffFromMergeBase = rf.NewFromMergeBase
	}
	if rf.WholeFiles {
		overlay.Issues.WholeFiles = true
	}
	if rf.Fix {
		overlay.Issues.NeedFix = true
	}

	return config.Merge(base, overlay)
}
