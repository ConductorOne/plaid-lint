package output

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// Tab prints diagnostics with tabs as field separators, aligned via
// text/tabwriter. Format: "<pos>\t<linter>\t<message>\n" when
// PrintLinterName is true, or "<pos>\t<message>\n" otherwise.
//
// As with Text, colored output (golangci-lint's `colored-tab` mode) is
// the caller's choice: plaid-lint exposes ColoredTab as a config flag
// on the same printer rather than a separate printer type, matching
// upstream's pkg/printers.Tab.
type Tab struct {
	w               io.Writer
	PrintLinterName bool
}

func NewTab(w io.Writer) *Tab {
	return &Tab{w: w, PrintLinterName: true}
}

func (p *Tab) Print(diags []Diagnostic) error {
	tw := tabwriter.NewWriter(p.w, 0, 0, 2, ' ', 0)
	for i := range diags {
		d := &diags[i]
		text := d.Message
		if p.PrintLinterName {
			text = fmt.Sprintf("%s\t%s", d.Linter, text)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", formatPos(d.Pos), text); err != nil {
			return err
		}
	}
	return tw.Flush()
}
