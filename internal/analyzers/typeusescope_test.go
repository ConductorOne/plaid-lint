// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import "testing"

// TestTypeUseScope_DefaultIsFullTypeGraph pins the back-compat
// contract: a zero-valued AnalyzerDescriptor (or any descriptor that
// doesn't explicitly set TypeUseScope) opts into the prior
// behavior.
func TestTypeUseScope_DefaultIsFullTypeGraph(t *testing.T) {
	d := &AnalyzerDescriptor{}
	if d.TypeUseScope != TypeUseFullTypeGraph {
		t.Errorf("zero-valued TypeUseScope = %v, want TypeUseFullTypeGraph (0)", d.TypeUseScope)
	}
}

// TestTypeUseScope_String covers the debug stringer for every known
// value, plus the fallback for an unknown ordinal.
func TestTypeUseScope_String(t *testing.T) {
	cases := []struct {
		s    TypeUseScope
		want string
	}{
		{TypeUseFullTypeGraph, "FullTypeGraph"},
		{TypeUseSyntaxOnly, "SyntaxOnly"},
		{TypeUseExportedTypesOnly, "ExportedTypesOnly"},
		{TypeUseScope(99), "TypeUseScope(99)"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("TypeUseScope(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}
