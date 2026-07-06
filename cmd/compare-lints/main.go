// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command compare-lints reads two diagnostic JSON files (one from
// golangci-lint v2.x, one from plaid-lint) and produces a markdown
// report of the per-linter overlap and divergence.
//
// JSON shapes the tool bridges:
//
//	golangci-lint v2.9 (upstream):
//	  {"Issues":[{"FromLinter":"<name>","Text":"<msg>",
//	              "Pos":{"Filename":"file","Line":N,"Column":N,"Offset":N},
//	              "Severity":"","SourceLines":[...], ...}],
//	   "Report":{...}}
//
//	plaid-lint (this repo, internal/output/json.go):
//	  {"issues":[{"linter":"<name>","message":"<msg>","severity":"...",
//	               "pos":{"filename":"file","line":N,"column":N}, ...}]}
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const messageStemLen = 80

// golangciIssue mirrors the upstream golangci-lint v2 wire shape.
// Only the fields the comparator uses are decoded.
type golangciIssue struct {
	FromLinter string `json:"FromLinter"`
	Text       string `json:"Text"`
	Severity   string `json:"Severity"`
	Pos        struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
		Column   int    `json:"Column"`
	} `json:"Pos"`
}

type golangciFile struct {
	Issues []golangciIssue `json:"Issues"`
}

// plaidIssue mirrors output.Diagnostic for decode.
type plaidIssue struct {
	Linter   string `json:"linter"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Pos      struct {
		Filename string `json:"filename"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
	} `json:"pos"`
}

type plaidFile struct {
	Issues []plaidIssue `json:"issues"`
}

// diag is the unified comparator shape. Both inputs collapse to this.
type diag struct {
	File    string
	Line    int
	Column  int
	Linter  string
	Message string // full text, preserved for the divergence dump
	Source  string // "golangci" or "plaid", for reporting
}

// key is the pairing identity: (file, line, linter, message-stem).
// Column and trailing punctuation are deliberately excluded so
// diagnostics that differ only in those fields still pair.
type key struct {
	File   string
	Line   int
	Linter string
	Stem   string
}

func (d diag) key() key {
	return key{
		File:   normalizePath(d.File),
		Line:   d.Line,
		Linter: d.Linter,
		Stem:   messageStem(d.Linter, d.Message),
	}
}

// normalizePath strips any volume prefix and collapses to forward slashes.
// Both tools may emit absolute or workspace-relative paths; for pairing we
// only need stability across the two inputs of the same run.
func normalizePath(p string) string {
	p = filepath.ToSlash(p)
	return p
}

// messageStem normalizes the message for pairing:
//   - lowercase
//   - strip the leading "<linter>: " prefix (golangci-lint sometimes prepends it
//     to the Text field, sometimes the wrapper does)
//   - strip surrounding whitespace and trailing punctuation
//   - truncate to messageStemLen runes
func messageStem(linter, msg string) string {
	s := strings.ToLower(strings.TrimSpace(msg))
	// Strip "<linter>: " prefix if present (golangci-lint sometimes carries it).
	prefix := strings.ToLower(linter) + ": "
	s = strings.TrimPrefix(s, prefix)
	s = strings.TrimPrefix(s, ": ") // bare-colon prefix variant
	// Drop trailing punctuation that varies between linters.
	s = strings.TrimRight(s, ".,;:!? \t\r\n")
	if len(s) > messageStemLen {
		s = s[:messageStemLen]
	}
	return s
}

func loadGolangci(path string) ([]diag, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f golangciFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make([]diag, 0, len(f.Issues))
	for _, i := range f.Issues {
		out = append(out, diag{
			File:    i.Pos.Filename,
			Line:    i.Pos.Line,
			Column:  i.Pos.Column,
			Linter:  i.FromLinter,
			Message: i.Text,
			Source:  "golangci",
		})
	}
	return out, nil
}

