// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package output

import (
	"testing"

	"github.com/conductorone/plaid-lint/internal/gopls/cache"
	"github.com/conductorone/plaid-lint/internal/gopls/protocol"
)

func TestFromAnalysis_ConvertsBasicDiagnostic(t *testing.T) {
	d := &cache.Diagnostic{
		URI:      protocol.URIFromPath("/tmp/foo.go"),
		Range:    protocol.Range{Start: protocol.Position{Line: 9, Character: 4}, End: protocol.Position{Line: 9, Character: 8}},
		Severity: protocol.SeverityWarning,
		Source:   "ineffassign",
		Message:  "ineffectual assignment",
	}
	got := FromAnalysis(d)
	if got.Linter != "ineffassign" {
		t.Errorf("Linter=%q want %q", got.Linter, "ineffassign")
	}
	if got.Message != "ineffectual assignment" {
		t.Errorf("Message=%q", got.Message)
	}
	if got.Severity != SeverityWarning {
		t.Errorf("Severity=%v want %v", got.Severity, SeverityWarning)
	}
	if got.Pos.Filename != "/tmp/foo.go" {
		t.Errorf("Filename=%q", got.Pos.Filename)
	}
	// LSP is zero-based; output.Diagnostic is one-based.
	if got.Pos.Line != 10 {
		t.Errorf("Line=%d want 10 (1-based)", got.Pos.Line)
	}
	if got.Pos.Column != 5 {
		t.Errorf("Column=%d want 5 (1-based)", got.Pos.Column)
	}
}

func TestFromAnalysis_DefaultsSeverityToError(t *testing.T) {
	d := &cache.Diagnostic{
		URI:    protocol.URIFromPath("/tmp/foo.go"),
		Source: "errcheck",
	}
	got := FromAnalysis(d)
	if got.Severity != SeverityError {
		t.Errorf("Severity=%v want SeverityError (no severity → default error)", got.Severity)
	}
}

func TestFromAnalysis_ConvertsRelated(t *testing.T) {
	d := &cache.Diagnostic{
		URI:     protocol.URIFromPath("/tmp/foo.go"),
		Source:  "govet",
		Message: "redeclared",
		Related: []protocol.DiagnosticRelatedInformation{
			{
				Location: protocol.Location{
					URI:   protocol.URIFromPath("/tmp/other.go"),
					Range: protocol.Range{Start: protocol.Position{Line: 1, Character: 2}},
				},
				Message: "previously declared here",
			},
		},
	}
	got := FromAnalysis(d)
	if len(got.Related) != 1 {
		t.Fatalf("Related=%d want 1", len(got.Related))
	}
	r := got.Related[0]
	if r.Message != "previously declared here" {
		t.Errorf("related message=%q", r.Message)
	}
	if r.Position.Filename != "/tmp/other.go" {
		t.Errorf("related filename=%q", r.Position.Filename)
	}
	if r.Position.Line != 2 || r.Position.Column != 3 {
		t.Errorf("related pos=%d:%d want 2:3", r.Position.Line, r.Position.Column)
	}
}

func TestFromAnalysis_NilSafe(t *testing.T) {
	got := FromAnalysis(nil)
	if got.Linter != "" || got.Message != "" {
		t.Errorf("FromAnalysis(nil) should return zero Diagnostic, got %+v", got)
	}
}
