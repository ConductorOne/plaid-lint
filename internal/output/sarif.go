package output

import (
	"encoding/json"
	"io"
)

// SARIF 2.1.0 printer.
// https://docs.oasis-open.org/sarif/sarif/v2.1.0/
//
// One run per invocation, one result per diagnostic. Tool name is fixed
// to "plaid-lint" so SARIF consumers can match the producer; the
// driver version is intentionally absent here (CLI surface concern,
// wired in by T2.4).
type Sarif struct {
	w         io.Writer
	sanitizer *severitySanitizer
}

const (
	sarifVersion         = "2.1.0"
	sarifSchemaURI       = "https://schemastore.azurewebsites.net/schemas/json/sarif-2.1.0-rtm.6.json"
	defaultSarifSeverity = "error"
)

func NewSarif(w io.Writer) *Sarif {
	return &Sarif{
		w: w,
		sanitizer: newSeveritySanitizer(
			[]string{"none", "note", "warning", defaultSarifSeverity},
			defaultSarifSeverity,
		),
	}
}

type sarifOutput struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name string `json:"name"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI   string `json:"uri"`
	Index int    `json:"index"`
}

type sarifRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
}

func (p *Sarif) Print(diags []Diagnostic) error {
	run := sarifRun{Results: make([]sarifResult, 0, len(diags))}
	run.Tool.Driver.Name = "plaid-lint"

	for i := range diags {
		d := &diags[i]
		// Per SARIF spec, startColumn defaults to 1 when omitted; we
		// always emit at least 1 so downstream validators that require
		// the field don't reject our output.
		col := d.Pos.Column
		if col < 1 {
			col = 1
		}
		run.Results = append(run.Results, sarifResult{
			RuleID:  d.Linter,
			Level:   p.sanitizer.sanitize(string(d.Severity)),
			Message: sarifMessage{Text: d.Message},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: d.Pos.Filename},
					Region: sarifRegion{
						StartLine:   d.Pos.Line,
						StartColumn: col,
					},
				},
			}},
		})
	}

	out := sarifOutput{
		Version: sarifVersion,
		Schema:  sarifSchemaURI,
		Runs:    []sarifRun{run},
	}
	return json.NewEncoder(p.w).Encode(out)
}