func loadPlaid(path string) ([]diag, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f plaidFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make([]diag, 0, len(f.Issues))
	for _, i := range f.Issues {
		out = append(out, diag{
			File:    i.Pos.Filename,
			Line:    i.Pos.Line,
			Column:  i.Pos.Column,
			Linter:  i.Linter,
			Message: i.Message,
			Source:  "plaid",
		})
	}
	return out, nil
}

// linterStat holds the per-linter counts for the summary table.
type linterStat struct {
	Linter       string
	Golangci     int
	Plaid      int
	Overlap      int
	GolangciOnly int
	PlaidOnly  int
}

type comparison struct {
	Golangci     []diag
	Plaid      []diag
	Overlap      []diag // pulled from the plaid side when a pair exists
	GolangciOnly []diag
	PlaidOnly  []diag
	PerLinter    []linterStat
}

func compare(golangci, plaid []diag) comparison {
	gByKey := map[key][]int{}
	for i, d := range golangci {
		k := d.key()
		gByKey[k] = append(gByKey[k], i)
	}
	cByKey := map[key][]int{}
	for i, d := range plaid {
		k := d.key()
		cByKey[k] = append(cByKey[k], i)
	}

	var overlap, gOnly, cOnly []diag
	matchedG := make([]bool, len(golangci))
	matchedC := make([]bool, len(plaid))

	for k, gIdxs := range gByKey {
		cIdxs, ok := cByKey[k]
		if !ok {
			continue
		}
		// Pair the min(len(gIdxs), len(cIdxs)) entries.
		n := len(gIdxs)
		if len(cIdxs) < n {
			n = len(cIdxs)
		}
		for i := 0; i < n; i++ {
			matchedG[gIdxs[i]] = true
			matchedC[cIdxs[i]] = true
			overlap = append(overlap, plaid[cIdxs[i]])
		}
	}
	for i, d := range golangci {
		if !matchedG[i] {
			gOnly = append(gOnly, d)
		}
	}
	for i, d := range plaid {
		if !matchedC[i] {
			cOnly = append(cOnly, d)
		}
	}

	// Per-linter stats. Each side's full counts, plus paired counts.
	gCount := map[string]int{}
	cCount := map[string]int{}
	oCount := map[string]int{}
	gOnlyCount := map[string]int{}
	cOnlyCount := map[string]int{}
	for _, d := range golangci {
		gCount[d.Linter]++
	}
	for _, d := range plaid {
		cCount[d.Linter]++
	}
	for _, d := range overlap {
		oCount[d.Linter]++
	}
	for _, d := range gOnly {
		gOnlyCount[d.Linter]++
	}
	for _, d := range cOnly {
		cOnlyCount[d.Linter]++
	}
	names := map[string]bool{}
	for k := range gCount {
		names[k] = true
	}
	for k := range cCount {
		names[k] = true
	}
	stats := make([]linterStat, 0, len(names))
	for n := range names {
		stats = append(stats, linterStat{
			Linter:       n,
			Golangci:     gCount[n],
			Plaid:      cCount[n],
			Overlap:      oCount[n],
			GolangciOnly: gOnlyCount[n],
			PlaidOnly:  cOnlyCount[n],
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Linter < stats[j].Linter })

	return comparison{
		Golangci:     golangci,
		Plaid:      plaid,
		Overlap:      overlap,
		GolangciOnly: gOnly,
		PlaidOnly:  cOnly,
		PerLinter:    stats,
	}
}

func sortDiags(ds []diag) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Linter != b.Linter {
			return a.Linter < b.Linter
		}
		return a.Message < b.Message
	})
}

