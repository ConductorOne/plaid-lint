// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMessageStem(t *testing.T) {
	cases := []struct {
		name   string
		linter string
		msg    string
		want   string
	}{
		{"plain", "errcheck", "Error return value not checked", "error return value not checked"},
		{"trailing-period", "revive", "exported func Foo should have comment.", "exported func foo should have comment"},
		{"linter-prefix", "errcheck", "errcheck: Error return value not checked", "error return value not checked"},
		{"bare-colon-prefix", "typecheck", ": # mypkg\n./main.go: declared", "# mypkg\n./main.go: declared"},
		{"trailing-whitespace-and-punc", "x", "  message;  ", "message"},
		{"long-truncates", "x", strings.Repeat("a", 200), strings.Repeat("a", 80)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := messageStem(tc.linter, tc.msg)
			if got != tc.want {
				t.Errorf("messageStem(%q, %q) = %q, want %q", tc.linter, tc.msg, got, tc.want)
			}
		})
	}
}

func TestCompare_Overlap(t *testing.T) {
	g := []diag{
		{File: "a.go", Line: 10, Column: 5, Linter: "errcheck", Message: "Error return value not checked", Source: "golangci"},
		{File: "b.go", Line: 3, Linter: "revive", Message: "exported func Foo.", Source: "golangci"},
	}
	c := []diag{
		{File: "a.go", Line: 10, Column: 4, Linter: "errcheck", Message: "Error return value not checked.", Source: "plaid"},
		{File: "c.go", Line: 7, Linter: "gosec", Message: "weak random", Source: "plaid"},
	}
	got := compare(g, c)
	if len(got.Overlap) != 1 {
		t.Fatalf("overlap = %d, want 1", len(got.Overlap))
	}
	if len(got.GolangciOnly) != 1 {
		t.Fatalf("golangci-only = %d, want 1", len(got.GolangciOnly))
	}
	if got.GolangciOnly[0].Linter != "revive" {
		t.Errorf("golangci-only[0].Linter = %q, want revive", got.GolangciOnly[0].Linter)
	}
	if len(got.PlaidOnly) != 1 {
		t.Fatalf("plaid-only = %d, want 1", len(got.PlaidOnly))
	}
	if got.PlaidOnly[0].Linter != "gosec" {
		t.Errorf("plaid-only[0].Linter = %q, want gosec", got.PlaidOnly[0].Linter)
	}
}

func TestCompare_PerLinterStats(t *testing.T) {
	g := []diag{
		{File: "a.go", Line: 1, Linter: "errcheck", Message: "err"},
		{File: "a.go", Line: 2, Linter: "errcheck", Message: "err2"},
		{File: "a.go", Line: 3, Linter: "revive", Message: "rev"},
	}
	c := []diag{
		{File: "a.go", Line: 1, Linter: "errcheck", Message: "err"},
		{File: "a.go", Line: 4, Linter: "gosec", Message: "sec"},
	}
	got := compare(g, c)
	statsByName := map[string]linterStat{}
	for _, s := range got.PerLinter {
		statsByName[s.Linter] = s
	}
	if s := statsByName["errcheck"]; s.Golangci != 2 || s.Plaid != 1 || s.Overlap != 1 {
		t.Errorf("errcheck: %+v", s)
	}
	if s := statsByName["revive"]; s.Golangci != 1 || s.Plaid != 0 || s.GolangciOnly != 1 {
		t.Errorf("revive: %+v", s)
	}
	if s := statsByName["gosec"]; s.Plaid != 1 || s.PlaidOnly != 1 {
		t.Errorf("gosec: %+v", s)
	}
}

func TestLoadGolangci_FixtureShape(t *testing.T) {
	dir := t.TempDir()
	// The upstream wire shape — same one probed at /tmp/g.json during prep.
	contents := `{"Issues":[{"FromLinter":"errcheck","Text":"Error return value not checked",` +
		`"Severity":"","SourceLines":["x"],"Pos":{"Filename":"main.go","Offset":0,"Line":6,"Column":2},` +
		`"ExpectNoLint":false,"ExpectedNoLintLinter":""}],"Report":{"Linters":[]}}`
	path := filepath.Join(dir, "g.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := loadGolangci(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("len(ds) = %d, want 1", len(ds))
	}
	if ds[0].Linter != "errcheck" || ds[0].Line != 6 || ds[0].File != "main.go" {
		t.Errorf("ds[0] = %+v", ds[0])
	}
}

func TestLoadPlaid_FixtureShape(t *testing.T) {
	dir := t.TempDir()
	// plaid wire shape from internal/output/json.go
	contents := `{"issues":[{"linter":"errcheck","message":"Error return value not checked","severity":"warning",` +
		`"pos":{"filename":"main.go","line":6,"column":2}}]}`
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := loadPlaid(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 {
		t.Fatalf("len(ds) = %d, want 1", len(ds))
	}
	if ds[0].Linter != "errcheck" || ds[0].Line != 6 || ds[0].File != "main.go" {
		t.Errorf("ds[0] = %+v", ds[0])
	}
}

func TestLoadPlaid_EmptyIssues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if err := os.WriteFile(path, []byte(`{"issues":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := loadPlaid(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 0 {
		t.Fatalf("len(ds) = %d, want 0", len(ds))
	}
}

func TestWriteReport_Smoke(t *testing.T) {
	c := compare(
		[]diag{{File: "a.go", Line: 1, Linter: "errcheck", Message: "x"}},
		[]diag{{File: "a.go", Line: 1, Linter: "errcheck", Message: "x"}},
	)
	var buf bytes.Buffer
	writeReport(&buf, c, "/tmp/g.json", "/tmp/c.json")
	out := buf.String()
	for _, want := range []string{
		"# Diagnostic comparison",
		"## Summary",
		"## Per-linter breakdown",
		"## Top 20 plaid-only diagnostics",
		"## Top 20 golangci-only diagnostics",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

func TestCompare_PairingIgnoresColumnAndPunctuation(t *testing.T) {
	g := []diag{{File: "a.go", Line: 1, Column: 5, Linter: "errcheck", Message: "missing return"}}
	c := []diag{{File: "a.go", Line: 1, Column: 99, Linter: "errcheck", Message: "missing return."}}
	got := compare(g, c)
	if len(got.Overlap) != 1 {
		t.Fatalf("expected 1 overlap despite differing column & trailing period; got %d", len(got.Overlap))
	}
}
