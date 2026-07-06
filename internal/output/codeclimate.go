package output

import (
	"crypto/md5" //nolint:gosec // matches upstream; not a security boundary
	"encoding/json"
	"fmt"
	"io"
)

const defaultCodeClimateSeverity = "critical"

// CodeClimate prints diagnostics in the Code Climate spec format,
// which is also what GitLab CI's "Code Quality" report expects.
// https://github.com/codeclimate/platform/blob/HEAD/spec/analyzers/SPEC.md
// https://docs.gitlab.com/ee/ci/testing/code_quality.html#code-quality-report-format
//
// Output is a JSON array of issue objects.
type CodeClimate struct {
	w         io.Writer
	sanitizer *severitySanitizer
}

func NewCodeClimate(w io.Writer) *CodeClimate {
	return &CodeClimate{
		w: w,
		sanitizer: newSeveritySanitizer(
			[]string{"info", "minor", "major", defaultCodeClimateSeverity, "blocker"},
			defaultCodeClimateSeverity,
		),
	}
}

type codeClimateIssue struct {
	Description string `json:"description"`
	CheckName   string `json:"check_name"`
	Severity    string `json:"severity,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Location    struct {
		Path  string `json:"path"`
		Lines struct {
			Begin int `json:"begin"`
		} `json:"lines"`
	} `json:"location"`
}

func (p *CodeClimate) Print(diags []Diagnostic) error {
	out := make([]codeClimateIssue, 0, len(diags))
	for i := range diags {
		d := &diags[i]
		issue := codeClimateIssue{
			Description: d.Description(),
			CheckName:   d.Linter,
			Severity:    p.sanitizer.sanitize(string(d.Severity)),
			Fingerprint: fingerprint(d),
		}
		issue.Location.Path = d.Pos.Filename
		issue.Location.Lines.Begin = d.Pos.Line
		out = append(out, issue)
	}
	return json.NewEncoder(p.w).Encode(out)
}

// fingerprint matches upstream's Issue.Fingerprint(): md5 of
// "<filename><text><firstSourceLine>" rendered as uppercase hex. The
// hash isn't a security boundary; it deduplicates issues across runs.
func fingerprint(d *Diagnostic) string {
	firstLine := ""
	if len(d.SourceLines) > 0 {
		firstLine = d.SourceLines[0]
	}
	h := md5.New() //nolint:gosec // matches upstream
	_, _ = fmt.Fprintf(h, "%s%s%s", d.Pos.Filename, d.Message, firstLine)
	return fmt.Sprintf("%X", h.Sum(nil))
}
