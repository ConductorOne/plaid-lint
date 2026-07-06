// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// fixtureIn is a small replica of c1's .golangci.yml shape covering
// every transform: custom-plugin block under linters.settings, the
// tracecheck entry in linters.enable and linters.disable, and the
// per-rule linters list under linters.exclusions.rules.
const fixtureIn = `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - tracecheck
    - revive
  disable:
    - tracecheck
    - lll
  settings:
    revive:
      severity: error
    custom:
      tracecheck:
        path: /linters/tracecheck.so
        description: Checks tracing spans for bad names
  exclusions:
    generated: lax
    rules:
      - linters:
          - forbidigo
          - tracecheck
          - revive
        path: pkg/foo/
      - linters:
          - tracecheck
        text: blank
`

// fixtureExpected is what the python heredoc produces for fixtureIn
// (structurally — yaml.v3's encoder preserves more layout detail, so
// we compare by parse-and-DeepEqual rather than byte equality).
const fixtureExpected = `version: "2"
linters:
  default: none
  enable:
    - errcheck
    - revive
  disable:
    - lll
  settings:
    revive:
      severity: error
  exclusions:
    generated: lax
    rules:
      - linters:
          - forbidigo
          - revive
        path: pkg/foo/
      - linters: []
        text: blank
`

func TestStripRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.yml")
	dst := filepath.Join(dir, "out.yml")
	if err := os.WriteFile(src, []byte(fixtureIn), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var gotV, wantV any
	if err := yaml.Unmarshal(got, &gotV); err != nil {
		t.Fatalf("parse got: %v", err)
	}
	if err := yaml.Unmarshal([]byte(fixtureExpected), &wantV); err != nil {
		t.Fatalf("parse want: %v", err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("stripped output does not match expected\n--- got ---\n%s\n--- want ---\n%s",
			string(got), fixtureExpected)
	}
}

func TestStripIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.yml")
	pass1 := filepath.Join(dir, "pass1.yml")
	pass2 := filepath.Join(dir, "pass2.yml")
	if err := os.WriteFile(src, []byte(fixtureIn), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(src, pass1); err != nil {
		t.Fatalf("pass1: %v", err)
	}
	if err := run(pass1, pass2); err != nil {
		t.Fatalf("pass2: %v", err)
	}
	a, _ := os.ReadFile(pass1)
	b, _ := os.ReadFile(pass2)
	if string(a) != string(b) {
		t.Errorf("non-idempotent: pass1 != pass2\n--- pass1 ---\n%s\n--- pass2 ---\n%s",
			string(a), string(b))
	}
}

func TestStripPreservesNonTargetLinters(t *testing.T) {
	in := `linters:
  enable:
    - errcheck
    - revive
  disable: []
  settings:
    revive:
      severity: error
`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.yml")
	dst := filepath.Join(dir, "out.yml")
	if err := os.WriteFile(src, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := os.ReadFile(dst)
	var gotV, wantV any
	if err := yaml.Unmarshal(got, &gotV); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte(in), &wantV); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("non-target config was mutated\n--- got ---\n%s\n--- want ---\n%s",
			string(got), in)
	}
}

func TestStripHandlesMissingSections(t *testing.T) {
	// A config that has no settings, no exclusions, no disable — only
	// enable. Verifies the strip walks defensively without panicking.
	in := `version: "2"
linters:
  enable:
    - tracecheck
    - errcheck
`
	dir := t.TempDir()
	src := filepath.Join(dir, "in.yml")
	dst := filepath.Join(dir, "out.yml")
	if err := os.WriteFile(src, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(src, dst); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, _ := os.ReadFile(dst)
	var gotV any
	if err := yaml.Unmarshal(got, &gotV); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"version": "2",
		"linters": map[string]any{
			"enable": []any{"errcheck"},
		},
	}
	if !reflect.DeepEqual(gotV, want) {
		t.Errorf("got %#v want %#v", gotV, want)
	}
}

// TestStripAgainstPythonReference compares the Go tool's output
// against the python heredoc's output for c1's actual .golangci.yml.
// The two must be structurally equivalent (parse to the same value).
// Skipped when c1's config is not on disk (CI without the c1 worktree
// mounted).
func TestStripAgainstPythonReference(t *testing.T) {
	const c1Config = "/data/squire/src/c1/.golangci.yml"
	if _, err := os.Stat(c1Config); err != nil {
		t.Skipf("c1 config not available at %s: %v", c1Config, err)
	}
	const pyRef = "/tmp/compare-golangci-python-reference.yml"
	if _, err := os.Stat(pyRef); err != nil {
		t.Skipf("python reference not generated at %s; regenerate via scripts/compare-against-c1.sh once", pyRef)
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "go.yml")
	if err := run(c1Config, dst); err != nil {
		t.Fatalf("run: %v", err)
	}
	gotB, _ := os.ReadFile(dst)
	wantB, _ := os.ReadFile(pyRef)
	var gotV, wantV any
	if err := yaml.Unmarshal(gotB, &gotV); err != nil {
		t.Fatalf("parse go output: %v", err)
	}
	if err := yaml.Unmarshal(wantB, &wantV); err != nil {
		t.Fatalf("parse python ref: %v", err)
	}
	if !reflect.DeepEqual(gotV, wantV) {
		t.Errorf("Go output and python reference are not structurally equivalent")
	}
}
