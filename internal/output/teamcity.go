package output

import (
	"fmt"
	"io"
	"strings"
)

// TeamCity prints diagnostics as TeamCity service messages.
// https://www.jetbrains.com/help/teamcity/service-messages.html
//
// Each unique linter emits one inspectionType service message, followed
// by one inspection instance per diagnostic.
type TeamCity struct {
	w         io.Writer
	sanitizer *severitySanitizer
}

const defaultTeamCitySeverity = "ERROR"

// Field-length caps from the TeamCity spec.
const (
	tcSmallLimit = 255
	tcLargeLimit = 4000
)

func NewTeamCity(w io.Writer) *TeamCity {
	return &TeamCity{
		w: w,
		sanitizer: newSeveritySanitizer(
			[]string{"INFO", defaultTeamCitySeverity, "WARNING", "WEAK WARNING"},
			defaultTeamCitySeverity,
		),
	}
}

func (p *TeamCity) Print(diags []Diagnostic) error {
	seen := map[string]struct{}{}
	for i := range diags {
		d := &diags[i]
		if _, ok := seen[d.Linter]; !ok {
			seen[d.Linter] = struct{}{}
			if _, err := fmt.Fprintf(p.w,
				"##teamcity[inspectionType id='%s' name='%s' description='%s' category='%s']\n",
				teamcityAttr(d.Linter, tcSmallLimit),
				teamcityAttr(d.Linter, tcSmallLimit),
				teamcityAttr(d.Linter, tcLargeLimit),
				teamcityAttr("Golangci-lint reports", tcSmallLimit),
			); err != nil {
				return err
			}
		}
		sev := p.sanitizer.sanitize(strings.ToUpper(string(d.Severity)))
		if _, err := fmt.Fprintf(p.w,
			"##teamcity[inspection typeId='%s' message='%s' file='%s' line='%d' SEVERITY='%s']\n",
			teamcityAttr(d.Linter, tcSmallLimit),
			teamcityAttr(d.Message, tcLargeLimit),
			teamcityAttr(d.Pos.Filename, tcLargeLimit),
			d.Pos.Line,
			sev,
		); err != nil {
			return err
		}
	}
	return nil
}

func teamcityAttr(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		repl := string(r)
		switch r {
		case '\'':
			repl = "|'"
		case '\n':
			repl = "|n"
		case '\r':
			repl = "|r"
		case '|':
			repl = "||"
		case '[':
			repl = "|["
		case ']':
			repl = "|]"
		}
		replLen := 1
		if repl != string(r) {
			replLen = len(repl)
		}
		if used+replLen > limit {
			break
		}
		b.WriteString(repl)
		used += replLen
	}
	return b.String()
}