func writeReport(w io.Writer, c comparison, golangciPath, plaidPath string) {
	fmt.Fprintln(w, "# Diagnostic comparison: golangci-lint vs plaid-lint")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "- golangci input: `%s`\n", golangciPath)
	fmt.Fprintf(w, "- plaid input:  `%s`\n", plaidPath)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Summary")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "| Side | Total diagnostics |\n|---|---:|\n")
	fmt.Fprintf(w, "| golangci-lint | %d |\n", len(c.Golangci))
	fmt.Fprintf(w, "| plaid-lint  | %d |\n", len(c.Plaid))
	fmt.Fprintf(w, "| Paired (overlap) | %d |\n", len(c.Overlap))
	fmt.Fprintf(w, "| golangci-only | %d |\n", len(c.GolangciOnly))
	fmt.Fprintf(w, "| plaid-only  | %d |\n", len(c.PlaidOnly))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Per-linter breakdown")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Linter | golangci | plaid | overlap | golangci-only | plaid-only |")
	fmt.Fprintln(w, "|---|---:|---:|---:|---:|---:|")
	for _, s := range c.PerLinter {
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d |\n",
			s.Linter, s.Golangci, s.Plaid, s.Overlap, s.GolangciOnly, s.PlaidOnly)
	}
	fmt.Fprintln(w)

	writeTopN(w, "plaid-only", c.PlaidOnly, 20)
	writeTopN(w, "golangci-only", c.GolangciOnly, 20)
}

func writeTopN(w io.Writer, label string, ds []diag, n int) {
	fmt.Fprintf(w, "## Top %d %s diagnostics\n\n", n, label)
	if len(ds) == 0 {
		fmt.Fprintln(w, "_None._")
		fmt.Fprintln(w)
		return
	}
	sortDiags(ds)
	limit := n
	if len(ds) < limit {
		limit = len(ds)
	}
	for i := 0; i < limit; i++ {
		d := ds[i]
		col := ""
		if d.Column > 0 {
			col = fmt.Sprintf(":%d", d.Column)
		}
		// Single-line excerpt of the message.
		msg := strings.ReplaceAll(d.Message, "\n", " \\ ")
		fmt.Fprintf(w, "- `%s:%d%s` **%s**: %s\n", d.File, d.Line, col, d.Linter, msg)
	}
	fmt.Fprintln(w)
}

func writeDetailed(path string, c comparison) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# Detailed divergence dump")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## All golangci-only diagnostics")
	fmt.Fprintln(f)
	sortDiags(c.GolangciOnly)
	for _, d := range c.GolangciOnly {
		fmt.Fprintf(f, "- `%s:%d:%d` **%s**: %s\n", d.File, d.Line, d.Column, d.Linter,
			strings.ReplaceAll(d.Message, "\n", " \\ "))
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## All plaid-only diagnostics")
	fmt.Fprintln(f)
	sortDiags(c.PlaidOnly)
	for _, d := range c.PlaidOnly {
		fmt.Fprintf(f, "- `%s:%d:%d` **%s**: %s\n", d.File, d.Line, d.Column, d.Linter,
			strings.ReplaceAll(d.Message, "\n", " \\ "))
	}
	return nil
}

func main() {
	golangciPath := flag.String("golangci", "", "path to golangci-lint JSON output")
	plaidPath := flag.String("plaid", "", "path to plaid-lint JSON output")
	outPath := flag.String("out", "", "path to write markdown report")
	detailed := flag.Bool("detailed", false, "also dump every divergence to <out>.diff.md")
	flag.Parse()

	if *golangciPath == "" || *plaidPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: compare-lints --golangci=PATH --plaid=PATH --out=PATH [--detailed]")
		os.Exit(2)
	}

	gDiags, err := loadGolangci(*golangciPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare-lints:", err)
		os.Exit(1)
	}
	cDiags, err := loadPlaid(*plaidPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare-lints:", err)
		os.Exit(1)
	}
	c := compare(gDiags, cDiags)

	out, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "compare-lints:", err)
		os.Exit(1)
	}
	defer out.Close()
	writeReport(out, c, *golangciPath, *plaidPath)

	if *detailed {
		diffPath := strings.TrimSuffix(*outPath, ".md") + ".diff.md"
		if err := writeDetailed(diffPath, c); err != nil {
			fmt.Fprintln(os.Stderr, "compare-lints: write detailed:", err)
			os.Exit(1)
		}
	}
}
