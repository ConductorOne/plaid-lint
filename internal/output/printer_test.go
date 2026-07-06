package output

import (
	"bytes"
	"testing"
)

// TestAllFormatsRegistered ensures NewPrinter handles every name returned
// by AllFormats(), and rejects unknown formats.
func TestAllFormatsRegistered(t *testing.T) {
	for _, f := range AllFormats() {
		p, err := NewPrinter(f, &bytes.Buffer{})
		if err != nil {
			t.Errorf("NewPrinter(%q): %v", f, err)
			continue
		}
		if p == nil {
			t.Errorf("NewPrinter(%q): nil printer", f)
		}
	}
	if _, err := NewPrinter("nope", &bytes.Buffer{}); err == nil {
		t.Errorf("NewPrinter(\"nope\") should have failed")
	}
}

// TestDeterminism re-renders each fixture across each format multiple
// times and asserts bytes are identical. Catches map-iteration leaks
// (a recurring upstream regression in JSON and SARIF).
func TestDeterminism(t *testing.T) {
	for _, name := range fixtureNames() {
		diags := fixtures[name]
		// Pre-sort so we test the printer's own determinism, not the
		// sort step's.
		sorted := append([]Diagnostic(nil), diags...)
		Sort(sorted)
		for _, format := range AllFormats() {
			t.Run(string(format)+"/"+name, func(t *testing.T) {
				var first []byte
				for i := 0; i < 5; i++ {
					out := renderFor(t, format, sorted)
					if first == nil {
						first = out
						continue
					}
					if !bytes.Equal(first, out) {
						t.Fatalf("non-deterministic output on iteration %d", i)
					}
				}
			})
		}
	}
}

func TestSortStable(t *testing.T) {
	in := []Diagnostic{
		{Linter: "b", Pos: Position{Filename: "z.go", Line: 1}},
		{Linter: "a", Pos: Position{Filename: "a.go", Line: 10}},
		{Linter: "a", Pos: Position{Filename: "a.go", Line: 2}},
		{Linter: "c", Pos: Position{Filename: "a.go", Line: 2, Column: 5}},
	}
	Sort(in)
	want := []string{"a.go:2 a", "a.go:2:5 c", "a.go:10 a", "z.go:1 b"}
	for i, d := range in {
		got := d.PosString() + " " + d.Linter
		if got != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got, want[i])
		}
	}
}
