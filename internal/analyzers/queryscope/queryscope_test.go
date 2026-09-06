// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package queryscope_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/conductorone/plaid-lint/internal/analyzers/queryscope"
)

func TestQueryScope(t *testing.T) {
	analysistest.Run(t, dataDir(t), queryscope.New(queryscope.Config{
		UnscopedMethods:           []string{"CountStar"},
		ScopedMethods:             []string{"SelectCol", "ScopeJoinedTable", "RawWith", "WithRawCTE"},
		ScopedTwoArgStringMethods: []string{"Select", "Count"},
		ScopePredicateMethods:     []string{"Where"},
		ScopeFieldNames:           []string{"TenantId"},
		ScopeStringSubstrings:     []string{"tenant_id"},
		OptOutMethodContains:      []string{"IgnoreTenantCheck"},
		OptOutMethods:             []string{"WithDangerousCrossTenant"},
		QueryParameterTypeNames:   []string{"Select", "SelectDataset"},
		ScopedMethodMinArgs:       map[string]int{"With": 3},
		IgnoreDirective:           "queryscope:ignore",
	}), "queryscope")
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  queryscope.Config
		want bool
	}{
		{name: "missing unscoped methods", cfg: queryscope.Config{IgnoreDirective: "queryscope:ignore"}, want: true},
		{name: "empty ignore directive", cfg: queryscope.Config{UnscopedMethods: []string{"CountStar"}}, want: true},
		{name: "negative argument count", cfg: queryscope.Config{UnscopedMethods: []string{"CountStar"}, IgnoreDirective: "queryscope:ignore", ScopedMethodMinArgs: map[string]int{"With": -1}}, want: true},
		{name: "valid", cfg: queryscope.Config{UnscopedMethods: []string{"CountStar"}, IgnoreDirective: "queryscope:ignore"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.cfg.Validate(); (err != nil) != test.want {
				t.Fatalf("Validate() error = %v, want error = %t", err, test.want)
			}
		})
	}
}

func dataDir(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(filename), "testdata")
}
