// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"testing"

	"golang.org/x/tools/go/analysis"
)

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	a := &analysis.Analyzer{Name: "tester"}
	d := &AnalyzerDescriptor{
		Analyzer:   a,
		ConfigSalt: func(any) [32]byte { return [32]byte{0xaa} },
	}
	r.Register(d)
	got := r.Lookup(a)
	if got != d {
		t.Errorf("Lookup returned %v, want %v", got, d)
	}
	// AnalyzerVersion auto-populated by Register.
	if got.AnalyzerVersion == "" {
		t.Errorf("AnalyzerVersion empty after Register; want auto-populated")
	}
}

func TestRegistryLookupMiss(t *testing.T) {
	r := NewRegistry()
	a := &analysis.Analyzer{Name: "unknown"}
	if got := r.Lookup(a); got != nil {
		t.Errorf("Lookup(unknown) = %v, want nil", got)
	}
}

func TestRegistryRegisterPanicsOnNil(t *testing.T) {
	r := NewRegistry()
	t.Run("nil descriptor", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Register(nil) did not panic")
			}
		}()
		r.Register(nil)
	})
	t.Run("nil Analyzer", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Register({Analyzer:nil}) did not panic")
			}
		}()
		r.Register(&AnalyzerDescriptor{
			ConfigSalt: func(any) [32]byte { return [32]byte{} },
		})
	})
	t.Run("nil ConfigSalt", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("Register({ConfigSalt:nil}) did not panic")
			}
		}()
		r.Register(&AnalyzerDescriptor{
			Analyzer: &analysis.Analyzer{Name: "x"},
		})
	})
}

func TestBundledRegistryHasW7Analyzers(t *testing.T) {
	// All eight W7 root analyzers (5 W6 + errcheck + ineffassign +
	// SA1000) must have descriptors in the BundledRegistry.
	wantNames := []string{
		"assign", "nilfunc", "nilness", "printf", "unusedresult",
		"errcheck", "ineffassign", "SA1000",
	}
	got := make(map[string]bool)
	for _, d := range BundledRegistry.All() {
		got[d.Name()] = true
	}
	for _, name := range wantNames {
		if !got[name] {
			t.Errorf("BundledRegistry missing descriptor for %q", name)
		}
	}
}

func TestBundledRegistryDescriptorFields(t *testing.T) {
	// Every bundled descriptor must have a non-nil ConfigSalt and a
	// non-empty AnalyzerVersion (filled in by Register).
	for _, d := range BundledRegistry.All() {
		if d.ConfigSalt == nil {
			t.Errorf("descriptor %q has nil ConfigSalt", d.Name())
		}
		if d.AnalyzerVersion == "" {
			t.Errorf("descriptor %q has empty AnalyzerVersion", d.Name())
		}
	}
}

func TestAllBundledAnalyzersExcludesPrereqs(t *testing.T) {
	// The user-facing analyzer set must NOT include inspect, ctrlflow,
	// buildssa — they're pulled in via Requires resolution by
	// Snapshot.Analyze.
	prereqs := map[string]bool{"inspect": true, "ctrlflow": true, "buildssa": true}
	for _, a := range AllBundledAnalyzers() {
		if prereqs[a.Name] {
			t.Errorf("AllBundledAnalyzers includes prereq-only analyzer %q", a.Name)
		}
	}
}

func TestProcessBinaryVersionStable(t *testing.T) {
	// Two calls return the same value within a single process.
	a := ProcessBinaryVersion()
	b := ProcessBinaryVersion()
	if a != b {
		t.Errorf("ProcessBinaryVersion not stable: %q vs %q", a, b)
	}
	if a == "" {
		t.Errorf("ProcessBinaryVersion is empty")
	}
}
