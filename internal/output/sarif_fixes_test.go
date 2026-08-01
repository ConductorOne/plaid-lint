// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestSarifFixes pins the SARIF §3.55 fix rendering: replacements
// grouped per file, regions 1-indexed, insertedContent omitted for
// pure deletions, and no fixes array at all for fix-less results.
func TestSarifFixes(t *testing.T) {
	diags := []Diagnostic{
		{
			Linter:  "tracecheck",
			Message: "span name mismatch",
			Pos:     Position{Filename: "pkg/a/a.go", Line: 10, Column: 3},
			SuggestedFixes: []SuggestedFix{{
				Message: "rename span",
				TextEdits: []TextEdit{
					{
						Start:   Position{Filename: "pkg/a/a.go", Line: 10, Column: 5},
						End:     Position{Filename: "pkg/a/a.go", Line: 10, Column: 12},
						NewText: `"do_thing"`,
					},
					{
						// Pure deletion.
						Start: Position{Filename: "pkg/a/a.go", Line: 11, Column: 1},
						End:   Position{Filename: "pkg/a/a.go", Line: 12, Column: 1},
					},
				},
			}},
		},
		{
			Linter:  "errcheck",
			Message: "unchecked error",
			Pos:     Position{Filename: "pkg/a/a.go", Line: 20},
		},
	}

	var buf bytes.Buffer
	if err := NewSarif(&buf).Print(diags); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		Runs []struct {
			Results []struct {
				RuleID string `json:"ruleId"`
				Fixes  []struct {
					Description *struct {
						Text string `json:"text"`
					} `json:"description"`
					ArtifactChanges []struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Replacements []struct {
							DeletedRegion struct {
								StartLine   int `json:"startLine"`
								StartColumn int `json:"startColumn"`
								EndLine     int `json:"endLine"`
								EndColumn   int `json:"endColumn"`
							} `json:"deletedRegion"`
							InsertedContent *struct {
								Text string `json:"text"`
							} `json:"insertedContent"`
						} `json:"replacements"`
					} `json:"artifactChanges"`
				} `json:"fixes"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}

	results := doc.Runs[0].Results
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}

	withFix := results[0]
	if len(withFix.Fixes) != 1 {
		t.Fatalf("want 1 fix, got %d", len(withFix.Fixes))
	}
	fix := withFix.Fixes[0]
	if fix.Description == nil || fix.Description.Text != "rename span" {
		t.Errorf("fix description = %+v", fix.Description)
	}
	if len(fix.ArtifactChanges) != 1 {
		t.Fatalf("want 1 artifactChange (same file), got %d", len(fix.ArtifactChanges))
	}
	ac := fix.ArtifactChanges[0]
	if ac.ArtifactLocation.URI != "pkg/a/a.go" {
		t.Errorf("uri = %q", ac.ArtifactLocation.URI)
	}
	if len(ac.Replacements) != 2 {
		t.Fatalf("want 2 replacements, got %d", len(ac.Replacements))
	}
	r0 := ac.Replacements[0]
	if r0.DeletedRegion.StartLine != 10 || r0.DeletedRegion.StartColumn != 5 ||
		r0.DeletedRegion.EndLine != 10 || r0.DeletedRegion.EndColumn != 12 {
		t.Errorf("replacement 0 region = %+v", r0.DeletedRegion)
	}
	if r0.InsertedContent == nil || r0.InsertedContent.Text != `"do_thing"` {
		t.Errorf("replacement 0 content = %+v", r0.InsertedContent)
	}
	if r1 := ac.Replacements[1]; r1.InsertedContent != nil {
		t.Errorf("pure deletion carries insertedContent: %+v", r1.InsertedContent)
	}

	if fixesJSON := results[1].Fixes; len(fixesJSON) != 0 {
		t.Errorf("fix-less result has fixes: %+v", fixesJSON)
	}
	// The fix-less serialization must not even contain the key, so
	// pre-fix consumers see byte-identical output.
	if strings.Count(buf.String(), `"fixes"`) != 1 {
		t.Errorf("fixes key count = %d, want 1", strings.Count(buf.String(), `"fixes"`))
	}
}
