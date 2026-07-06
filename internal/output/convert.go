// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package output

import (
	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

// FromAnalysis adapts a single in-process diagnostic produced by
// the gopls cache (which is the form Snapshot.Analyze returns) to
// the production [Diagnostic] shape that printers consume.
//
// Conversion rules:
//
//   - Linter is taken from cache.Diagnostic.Source. For
//     analysis-driven diagnostics gopls sets Source to the
//     analyzer's Name() — exactly the registry attribution the
//     engine wants. The caller may override by mutating the
//     returned struct.
//
//   - Pos.Filename is cache.Diagnostic.URI.Path() (absolute path).
//
//   - Pos.Line / Pos.Column convert the 0-indexed LSP protocol.Range
//     to 1-indexed integers. LSP rows/columns are zero-based; every
//     downstream printer expects 1-based.
//
//   - Severity defaults to SeverityError when the cache diagnostic
//     carries no severity (the protocol zero value) or any value
//     other than the two we recognize. This matches upstream
//     golangci-lint's "treat analysis findings as errors unless
//     told otherwise" convention.
//
//   - Related maps element-for-element to RelatedInformation.
//
//   - SuggestedFix is intentionally NOT converted (out of scope
//     per the dispatch). cache.Diagnostic.SuggestedFixes is
//     dropped silently.
//
// FromAnalysis is pure: no I/O, no error path. The caller is
// responsible for handling a nil input upstream (a nil diagnostic
// pointer in Snapshot.Analyze's output is a gopls bug).
func FromAnalysis(d *cache.Diagnostic) Diagnostic {
	if d == nil {
		return Diagnostic{}
	}
	out := Diagnostic{
		Linter:   string(d.Source),
		Message:  d.Message,
		Severity: severityFromLSP(d.Severity),
		Pos: Position{
			Filename: d.URI.Path(),
			Line:     int(d.Range.Start.Line) + 1,
			Column:   int(d.Range.Start.Character) + 1,
		},
	}
	if len(d.Related) > 0 {
		out.Related = make([]RelatedInformation, 0, len(d.Related))
		for _, r := range d.Related {
			out.Related = append(out.Related, RelatedInformation{
				Message: r.Message,
				Position: Position{
					Filename: r.Location.URI.Path(),
					Line:     int(r.Location.Range.Start.Line) + 1,
					Column:   int(r.Location.Range.Start.Character) + 1,
				},
			})
		}
	}
	return out
}

// severityFromLSP maps the LSP DiagnosticSeverity enum onto the
// canonical [Severity] vocabulary. Unknown / zero values collapse
// to SeverityError, matching the printers' sanitizer fallback.
func severityFromLSP(s protocol.DiagnosticSeverity) Severity {
	switch s {
	case protocol.SeverityWarning:
		return SeverityWarning
	case protocol.SeverityInformation, protocol.SeverityHint:
		return SeverityInfo
	default:
		return SeverityError
	}
}
