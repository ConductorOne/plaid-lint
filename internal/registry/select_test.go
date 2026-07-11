// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

func TestSelectAnalyzers(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = config.GroupNone
	cfg.Linters.Enable = []string{"staticcheck"}
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	selected, err := reg.SelectAnalyzers([]string{"SA1019", "S1000"})
	if err != nil {
		t.Fatalf("SelectAnalyzers: %v", err)
	}
	var got []string
	for _, rr := range selected.Enabled() {
		got = append(got, rr.Analyzer.Name)
	}
	if want := []string{"S1000", "SA1019"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled analyzers = %v, want %v", got, want)
	}

	if got, want := len(reg.Enabled()), len(staticcheckAnalyzers(nil)); got != want {
		t.Fatalf("original registry changed: enabled = %d, want %d", got, want)
	}
}

func TestSelectAnalyzersRejectsUnavailableName(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Linters.Default = config.GroupNone
	cfg.Linters.Enable = []string{"staticcheck"}
	reg, _, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	_, err = reg.SelectAnalyzers([]string{"SA1019", "not-an-analyzer"})
	if err == nil || !strings.Contains(err.Error(), "not-an-analyzer") {
		t.Fatalf("SelectAnalyzers error = %v, want unavailable analyzer name", err)
	}
}
