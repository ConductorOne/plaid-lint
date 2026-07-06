// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// corpusEntry — one row from the 16-config table. The actual YAML in
// testdata/corpus is hand-derived from schema notes (the test
// infrastructure shouldn't depend on network fetches).
type corpusEntry struct {
	file     string
	schema   string // "v1" or "v2"
	wantWarn bool   // legacy keys present
	notes    string
}

var corpus = []corpusEntry{
	{file: "01-prometheus.yml", schema: "v2", notes: "23 linters, depguard atomic/ioutil, perfsprint"},
	{file: "02-kubernetes-release.yml", schema: "v2", notes: "80+ linters, gocyclo=40, gci k8s sections"},
	{file: "03-terraform-provider-google.yml", schema: "v1", wantWarn: true, notes: "minimal, depguard SDK patterns"},
	{file: "04-uber-go-guide.yml", schema: "v1", wantWarn: true, notes: "reference config"},
	{file: "05-crossplane.yml", schema: "v2", notes: "30+ linters, testify/ginkgo depguard"},
	{file: "06-kwok.yaml", schema: "v2", notes: "gocyclo=50, importas k8s aliases"},
	{file: "07-kong-ktf.yaml", schema: "v2", notes: "forbidigo deprecated, gomodguard ghodss/yaml"},
	{file: "08-kong-kic.yaml", schema: "v2", notes: "30 linters, forbidigo Gateway API"},
	{file: "09-milvus.yml", schema: "v2", notes: "depguard cockroachdb/errors + gogo/protobuf"},
	{file: "10-docker-cli.yml", schema: "v2", notes: "45+ linters, line length 200"},
	{file: "11-docker-buildx.yml", schema: "v2", notes: "vendor mode, gosec exclusions"},
	{file: "12-docker-go-imageinspect.yml", schema: "v2", notes: "12 linters"},
	{file: "13-golang-migrate.yml", schema: "v1", wantWarn: true, notes: "7 linters, gofmt only"},
	{file: "14-pulumi-hcloud.yml", schema: "v1", wantWarn: true, notes: "revive var-naming skip"},
	{file: "15-thanos.yml", schema: "v2", notes: "5 linters, Cortex/loggers errcheck"},
	{file: "16-golangci-self.yml", schema: "v2", notes: "self-dogfood, 37 linters"},
}

func TestCorpus_AllParse(t *testing.T) {
	var (
		parsed   int
		warnings int
		failed   int
		v1Count  int
	)

	for _, ent := range corpus {
		ent := ent
		t.Run(ent.file, func(t *testing.T) {
			path := filepath.Join("testdata", "corpus", ent.file)
			cfg, warns, err := Load(path)
			if err != nil {
				failed++
				t.Fatalf("Load %s: %v", ent.file, err)
			}
			parsed++
			if len(warns) > 0 {
				warnings++
			}

			// Schema-driven assertions.
			switch ent.schema {
			case "v2":
				if cfg.Version != "2" {
					t.Errorf("expected v2 version, got %q", cfg.Version)
				}
				if ent.wantWarn && len(warns) == 0 {
					t.Errorf("expected at least one warning, got none")
				}
				if !ent.wantWarn && len(warns) > 0 {
					t.Errorf("v2 file produced %d warnings: %v", len(warns), warns)
				}
			case "v1":
				v1Count++
				if !ent.wantWarn {
					t.Fatalf("internal corpus bookkeeping bug: v1 entry should always have wantWarn=true")
				}
				if len(warns) == 0 {
					t.Errorf("expected legacy-key warnings on v1 file, got none")
				}
				// After migration, exclusion rules / linters.settings should
				// have data when the v1 file declared them.
			}

			// Universal: Generated defaults to strict after finalize unless
			// the file set it explicitly to lax/disable.
			if cfg.Linters.Exclusions.Generated == "" {
				t.Errorf("Linters.Exclusions.Generated empty after finalize")
			}

			// Validate the parsed config. The corpus is hand-built so all
			// entries are valid; surface anything that isn't as a test
			// failure with the field path.
			if errs := Validate(cfg); len(errs) > 0 {
				t.Errorf("Validate: %v", errors.Join(errs...))
			}
		})
	}

	t.Logf("Corpus summary: parsed=%d/%d failed=%d warnings=%d v1=%d",
		parsed, len(corpus), failed, warnings, v1Count)
}

func TestCorpus_V1MigrationCoverage(t *testing.T) {
	// Spot-check that v1 files actually produce the migrations we
	// expect (not just any warnings — the right ones).
	cases := map[string][]string{
		"03-terraform-provider-google.yml": {"linters.disable-all", "run.skip-files", "linters-settings"},
		"04-uber-go-guide.yml":             {"linters.disable-all"},
		"13-golang-migrate.yml":            {"linters.disable-all", "linters-settings", "issues.exclude-generated"},
		"14-pulumi-hcloud.yml":             {"linters.disable-all", "linters-settings"},
	}

	for file, wantFields := range cases {
		t.Run(file, func(t *testing.T) {
			path := filepath.Join("testdata", "corpus", file)
			_, warns, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			haveFields := map[string]bool{}
			for _, w := range warns {
				haveFields[w.Field] = true
			}
			for _, f := range wantFields {
				if !haveFields[f] {
					t.Errorf("missing migration warning for %q (got %s)",
						f, strings.Join(warningFields(warns), ", "))
				}
			}
		})
	}
}

func warningFields(warns []Warning) []string {
	out := make([]string, 0, len(warns))
	for _, w := range warns {
		out = append(out, w.Field)
	}
	return out
}
