package output

import (
	"encoding/json"
	"io"
)

// JSON prints diagnostics as a single JSON object with an "issues"
// array. This is plaid-lint's native production-diagnostic shape;
// distinct from upstream's pkg/printers.JSON wire format (which wraps
// pkg/result.Issue and includes a "Report" sibling with build metadata).
//
// Upstream byte-comparability is not a goal for the json printer: the
// upstream Issue type carries fields (Pkg, SourceLines, HunkPos, ...) that
// don't translate to plaid-lint's diagnostic model. The structural
// equivalent is documented in json.upstream.diff.md.
type JSON struct {
	w io.Writer
}

func NewJSON(w io.Writer) *JSON {
	return &JSON{w: w}
}

// jsonPayload is the wire shape: { "issues": [...] }. A future
// addition of "report" metadata (matching upstream) would slot here
// without breaking consumers that ignore unknown fields.
type jsonPayload struct {
	Issues []Diagnostic `json:"issues"`
}

func (p *JSON) Print(diags []Diagnostic) error {
	if diags == nil {
		diags = []Diagnostic{}
	}
	enc := json.NewEncoder(p.w)
	return enc.Encode(jsonPayload{Issues: diags})
}
