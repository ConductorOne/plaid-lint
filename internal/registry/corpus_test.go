// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/conductorone/plaid-lint/internal/config"
)

// corpusFiles mirrors the 16-config corpus T2.1 ships in
// `internal/config/testdata/corpus/`. Walking the directory at test
// time would be fine too, but the fixed list lets the test catch a
// renumbered fixture file.
var corpusFiles = []string{
	"01-prometheus.yml",
	"02-kubernetes-release.yml",
	"03-terraform-provider-google.yml",
	"04-uber-go-guide.yml",
	"05-crossplane.yml",
	"06-kwok.yaml",
	"07-kong-ktf.yaml",
	"08-kong-kic.yaml",
	"09-milvus.yml",
	"10-docker-cli.yml",
	"11-docker-buildx.yml",
	"12-docker-go-imageinspect.yml",
	"13-golang-migrate.yml",
	"14-pulumi-hcloud.yml",
	"15-thanos.yml",
	"16-golangci-self.yml",
}

// corpusTestdataDir is the path to T2.1's fixture set, relative to
// `internal/registry/`'s test working directory.
const corpusTestdataDir = "../config/testdata/corpus"

// TestCorpus_AllResolve_NoErrors loads every corpus config via
// config.Load, runs registry.Validate, then registry.Build, and
// asserts:
//
//   - config.Load succeeds (T2.1 already covers this, but we exercise
//     the integration too).
//   - registry.Validate returns no errors (every linter name in the
//     corpus is known to the catalog).
//   - registry.Build returns no error and the Enabled() set is
//     non-empty.
//
// Together these prove the 16/16 corpus pass-rate the dispatch
// brief requires.
func TestCorpus_AllResolve_NoErrors(t *testing.T) {
	var pass, fail, emptyEnable int
	for _, fname := range corpusFiles {
		fname := fname
		t.Run(fname, func(t *testing.T) {
			path := filepath.Join(corpusTestdataDir, fname)
			cfg, _, err := config.Load(path)
			if err != nil {
				fail++
				t.Fatalf("config.Load: %v", err)
			}
			if errs := config.Validate(cfg); len(errs) > 0 {
				fail++
				t.Fatalf("config.Validate: %v", errors.Join(errs...))
			}

			// Registry-aware validation.
			if errs := Validate(cfg); len(errs) > 0 {
				fail++
				t.Errorf("registry.Validate: %v", errors.Join(errs...))
				return
			}

			reg, _, err := Build(cfg)
			if err != nil {
				fail++
				t.Fatalf("registry.Build: %v", err)
			}
			if len(reg.Enabled()) == 0 {
				emptyEnable++
				t.Errorf("registry.Build produced 0 enabled linters")
			}
			pass++
		})
	}
	t.Logf("Corpus resolution: pass=%d fail=%d empty-enable=%d (of %d)",
		pass, fail, emptyEnable, len(corpusFiles))
}

// TestCorpus_RealWorldEnableSets validates that representative
// real-world configs pull in expected linters.
func TestCorpus_RealWorldEnableSets(t *testing.T) {
	cases := []struct {
		file     string
		expected []string // linters that must be in Enabled
	}{
		{
			file:     "01-prometheus.yml",
			expected: []string{"errcheck", "govet", "staticcheck"},
		},
		{
			file:     "16-golangci-self.yml",
			expected: []string{"errcheck", "govet", "ineffassign", "staticcheck"},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.file, func(t *testing.T) {
			path := filepath.Join(corpusTestdataDir, c.file)
			cfg, _, err := config.Load(path)
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			reg, _, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := map[string]bool{}
			for _, r := range reg.Enabled() {
				got[r.Name] = true
			}
			for _, want := range c.expected {
				if !got[want] {
					t.Errorf("%s: missing %q in Enabled", c.file, want)
				}
			}
		})
	}
}
