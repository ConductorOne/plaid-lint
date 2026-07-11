// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/conductorone/plaid-lint/internal/config"
)

func parseRunFlags(t *testing.T, args []string) *runFlags {
	t.Helper()
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rf := bindRunFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	rf.recordSetFlags(fs)
	rf.Args = fs.Args()
	return rf
}

func TestCSVSlice_AppendsAndSplits(t *testing.T) {
	rf := parseRunFlags(t, []string{
		"--enable=a,b",
		"--enable=c",
		"-E", "d,e",
	})
	want := []string{"a", "b", "c", "d", "e"}
	if !reflect.DeepEqual([]string(rf.Enable), want) {
		t.Fatalf("enable = %v, want %v", rf.Enable, want)
	}
}

func TestCSVSlice_TrimsAndDropsEmpty(t *testing.T) {
	rf := parseRunFlags(t, []string{"--build-tags= a , , b "})
	want := []string{"a", "b"}
	if !reflect.DeepEqual([]string(rf.BuildTags), want) {
		t.Fatalf("build-tags = %v, want %v", rf.BuildTags, want)
	}
}

func TestBoolPtr_UnsetVsExplicit(t *testing.T) {
	// Unset.
	rf := parseRunFlags(t, nil)
	if rf.Tests.Value() != nil {
		t.Fatalf("unset --tests should be nil, got %v", *rf.Tests.Value())
	}
	if rf.ShowStats.Value() != nil {
		t.Fatalf("unset --show-stats should be nil")
	}
	// Explicit false.
	rf = parseRunFlags(t, []string{"--tests=false"})
	v := rf.Tests.Value()
	if v == nil || *v != false {
		t.Fatalf("--tests=false: got %v", v)
	}
	// Explicit true via bare flag.
	rf = parseRunFlags(t, []string{"--tests"})
	v = rf.Tests.Value()
	if v == nil || *v != true {
		t.Fatalf("--tests: got %v", v)
	}
}

func TestIntPtr_UnsetVsExplicitZero(t *testing.T) {
	rf := parseRunFlags(t, nil)
	if rf.IssuesExitCode.Value() != nil {
		t.Fatalf("unset --issues-exit-code should be nil")
	}
	rf = parseRunFlags(t, []string{"--issues-exit-code=0"})
	v := rf.IssuesExitCode.Value()
	if v == nil || *v != 0 {
		t.Fatalf("--issues-exit-code=0: got %v", v)
	}
	rf = parseRunFlags(t, []string{"--issues-exit-code=7"})
	v = rf.IssuesExitCode.Value()
	if v == nil || *v != 7 {
		t.Fatalf("--issues-exit-code=7: got %v", v)
	}
}

func TestApplyOverlay_PreservesFileScalarsWhenUnset(t *testing.T) {
	base := config.NewDefault()
	base.Run.Timeout = config.Duration(5 * time.Minute)
	base.Run.ExitCodeIfIssuesFound = 99
	base.Issues.MaxIssuesPerLinter = 17

	rf := parseRunFlags(t, []string{"--enable=foo"})
	merged := rf.applyOverlay(base)

	if got, want := merged.Run.Timeout.AsDuration(), 5*time.Minute; got != want {
		t.Errorf("Timeout = %v, want %v (file value should survive)", got, want)
	}
	if got, want := merged.Run.ExitCodeIfIssuesFound, 99; got != want {
		t.Errorf("ExitCodeIfIssuesFound = %d, want %d", got, want)
	}
	if got, want := merged.Issues.MaxIssuesPerLinter, 17; got != want {
		t.Errorf("MaxIssuesPerLinter = %d, want %d", got, want)
	}
	if got, want := merged.Linters.Enable, []string{"foo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Linters.Enable = %v, want %v", got, want)
	}
}

func TestApplyOverlay_CLIScalarOverridesFile(t *testing.T) {
	base := config.NewDefault()
	base.Run.Timeout = config.Duration(5 * time.Minute)
	base.Issues.MaxIssuesPerLinter = 17

	rf := parseRunFlags(t, []string{
		"--timeout=30s",
		"--max-issues-per-linter=2",
	})
	merged := rf.applyOverlay(base)
	if got, want := merged.Run.Timeout.AsDuration(), 30*time.Second; got != want {
		t.Errorf("Timeout = %v, want %v", got, want)
	}
	if got, want := merged.Issues.MaxIssuesPerLinter, 2; got != want {
		t.Errorf("MaxIssuesPerLinter = %d, want %d", got, want)
	}
}

func TestApplyOverlay_EnableAppendsToFile(t *testing.T) {
	base := config.NewDefault()
	base.Linters.Enable = []string{"a", "b"}

	rf := parseRunFlags(t, []string{"--enable=c,d"})
	merged := rf.applyOverlay(base)
	want := []string{"a", "b", "c", "d"}
	if !reflect.DeepEqual(merged.Linters.Enable, want) {
		t.Errorf("Linters.Enable = %v, want %v", merged.Linters.Enable, want)
	}
}

func TestApplyOverlay_EnableOnlyReplaces(t *testing.T) {
	base := config.NewDefault()
	base.Linters.Default = "standard"
	base.Linters.Enable = []string{"a", "b"}

	rf := parseRunFlags(t, []string{"--enable-only=x,y"})
	merged := rf.applyOverlay(base)
	if got, want := merged.Linters.Default, "none"; got != want {
		t.Errorf("Linters.Default = %q, want %q (--enable-only should force none)", got, want)
	}
	// --enable-only is the exclusive form: it forces default=none and
	// must REPLACE the base/file-config enable list so only the named
	// linters run. The base [a, b] must not leak through.
	if got, want := merged.Linters.Enable, []string{"x", "y"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Linters.Enable = %v, want %v", got, want)
	}
}

func TestParseRunFlags_EnableOnlyAnalyzer(t *testing.T) {
	rf := parseRunFlags(t, []string{
		"--enable-only-analyzer=SA1019,S1000",
		"--enable-only-analyzer=printf",
	})
	want := csvSlice{"SA1019", "S1000", "printf"}
	if !reflect.DeepEqual(rf.Analyzers, want) {
		t.Fatalf("Analyzers = %v, want %v", rf.Analyzers, want)
	}
}

func TestApplyOverlay_NoConfigStillTracksOverlay(t *testing.T) {
	// Even with no file config the overlay path runs.
	rf := parseRunFlags(t, []string{"--default=none", "--enable=govet"})
	merged := rf.applyOverlay(config.NewDefault())
	if got, want := merged.Linters.Default, "none"; got != want {
		t.Errorf("Linters.Default = %q, want %q", got, want)
	}
	if got, want := merged.Linters.Enable, []string{"govet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Linters.Enable = %v, want %v", got, want)
	}
}

func TestParseInt(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"0", 0, false},
		{"-5", -5, false},
		{"+12", 12, false},
		{"", 0, true},
		{"abc", 0, true},
		{"1a", 0, true},
	}
	for _, c := range cases {
		got, err := parseInt(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseInt(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseInt(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
