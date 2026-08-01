// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package unit

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/conductorone/plaid-lint/internal/output"
)

// Collect aggregates unit-action SARIF outputs: it merges results,
// deduplicates, applies the test-variant supersede rule, and returns
// the surviving diagnostics. It is the consumer side of the unit
// contract — the thing a Bazel validation action or a CI collector
// runs over one or many .plaid.sarif files.
//
// The supersede rule mirrors the engine's dropTestSupersededPackages:
// when run B analyzed a strict superset of run A's files (the
// in-package test archive relative to its library), A's `unused`
// findings are unsound — a symbol referenced only from the extra
// files (tests) is falsely unused — so they are dropped. Duplicate
// findings between the two runs collapse via position dedup. Runs
// without plaidUnit properties participate in dedup but never in
// supersession.
type collectRun struct {
	sourcePath string
	pkg        string
	mode       string
	goFiles    []string
	diags      []output.Diagnostic
}

// CollectResult is what Collect hands back to the CLI.
type CollectResult struct {
	// Diagnostics is the merged, deduplicated, supersede-filtered,
	// sorted stream.
	Diagnostics []output.Diagnostic

	// Superseded counts `unused` findings dropped by the test-variant
	// rule (observability: a collector run should be able to explain
	// why a finding vanished).
	Superseded int
}

// Collect reads and merges the named SARIF files.
func Collect(paths []string) (*CollectResult, error) {
	runs := make([]collectRun, 0, len(paths))
	for _, p := range paths {
		r, err := readSarifRun(p)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}

	res := &CollectResult{}

	// Supersession: run indexes whose unused findings are dropped.
	superseded := make([]bool, len(runs))
	for i := range runs {
		if runs[i].pkg == "" || len(runs[i].goFiles) == 0 {
			continue
		}
		for j := range runs {
			if i == j || runs[j].pkg != runs[i].pkg {
				continue
			}
			if strictSuperset(runs[j].goFiles, runs[i].goFiles) {
				superseded[i] = true
				break
			}
		}
	}

	seen := map[string]bool{}
	for i := range runs {
		for _, d := range runs[i].diags {
			if superseded[i] && d.Linter == "unused" {
				res.Superseded++
				continue
			}
			key := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s",
				d.Pos.Filename, d.Pos.Line, d.Pos.Column, d.Linter, d.Message)
			if seen[key] {
				continue
			}
			seen[key] = true
			res.Diagnostics = append(res.Diagnostics, d)
		}
	}
	output.Sort(res.Diagnostics)
	return res, nil
}

// strictSuperset reports whether a ⊋ b as string sets.
func strictSuperset(a, b []string) bool {
	if len(a) <= len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

// sarifDoc is the narrow read-side projection of the SARIF shape the
// unit driver writes. Unknown fields are ignored deliberately: collect
// must tolerate SARIF from newer plaid-lint versions (and, degraded,
// from other producers).
type sarifDoc struct {
	Runs []struct {
		Results []struct {
			RuleID  string `json:"ruleId"`
			Level   string `json:"level"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
			Locations []struct {
				PhysicalLocation struct {
					ArtifactLocation struct {
						URI string `json:"uri"`
					} `json:"artifactLocation"`
					Region struct {
						StartLine   int `json:"startLine"`
						StartColumn int `json:"startColumn"`
					} `json:"region"`
				} `json:"physicalLocation"`
			} `json:"locations"`
		} `json:"results"`
		Properties struct {
			PlaidUnit struct {
				Package string   `json:"package"`
				Mode    string   `json:"mode"`
				GoFiles []string `json:"goFiles"`
			} `json:"plaidUnit"`
		} `json:"properties"`
	} `json:"runs"`
}

// readSarifRun loads one SARIF file into a collectRun.
func readSarifRun(path string) (collectRun, error) {
	run := collectRun{sourcePath: path}
	body, err := os.ReadFile(path)
	if err != nil {
		return run, fmt.Errorf("collect: %w", err)
	}
	var doc sarifDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return run, fmt.Errorf("collect: parse %s: %w", path, err)
	}
	for _, r := range doc.Runs {
		if run.pkg == "" {
			run.pkg = r.Properties.PlaidUnit.Package
			run.mode = r.Properties.PlaidUnit.Mode
			run.goFiles = append([]string(nil), r.Properties.PlaidUnit.GoFiles...)
			sort.Strings(run.goFiles)
		}
		for _, res := range r.Results {
			d := output.Diagnostic{
				Linter:   res.RuleID,
				Message:  res.Message.Text,
				Severity: output.Severity(res.Level),
			}
			if len(res.Locations) > 0 {
				loc := res.Locations[0].PhysicalLocation
				d.Pos = output.Position{
					Filename: loc.ArtifactLocation.URI,
					Line:     loc.Region.StartLine,
					Column:   loc.Region.StartColumn,
				}
			}
			run.diags = append(run.diags, d)
		}
	}
	return run, nil
}
