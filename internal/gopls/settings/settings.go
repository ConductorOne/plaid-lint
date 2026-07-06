// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package settings is a minimal shim of upstream gopls's settings.
// Only the symbols read by the forked cache code are defined here;
// LSP-RPC user options, staticcheck wiring, and codeactionkind are
// out of scope for plaid-lint.
package settings

import (
	"time"

	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
	"golang.org/x/tools/go/analysis"
)

// Options is the carrier struct returned by Snapshot.Options(). Only
// fields actually read by the forked cache code appear here.
type Options struct {
	BuildOptions
	UIOptions
	InternalOptions
	UserOptions
}

// BuildOptions groups build-related options. Referenced by name from a
// doc comment in filterer.go; otherwise unused.
type BuildOptions struct {
	BuildFlags              []string
	DirectoryFilters        []string
	ExpandWorkspaceToModule bool
	StandaloneTags          []string
	TemplateExtensions      []string

	// Tests controls whether the workspace loader passes
	// packages.Config.Tests=true so test variants of workspace
	// packages reach the analyzer pass. A nil value selects the
	// default of true, matching golangci-lint v2's `run.tests`
	// default. Pass &false to opt out (skip *_test.go files).
	Tests *bool
}

// UIOptions covers UI knobs the cache reads.
type UIOptions struct {
	AnalysisProgressReporting   bool
	LinkTarget                  string
	Local                       string
	ReportAnalysisProgressAfter time.Duration
	VerboseOutput               bool
}

// InternalOptions covers gopls-internal knobs the cache reads.
type InternalOptions struct {
	SubdirWatchPatterns SubdirWatchPatterns
	WorkspaceFiles      []string
}

// UserOptions covers options sourced from the LSP client.
type UserOptions struct {
	ClientInfo                  *ClientInfo
	RelatedInformationSupported bool

	// Analyses maps analyzer name to enable/disable.
	Analyses map[string]bool

	// Staticcheck / StaticcheckProvided are referenced by the
	// upstream Analyzer.Enabled method but the carrier Analyzer in
	// this shim has no staticcheck data, so these are kept as zero
	// values.
	Staticcheck         bool
	StaticcheckProvided bool
}

// ClientInfo identifies the LSP client. Carrier returns an empty
// struct.
type ClientInfo struct {
	Name    string
	Version string
}

// SubdirWatchPatterns enumerates the subdirectory file-watching
// strategy. Cache code reads three constants.
type SubdirWatchPatterns string

const (
	SubdirWatchPatternsOn   SubdirWatchPatterns = "On"
	SubdirWatchPatternsOff  SubdirWatchPatterns = "Off"
	SubdirWatchPatternsAuto SubdirWatchPatterns = "Auto"
)

// Analyzer is the carrier wrapper for an analysis.Analyzer + its
// gopls-side metadata. This is replaced by plaid-lint's
// AnalyzerDescriptor in Phase 1 W4–W5; until then we carry only enough
// surface to satisfy the cache's analysis.go and errors.go.
type Analyzer struct {
	analyzer    *analysis.Analyzer
	nonDefault  bool
	actionKinds []protocol.CodeActionKind
	severity    protocol.DiagnosticSeverity
	tags        []protocol.DiagnosticTag
}

// Analyzer returns the wrapped analyzer.
func (a *Analyzer) Analyzer() *analysis.Analyzer { return a.analyzer }

// Enabled reports whether this analyzer is enabled.
func (a *Analyzer) Enabled(o *Options) bool {
	if o != nil {
		if v, ok := o.Analyses[a.Analyzer().Name]; ok {
			return v
		}
	}
	return !a.nonDefault
}

// ActionKinds returns the code-action kinds for diagnostics from this analyzer.
func (a *Analyzer) ActionKinds() []protocol.CodeActionKind { return a.actionKinds }

// Severity returns the severity, defaulting to SeverityWarning.
func (a *Analyzer) Severity() protocol.DiagnosticSeverity {
	if a.severity == 0 {
		return protocol.SeverityWarning
	}
	return a.severity
}

// Tags returns extra diagnostic tags for this analyzer.
func (a *Analyzer) Tags() []protocol.DiagnosticTag { return a.tags }

// String returns the wrapped analyzer's name.
func (a *Analyzer) String() string {
	if a.analyzer == nil {
		return ""
	}
	return a.analyzer.String()
}

// AllAnalyzers is the list of registered analyzers. The carrier is
// empty; plaid-lint registers analyzers through its own descriptor
// machinery in W4–W5.
//
// In the meantime, tests may construct *Analyzer values via
// NewAnalyzer and append to this slice to exercise the analysis
// pipeline end-to-end. Production callers should NOT append to
// AllAnalyzers — the descriptor machinery owns this list.
var AllAnalyzers []*Analyzer

// NewAnalyzer wraps a *analysis.Analyzer as a settings.Analyzer with
// the minimum metadata the cache's analysis.go reads. The wrapper
// defaults to "enabled by default" (nonDefault=false), so the analyzer
// will run whenever Snapshot.Analyze is invoked.
//
// This is the W6 escape hatch for the pipeline equivalence test: the
// production descriptor wiring is W7, but we need a way to register a
// concrete list of analyzers (the 7 representative ones) for the
// gate-evidence test.
func NewAnalyzer(a *analysis.Analyzer) *Analyzer {
	return &Analyzer{analyzer: a}
}

// DefaultOptions returns an Options populated with the field values
// the cache fork relies on. WorkspaceState constructs one of these
// per workspace when the caller doesn't supply a custom Options.
func DefaultOptions() *Options {
	return &Options{
		InternalOptions: InternalOptions{
			SubdirWatchPatterns: SubdirWatchPatternsAuto,
			WorkspaceFiles:      []string{"go.mod", "go.sum", "go.work", "go.work.sum"},
		},
	}
}
