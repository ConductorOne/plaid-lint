package output

import (
	"fmt"
	"io"
)

// Format is a stable identifier for one of the production-diagnostic
// output formats. The string values match upstream golangci-lint v2.9's
// format names so config files written for upstream are recognized
// here (this is T2.1's concern; we just keep the names compatible).
type Format string

const (
	FormatText        Format = "text"
	FormatJSON        Format = "json"
	FormatTab         Format = "tab"
	FormatHTML        Format = "html"
	FormatCheckstyle  Format = "checkstyle"
	FormatCodeClimate Format = "code-climate"
	FormatJUnitXML    Format = "junit-xml"
	FormatTeamCity    Format = "teamcity"
	FormatSarif       Format = "sarif"
)

// AllFormats returns the canonical 9 production-diagnostic format
// identifiers in a stable order.
//
// Note: golangci-lint v2's "colored-line-number" and "colored-tab"
// modes are flags on the text/tab printers respectively, not separate
// formats. The github-actions and sonarqube formats listed in earlier
// drafts of phase-2 are not upstream printers in v2.9 (github-actions
// migrated to text, sonarqube was never upstreamed); they are not
// implemented here.
func AllFormats() []Format {
	return []Format{
		FormatText, FormatJSON, FormatTab, FormatHTML,
		FormatCheckstyle, FormatCodeClimate, FormatJUnitXML,
		FormatTeamCity, FormatSarif,
	}
}

// NewPrinter constructs a printer for the named format, writing to w.
// Returns an error if the format is unrecognized.
func NewPrinter(format Format, w io.Writer) (Printer, error) {
	switch format {
	case FormatText:
		return NewText(w), nil
	case FormatJSON:
		return NewJSON(w), nil
	case FormatTab:
		return NewTab(w), nil
	case FormatHTML:
		return NewHTML(w), nil
	case FormatCheckstyle:
		return NewCheckstyle(w), nil
	case FormatCodeClimate:
		return NewCodeClimate(w), nil
	case FormatJUnitXML:
		return NewJUnitXML(w), nil
	case FormatTeamCity:
		return NewTeamCity(w), nil
	case FormatSarif:
		return NewSarif(w), nil
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}
}
