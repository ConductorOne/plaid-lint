package output

import (
	"fmt"
	"io"
)

// Text is the human-friendly textual printer (golangci-lint's default
// "text" format). One diagnostic per line: "file:line[:col]: message".
// Optional source-line preview and column underline match upstream.
//
// Color output is intentionally omitted from this port: plaid-lint's
// CLI surface (T2.4) decides whether to wrap the writer in a colorizer
// at the call site, keeping printers pure.
type Text struct {
	w               io.Writer
	PrintLinterName bool
	PrintIssuedLine bool
}

func NewText(w io.Writer) *Text {
	return &Text{w: w, PrintLinterName: true, PrintIssuedLine: true}
}

func (p *Text) Print(diags []Diagnostic) error {
	for i := range diags {
		d := &diags[i]
		if err := p.printOne(d); err != nil {
			return err
		}
		if p.PrintIssuedLine {
			if err := p.printSource(d); err != nil {
				return err
			}
			if err := p.printUnderline(d); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Text) printOne(d *Diagnostic) error {
	text := d.Message
	if p.PrintLinterName {
		text = fmt.Sprintf("%s (%s)", text, d.Linter)
	}
	_, err := fmt.Fprintf(p.w, "%s: %s\n", formatPos(d.Pos), text)
	return err
}

func (p *Text) printSource(d *Diagnostic) error {
	for _, line := range d.SourceLines {
		if _, err := fmt.Fprintln(p.w, line); err != nil {
			return err
		}
	}
	return nil
}

func (p *Text) printUnderline(d *Diagnostic) error {
	if len(d.SourceLines) != 1 || d.Pos.Column == 0 {
		return nil
	}
	col0 := d.Pos.Column - 1
	line := d.SourceLines[0]
	buf := make([]byte, 0, col0)
	for j := 0; j < len(line) && j < col0; j++ {
		if line[j] == '\t' {
			buf = append(buf, '\t')
		} else {
			buf = append(buf, ' ')
		}
	}
	_, err := fmt.Fprintf(p.w, "%s^\n", buf)
	return err
}
