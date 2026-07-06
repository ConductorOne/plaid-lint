// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package subproc

import (
	"path/filepath"
	"strings"

	"github.com/conductorone/plaid-lint/internal/output"
)

// Result-merge contract: subprocess-native output -> [output.Diagnostic].
//
// Each per-linter wrapper (T3.2 unused, T3.3 unparam, T3.4 plugin)
// decodes the subprocess's own output format (typically JSON for
// `unused -json` / `unparam -json`) into the intermediate
// [SubprocDiagnostic] shape declared below, then calls
// [Canonicalize] to produce a slice of [output.Diagnostic] that
// satisfies the per-field rules listed here. The canonical surface
// for the rest of plaid-lint is [output.Diagnostic] — there is no
// parallel diagnostic type at the engine boundary.
//
// Field-by-field mapping (input shape := subprocess output as
// captured into [SubprocDiagnostic]; output shape := [output.Diagnostic]):
//
//   - Linter (output.Diagnostic.Linter): set to the [Runner]'s
//     Name(), NOT whatever the subprocess emits. This guarantees
//     attribution stability even if a wrapped binary changes its
//     internal name (e.g. `honnef.co/go/tools/unused`'s `U1000`
//     check renders as Linter="unused").
//
//   - Message (output.Diagnostic.Message): copied verbatim from
//     SubprocDiagnostic.Message. Trailing whitespace is trimmed.
//
//   - Severity (output.Diagnostic.Severity): mapped via [mapSeverity]
//     from the subprocess's vocabulary onto the three canonical
//     values {SeverityError, SeverityWarning, SeverityInfo}. An
//     empty / unrecognized subprocess severity collapses to
//     SeverityError, matching how the printers' sanitizers behave
//     when given an empty Severity.
//
//   - Position (output.Diagnostic.Pos.Filename): canonicalized to an
//     absolute path via [canonicalizePath] anchored at
//     [WorkspaceRef.ModuleRoot]. Empty filenames are left empty (a
//     wrapper that produces these is buggy; the engine surfaces
//     them as-is for debugging).
//
//   - Position (output.Diagnostic.Pos.Line / Column): copied as 1-
//     indexed integers. A subprocess that emits 0-indexed columns
//     must convert before populating [SubprocDiagnostic]; the
//     framework cannot tell from the values alone. Zero is the
//     "unknown column" sentinel preserved by [output.Position].
//
//   - Related (output.Diagnostic.Related): subprocess multi-line
//     trail (e.g. `unused`'s "U1000 was declared but not used; see
//     also X at file:line") maps element-for-element via
//     [canonicalizeRelated]. If the subprocess emits no related
//     info, leave Related nil; do not fold it into Message.
//
//   - SourceLines / SuggestedFixes: T3.1 does not synthesize these.
//     Per-linter wrappers may set them if the subprocess emits
//     enough info; otherwise leave zero.
//
// Deterministic ordering is the [output.Sort] caller's
// responsibility (run.go sorts before printing); Canonicalize does
// not sort.

// SubprocDiagnostic is the intermediate value a per-linter wrapper
// builds while decoding subprocess output. It is one-to-one with
// [output.Diagnostic] except that file paths are not yet
// canonicalized and Severity is still the subprocess's native
// vocabulary.
//
// Wrappers can construct SubprocDiagnostic values directly from
// json.Unmarshal-style decoding without an adapter; the fields are
// kept Go-idiomatic (no struct tags) because no on-disk shape needs
// to match exactly. The wrapper owns its own json shape.
type SubprocDiagnostic struct {
	// Message is the diagnostic text. Required.
	Message string

	// Severity is whatever string the subprocess emits ("error",
	// "warning", "info", "U1000", "high", …). [mapSeverity]
	// classifies it into the canonical set.
	Severity string

	// File / Line / Column locate the diagnostic. File may be
	// relative to the workspace root; [Canonicalize] resolves it to
	// absolute.
	File   string
	Line   int
	Column int

	// Related is the optional multi-line trail. File paths inside
	// Related are canonicalized the same way as the primary Pos.
	Related []SubprocRelated
}

// SubprocRelated is one entry in a subprocess's multi-line trail.
type SubprocRelated struct {
	Message string
	File    string
	Line    int
	Column  int
}

// Canonicalize converts a slice of [SubprocDiagnostic] into
// [output.Diagnostic] per the rules documented above. linterName is
// the [Runner.Name] value to attribute every diagnostic to.
// workspace.ModuleRoot is used to anchor relative paths; if it is
// empty, paths are left in whatever shape the subprocess produced.
//
// Canonicalize is pure: it never reads the filesystem, never spawns,
// never returns an error. Empty subprocs is a valid input and
// produces a nil result.
func Canonicalize(linterName string, workspace WorkspaceRef, subprocs []SubprocDiagnostic) []output.Diagnostic {
	if len(subprocs) == 0 {
		return nil
	}
	out := make([]output.Diagnostic, 0, len(subprocs))
	for i := range subprocs {
		s := &subprocs[i]
		d := output.Diagnostic{
			Linter:   linterName,
			Message:  strings.TrimRight(s.Message, " \t\r\n"),
			Severity: mapSeverity(s.Severity),
			Pos: output.Position{
				Filename: canonicalizePath(workspace.ModuleRoot, s.File),
				Line:     s.Line,
				Column:   s.Column,
			},
		}
		if len(s.Related) > 0 {
			d.Related = canonicalizeRelated(workspace.ModuleRoot, s.Related)
		}
		out = append(out, d)
	}
	return out
}

// mapSeverity collapses a subprocess's native severity vocabulary
// onto plaid-lint's three canonical levels. The mapping is case-
// insensitive and recognizes the upstream-common set: error / err /
// high → SeverityError; warning / warn / medium → SeverityWarning;
// info / note / low / hint → SeverityInfo. Anything else (including
// the empty string) collapses to SeverityError, matching the
// printers' sanitizer fallback.
func mapSeverity(raw string) output.Severity {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "error", "err", "high", "fatal":
		return output.SeverityError
	case "warning", "warn", "medium":
		return output.SeverityWarning
	case "info", "note", "low", "hint":
		return output.SeverityInfo
	default:
		return output.SeverityError
	}
}

// canonicalizePath resolves p against moduleRoot, returning an
// absolute, lexically-cleaned path. p that is already absolute is
// cleaned but not re-rooted. Empty p remains empty (no synthesis).
// If moduleRoot itself is empty or non-absolute, p is returned
// cleaned but not re-rooted — the caller is responsible for passing
// a sane ModuleRoot.
func canonicalizePath(moduleRoot, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if moduleRoot == "" || !filepath.IsAbs(moduleRoot) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(moduleRoot, p))
}

// canonicalizeRelated applies canonicalizePath to each entry's file
// path. Messages and line/column values are copied verbatim.
func canonicalizeRelated(moduleRoot string, related []SubprocRelated) []output.RelatedInformation {
	out := make([]output.RelatedInformation, 0, len(related))
	for i := range related {
		r := &related[i]
		out = append(out, output.RelatedInformation{
			Message: strings.TrimRight(r.Message, " \t\r\n"),
			Position: output.Position{
				Filename: canonicalizePath(moduleRoot, r.File),
				Line:     r.Line,
				Column:   r.Column,
			},
		})
	}
	return out
}
