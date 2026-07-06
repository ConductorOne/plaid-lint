package output

import (
	"fmt"
	"html/template"
	"io"
	"strings"
)

// HTML prints diagnostics as a single self-contained HTML page.
//
// The page structure (one <h1> + one <table> of diagnostics + a summary
// row) matches upstream's intent (a glanceable report) but does not try
// to reproduce upstream's React/Bulma/highlight.js page. The dispatch
// brief explicitly allows divergence here; the cost of pulling those CDN
// dependencies into a byte-stable golden is not justified.
type HTML struct {
	w io.Writer
}

func NewHTML(w io.Writer) *HTML {
	return &HTML{w: w}
}

type htmlRow struct {
	Pos      string
	Linter   string
	Severity string
	Message  string
	Code     string
}

type htmlData struct {
	Total int
	Rows  []htmlRow
}

const htmlTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>plaid-lint report</title>
<style>
body { font-family: -apple-system, system-ui, sans-serif; margin: 2em; }
h1 { font-size: 1.4em; }
.summary { color: #555; margin-bottom: 1em; }
table { border-collapse: collapse; width: 100%; }
th, td { border: 1px solid #ddd; padding: 6px 10px; vertical-align: top; text-align: left; font-size: 0.9em; }
th { background: #f4f4f4; }
.sev-error { color: #b00020; font-weight: 600; }
.sev-warning { color: #b07b00; }
.sev-info { color: #00569b; }
pre { margin: 0; font-size: 0.85em; white-space: pre-wrap; }
.empty { color: #777; font-style: italic; }
</style>
</head>
<body>
<h1>plaid-lint report</h1>
<div class="summary">{{.Total}} diagnostic{{if ne .Total 1}}s{{end}}</div>
{{if .Rows}}
<table>
<thead><tr><th>Position</th><th>Linter</th><th>Severity</th><th>Message</th><th>Code</th></tr></thead>
<tbody>
{{range .Rows}}<tr>
<td>{{.Pos}}</td>
<td>{{.Linter}}</td>
<td class="sev-{{.Severity}}">{{.Severity}}</td>
<td>{{.Message}}</td>
<td><pre>{{.Code}}</pre></td>
</tr>
{{end}}</tbody>
</table>
{{else}}
<p class="empty">No diagnostics.</p>
{{end}}
</body>
</html>
`

func (p *HTML) Print(diags []Diagnostic) error {
	data := htmlData{Total: len(diags)}
	for i := range diags {
		d := &diags[i]
		sev := string(d.Severity)
		if sev == "" {
			sev = string(SeverityError)
		}
		data.Rows = append(data.Rows, htmlRow{
			Pos:      d.PosString(),
			Linter:   d.Linter,
			Severity: sev,
			Message:  strings.TrimSpace(d.Message),
			Code:     strings.Join(d.SourceLines, "\n"),
		})
	}

	t, err := template.New("plaid-lint").Parse(htmlTemplate)
	if err != nil {
		return fmt.Errorf("parse html template: %w", err)
	}
	return t.Execute(p.w, data)
}
