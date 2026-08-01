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
	w             io.Writer
	sanitizer     *severitySanitizer
	runProperties map[string]any
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

// SetRunProperties attaches a SARIF §3.8 property bag to the run
// object of subsequent Print calls. nil (the default) omits the
// field entirely, keeping property-less output byte-identical to the
// pre-properties format. Producers use this to carry machine-readable
// run metadata (e.g. plaid-lint unit records the analyzed package
// and file set so a collector can aggregate across runs).
func (p *Sarif) SetRunProperties(props map[string]any) {
	p.runProperties = props
}

type sarifOutput struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool       sarifTool      `json:"tool"`
	Results    []sarifResult  `json:"results"`
	Properties map[string]any `json:"properties,omitempty"`
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
	Fixes     []sarifFix      `json:"fixes,omitempty"`
}

// sarifFix carries a SuggestedFix as a SARIF fix object
// (§3.55): one artifactChange per touched file, one replacement per
// text edit. Emitted only when a diagnostic carries fixes, so
// fix-less output is byte-identical to the pre-fix format.
type sarifFix struct {
	Description     *sarifMessage         `json:"description,omitempty"`
	ArtifactChanges []sarifArtifactChange `json:"artifactChanges"`
}

type sarifArtifactChange struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Replacements     []sarifReplacement    `json:"replacements"`
}

type sarifReplacement struct {
	DeletedRegion   sarifDeletedRegion `json:"deletedRegion"`
	InsertedContent *sarifMessage      `json:"insertedContent,omitempty"`
}

// sarifDeletedRegion is a full start/end region (unlike sarifRegion,
// whose omitted end means "rest of line" per spec §3.30).
type sarifDeletedRegion struct {
	StartLine   int `json:"startLine"`
	StartColumn int `json:"startColumn"`
	EndLine     int `json:"endLine,omitempty"`
	EndColumn   int `json:"endColumn,omitempty"`
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
	run := sarifRun{
		Results:    make([]sarifResult, 0, len(diags)),
		Properties: p.runProperties,
	}
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
		res := sarifResult{
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
			Fixes: sarifFixes(d),
		}
		run.Results = append(run.Results, res)
	}

	out := sarifOutput{
		Version: sarifVersion,
		Schema:  sarifSchemaURI,
		Runs:    []sarifRun{run},
	}
	return json.NewEncoder(p.w).Encode(out)
}

// sarifFixes converts a diagnostic's SuggestedFixes into SARIF fix
// objects: replacements grouped per touched file, edit order
// preserved.
func sarifFixes(d *Diagnostic) []sarifFix {
	if len(d.SuggestedFixes) == 0 {
		return nil
	}
	fixes := make([]sarifFix, 0, len(d.SuggestedFixes))
	for _, sf := range d.SuggestedFixes {
		if len(sf.TextEdits) == 0 {
			continue
		}
		var fix sarifFix
		if sf.Message != "" {
			fix.Description = &sarifMessage{Text: sf.Message}
		}
		// Group replacements by file, preserving first-seen file
		// order and per-file edit order.
		byFile := map[string]int{} // uri -> index into ArtifactChanges
		for _, ed := range sf.TextEdits {
			uri := ed.Start.Filename
			idx, ok := byFile[uri]
			if !ok {
				idx = len(fix.ArtifactChanges)
				byFile[uri] = idx
				fix.ArtifactChanges = append(fix.ArtifactChanges, sarifArtifactChange{
					ArtifactLocation: sarifArtifactLocation{URI: uri},
				})
			}
			repl := sarifReplacement{
				DeletedRegion: sarifDeletedRegion{
					StartLine:   ed.Start.Line,
					StartColumn: max(ed.Start.Column, 1),
					EndLine:     ed.End.Line,
					EndColumn:   max(ed.End.Column, 1),
				},
			}
			if ed.NewText != "" {
				repl.InsertedContent = &sarifMessage{Text: ed.NewText}
			}
			fix.ArtifactChanges[idx].Replacements = append(fix.ArtifactChanges[idx].Replacements, repl)
		}
		fixes = append(fixes, fix)
	}
	if len(fixes) == 0 {
		return nil
	}
	return fixes
}
