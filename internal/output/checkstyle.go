package output

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
)

const defaultCheckstyleSeverity = "error"

// Checkstyle prints diagnostics in the Checkstyle XML format.
// https://checkstyle.org/config.html
//
// Output shape:
//
//	<?xml version="1.0" encoding="UTF-8"?>
//	<checkstyle version="5.0">
//	  <file name="...">
//	    <error column="..." line="..." message="..." severity="..." source="..."/>
//	  </file>
//	</checkstyle>
//
// Files are grouped and emitted in lexicographic order; diagnostics
// within a file preserve input order.
type Checkstyle struct {
	w         io.Writer
	sanitizer *severitySanitizer
}

func NewCheckstyle(w io.Writer) *Checkstyle {
	return &Checkstyle{
		w: w,
		// https://checkstyle.org/config.html#Severity
		sanitizer: newSeveritySanitizer(
			[]string{"ignore", "info", "warning", defaultCheckstyleSeverity},
			defaultCheckstyleSeverity,
		),
	}
}

type checkstyleOutput struct {
	XMLName xml.Name          `xml:"checkstyle"`
	Version string            `xml:"version,attr"`
	Files   []*checkstyleFile `xml:"file"`
}

type checkstyleFile struct {
	Name   string             `xml:"name,attr"`
	Errors []*checkstyleError `xml:"error"`
}

type checkstyleError struct {
	Column   int    `xml:"column,attr"`
	Line     int    `xml:"line,attr"`
	Message  string `xml:"message,attr"`
	Severity string `xml:"severity,attr"`
	Source   string `xml:"source,attr"`
}

func (p *Checkstyle) Print(diags []Diagnostic) error {
	out := checkstyleOutput{Version: "5.0"}
	idx := map[string]*checkstyleFile{}
	var order []string
	for i := range diags {
		d := &diags[i]
		f, ok := idx[d.Pos.Filename]
		if !ok {
			f = &checkstyleFile{Name: d.Pos.Filename}
			idx[d.Pos.Filename] = f
			order = append(order, d.Pos.Filename)
		}
		f.Errors = append(f.Errors, &checkstyleError{
			Column:   d.Pos.Column,
			Line:     d.Pos.Line,
			Message:  d.Message,
			Severity: p.sanitizer.sanitize(string(d.Severity)),
			Source:   d.Linter,
		})
	}
	sort.Strings(order)
	for _, name := range order {
		out.Files = append(out.Files, idx[name])
	}

	if _, err := fmt.Fprint(p.w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(p.w)
	enc.Indent("", "  ")
	if err := enc.Encode(&out); err != nil {
		return err
	}
	_, err := fmt.Fprintln(p.w)
	return err
}
