package output

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// JUnitXML prints diagnostics in the JUnit XML format. There is no
// official JUnit XML schema; the shape here matches upstream golangci-lint
// v2.9, which in turn matches testmoapp/junitxml's "expected" tree.
// https://github.com/testmoapp/junitxml
//
// Diagnostics are grouped per file into a <testsuite> with one <testcase>
// per diagnostic. Each test case carries a <failure> child holding the
// rendered position + linter + severity + source snippet.
type JUnitXML struct {
	w        io.Writer
	Extended bool
}

func NewJUnitXML(w io.Writer) *JUnitXML {
	return &JUnitXML{w: w}
}

type junitSuites struct {
	XMLName    xml.Name     `xml:"testsuites"`
	TestSuites []junitSuite `xml:"testsuite"`
}

type junitSuite struct {
	XMLName   xml.Name    `xml:"testsuite"`
	Suite     string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Errors    int         `xml:"errors,attr"`
	Failures  int         `xml:"failures,attr"`
	TestCases []junitCase `xml:"testcase"`
}

type junitCase struct {
	Name      string       `xml:"name,attr"`
	ClassName string       `xml:"classname,attr"`
	Failure   junitFailure `xml:"failure"`
	File      string       `xml:"file,attr,omitempty"`
	Line      int          `xml:"line,attr,omitempty"`
}

type junitFailure struct {
	Message string `xml:"message,attr"`
	Type    string `xml:"type,attr"`
	Content string `xml:",cdata"`
}

func (p *JUnitXML) Print(diags []Diagnostic) error {
	suites := map[string]*junitSuite{}
	var order []string
	for i := range diags {
		d := &diags[i]
		name := d.Pos.Filename
		s, ok := suites[name]
		if !ok {
			s = &junitSuite{Suite: name}
			suites[name] = s
			order = append(order, name)
		}
		s.Tests++
		s.Failures++

		posStr := d.PosString()
		tc := junitCase{
			Name:      d.Linter,
			ClassName: posStr,
			Failure: junitFailure{
				Type:    string(d.Severity),
				Message: posStr + ": " + d.Message,
				Content: fmt.Sprintf("%s: %s\nCategory: %s\nFile: %s\nLine: %d\nDetails: %s",
					d.Severity, d.Message, d.Linter, d.Pos.Filename, d.Pos.Line,
					strings.Join(d.SourceLines, "\n")),
			},
		}
		if p.Extended {
			tc.File = d.Pos.Filename
			tc.Line = d.Pos.Line
		}
		s.TestCases = append(s.TestCases, tc)
	}

	sort.Strings(order)
	out := junitSuites{}
	for _, name := range order {
		out.TestSuites = append(out.TestSuites, *suites[name])
	}

	enc := xml.NewEncoder(p.w)
	enc.Indent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	return nil
}
