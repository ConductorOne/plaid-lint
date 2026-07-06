package output

import (
	"fmt"
)

// Printer is the common interface implemented by every output format.
// All printers are pure formatters: take diagnostics, write to a writer.
// They have no side effects, no global state, and no configuration
// concerns (those are CLI surface, handled by T2.4).
type Printer interface {
	// Print writes diagnostics to the printer's writer. Implementations
	// must not retain references to the input slice after Print returns.
	Print(diags []Diagnostic) error
}

// formatPos formats a Position as "file:line[:column]". An empty filename
// is rendered as "-", matching token.Position.String() for unknown
// locations. A zero Column is omitted.
func formatPos(p Position) string {
	name := p.Filename
	if name == "" {
		name = "-"
	}
	if p.Column != 0 {
		return fmt.Sprintf("%s:%d:%d", name, p.Line, p.Column)
	}
	return fmt.Sprintf("%s:%d", name, p.Line)
}

// severitySanitizer maps free-form Severity strings onto the closed set
// each output format requires. Mirrors upstream's pkg/printers.severitySanitizer
// without the warning-emission behavior (we record the unsupported severities
// and the caller decides whether to log).
type severitySanitizer struct {
	allowed         []string
	defaultSeverity string

	unsupported map[string]struct{}
}

func newSeveritySanitizer(allowed []string, defaultSeverity string) *severitySanitizer {
	return &severitySanitizer{allowed: allowed, defaultSeverity: defaultSeverity}
}

func (s *severitySanitizer) sanitize(severity string) string {
	if severity == "" {
		return s.defaultSeverity
	}
	for _, a := range s.allowed {
		if a == severity {
			return severity
		}
	}
	if s.unsupported == nil {
		s.unsupported = map[string]struct{}{}
	}
	s.unsupported[severity] = struct{}{}
	return s.defaultSeverity
}

