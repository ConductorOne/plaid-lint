// Package output provides production-diagnostic printers for plaid-lint.
//
// The set of formats and the wire shape they emit match upstream
// golangci-lint v2.9's pkg/printers/ as closely as possible.
package output

import (
	"sort"
)

// Severity classifies a diagnostic. The string values are the canonical
// plaid-lint severities. Per-printer Severity sanitizers map these onto
// each format's own vocabulary (e.g. SARIF requires {none,note,warning,error}).
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Position is a 1-indexed file:line:column point. Column is optional;
// a Column of 0 means "unknown column" (matches upstream's behavior for
// linters like gosec that don't report a column).
type Position struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
}

// SuggestedFix is a textual replacement covering a contiguous range in the
// original file. The shape mirrors analysis.SuggestedFix / analysis.TextEdit
// but stays free of the analysis package dependency so printers can be used
// outside of the engine.
type SuggestedFix struct {
	Message   string     `json:"message,omitempty"`
	TextEdits []TextEdit `json:"text_edits,omitempty"`
}

// TextEdit is a single replacement. Positions are 1-indexed and inclusive
// of the Start point, exclusive of the End point.
type TextEdit struct {
	Start   Position `json:"start"`
	End     Position `json:"end"`
	NewText string   `json:"new_text"`
}

// RelatedInformation is the optional multi-line trail associated with a
// diagnostic (e.g. "previously declared here").
type RelatedInformation struct {
	Position Position `json:"position"`
	Message  string   `json:"message"`
}

// Diagnostic is the canonical shape plaid-lint emits to every output
// format. It is the producer-side analogue of upstream golangci-lint's
// pkg/result.Issue, but trimmed to the fields the production-diagnostic
// printers actually consume.
//
// Fields are tagged so the json printer can encode Diagnostic directly
// without an adapter; the other printers project Diagnostic into format-
// specific shapes.
type Diagnostic struct {
	// Linter is the analyzer name. Required.
	Linter string `json:"linter"`

	// Message is the human-readable diagnostic text. Required.
	Message string `json:"message"`

	// Severity classifies the diagnostic. If empty, treated as
	// SeverityError by each printer's sanitizer.
	Severity Severity `json:"severity,omitempty"`

	// Pos is the primary location. Filename is expected to be a path
	// relative to the workspace root or an absolute path; printers do
	// not rewrite it.
	Pos Position `json:"pos"`

	// SourceLines is the optional source-code context shown by the
	// text and html printers. One entry per line.
	SourceLines []string `json:"source_lines,omitempty"`

	// SuggestedFixes is an optional set of replacements that resolve
	// the diagnostic.
	SuggestedFixes []SuggestedFix `json:"suggested_fixes,omitempty"`

	// Related is the optional multi-line trail associated with the
	// diagnostic.
	Related []RelatedInformation `json:"related,omitempty"`
}

// Description renders "<linter>: <message>", matching upstream's
// result.Issue.Description() shape (used by the codeclimate printer).
func (d *Diagnostic) Description() string {
	return d.Linter + ": " + d.Message
}

// PosString renders "file:line[:column]", matching token.Position.String().
func (d *Diagnostic) PosString() string {
	return formatPos(d.Pos)
}

// Sort orders diagnostics deterministically so format-zero printers
// (json, sarif, checkstyle...) emit byte-stable output across runs.
//
// Sort order: filename, line, column, linter, message.
func Sort(diags []Diagnostic) {
	sort.SliceStable(diags, func(i, j int) bool {
		a, b := diags[i], diags[j]
		if a.Pos.Filename != b.Pos.Filename {
			return a.Pos.Filename < b.Pos.Filename
		}
		if a.Pos.Line != b.Pos.Line {
			return a.Pos.Line < b.Pos.Line
		}
		if a.Pos.Column != b.Pos.Column {
			return a.Pos.Column < b.Pos.Column
		}
		if a.Linter != b.Linter {
			return a.Linter < b.Linter
		}
		return a.Message < b.Message
	})
}
