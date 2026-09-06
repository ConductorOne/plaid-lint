// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package analyzers

import (
	"testing"

	"golang.org/x/tools/go/analysis"
)

func TestRegisterSyntaxOnlyWithConfigUsesConfigSalt(t *testing.T) {
	analyzer := &analysis.Analyzer{Name: "configured-syntax-only"}
	cfg := map[string]any{"scope": "tenant"}
	RegisterSyntaxOnlyWithConfig(analyzer, cfg, 1)

	descriptor := BundledRegistry.Lookup(analyzer)
	if descriptor == nil {
		t.Fatal("configured analyzer descriptor was not registered")
	}
	if got, want := descriptor.ConfigSalt(nil), ConfigSalt(analyzer.Name, cfg); got != want {
		t.Errorf("ConfigSalt = %x, want %x", got, want)
	}
}
