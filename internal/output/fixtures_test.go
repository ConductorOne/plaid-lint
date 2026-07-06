package output

// Fixtures used by the golden tests. Each fixture name maps to a slice
// of Diagnostic values; the slice is then rendered by every printer and
// the bytes compared against the on-disk .golden file.
//
// The fixture set covers:
//
//   - empty: zero diagnostics
//   - single: one diagnostic, full fields populated
//   - multi:  N diagnostics across files, severities, and linters
//   - edge:   long messages, multi-line messages, path with spaces
var fixtures = map[string][]Diagnostic{
	"empty": nil,

	"single": {
		{
			Linter:   "errcheck",
			Message:  "Error return value of `f.Close` is not checked",
			Severity: SeverityError,
			Pos:      Position{Filename: "pkg/foo/foo.go", Line: 42, Column: 9},
			SourceLines: []string{
				"\tf.Close()",
			},
		},
	},

	"multi": {
		{
			Linter:   "errcheck",
			Message:  "Error return value of `f.Close` is not checked",
			Severity: SeverityError,
			Pos:      Position{Filename: "pkg/a/a.go", Line: 10, Column: 5},
		},
		{
			Linter:   "staticcheck",
			Message:  "SA4006: this value of x is never used",
			Severity: SeverityWarning,
			Pos:      Position{Filename: "pkg/a/a.go", Line: 25, Column: 2},
		},
		{
			Linter:   "ineffassign",
			Message:  "ineffectual assignment to err",
			Severity: SeverityWarning,
			Pos:      Position{Filename: "pkg/b/b.go", Line: 7, Column: 13},
		},
		{
			Linter:   "unused",
			Message:  "func unusedHelper is unused",
			Severity: SeverityInfo,
			Pos:      Position{Filename: "pkg/c/c.go", Line: 100},
		},
	},

	"edge": {
		{
			Linter:   "govet",
			Message:  strRepeat("very long message segment. ", 20) + "end.",
			Severity: SeverityError,
			Pos:      Position{Filename: "pkg/edge/long.go", Line: 1, Column: 1},
		},
		{
			Linter:   "govet",
			Message:  "first line of message\nsecond line\nthird line",
			Severity: SeverityWarning,
			Pos:      Position{Filename: "pkg/edge/multiline.go", Line: 3, Column: 5},
		},
		{
			Linter:   "errcheck",
			Message:  "Error return value is not checked",
			Severity: SeverityError,
			Pos:      Position{Filename: "pkg/edge/path with spaces/foo.go", Line: 12, Column: 3},
		},
	},
}

func strRepeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// fixtureNames returns fixture names in a stable order so test output
// is deterministic.
func fixtureNames() []string {
	return []string{"empty", "single", "multi", "edge"}
}
