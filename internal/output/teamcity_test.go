package output

import (
	"strings"
	"testing"
)

func TestTeamCityEscapesStringAttributes(t *testing.T) {
	got := string(renderFor(t, FormatTeamCity, []Diagnostic{
		{
			Linter:   "lint'|[x]\n",
			Message:  "msg'|[x]\n",
			Severity: SeverityWarning,
			Pos: Position{
				Filename: "pkg/o'hara|[x]\nnext\r.go",
				Line:     12,
			},
		},
	}))

	for _, want := range []string{
		"inspectionType id='lint|'|||[x|]|n'",
		"name='lint|'|||[x|]|n'",
		"description='lint|'|||[x|]|n'",
		"typeId='lint|'|||[x|]|n'",
		"message='msg|'|||[x|]|n'",
		"file='pkg/o|'hara|||[x|]|nnext|r.go'",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("TeamCity output missing escaped attribute %q:\n%s", want, got)
		}
	}
}

func TestTeamCityAttrDoesNotSplitEscapeAtLimit(t *testing.T) {
	if got := teamcityAttr("ab[", 4); got != "ab|[" {
		t.Fatalf("teamcityAttr exact escape fit: got %q, want %q", got, "ab|[")
	}
	if got := teamcityAttr("abc[", 4); got != "abc" {
		t.Fatalf("teamcityAttr split escape: got %q, want %q", got, "abc")
	}
}
