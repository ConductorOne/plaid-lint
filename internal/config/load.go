// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// supportedExtensions lists the file extensions [LoadDirs] looks for,
// in upstream's discovery order: YAML first, then TOML (unsupported,
// see below), then JSON.
var supportedExtensions = []string{".yml", ".yaml", ".toml", ".json"}

// supportedBasename is the stem upstream/plaid-lint look for.
const supportedBasename = ".golangci"

// Load parses a config file at path. Path may be any of
// `.golangci.{yml,yaml,toml,json}`; the format is inferred from the
// extension. An empty extension is treated as YAML, matching upstream.
//
// The returned [Config] is canonical v2 form: any v1-only keys in the
// source have been migrated and surfaced as [Warning]s. The caller
// chooses whether to print warnings, fail on them, etc.
//
// Load does NOT call [Config.Validate]; the caller drives validation
// so it can opt out (e.g. for tooling that wants to inspect a
// partially-invalid config).
//
// TOML is not yet supported and returns an explicit error. JSON and
// YAML are both first-class.
func Load(path string) (*Config, []Warning, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config: resolve %q: %w", path, err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, fmt.Errorf("config: read %q: %w", abs, err)
	}
	cfg, warnings, err := Decode(body, filepath.Ext(abs))
	if err != nil {
		return nil, nil, fmt.Errorf("config: parse %q: %w", abs, err)
	}
	cfg.sourcePath = abs
	cfg.cfgDir = filepath.Dir(abs)
	return cfg, warnings, nil
}

// Decode parses an in-memory config body. ext is the source file's
// extension (with the leading `.`, e.g. ".yml"); pass "" for YAML by
// convention.
//
// Use this when the config is supplied via stdin or stitched together
// from multiple sources. For on-disk files, prefer [Load] which also
// populates the cfgDir / sourcePath fields.
func Decode(body []byte, ext string) (*Config, []Warning, error) {
	ext = strings.ToLower(ext)
	switch ext {
	case "", ".yml", ".yaml":
		return decodeYAML(body)
	case ".json":
		return decodeJSON(body)
	case ".toml":
		return nil, nil, errors.New("TOML configs are not yet supported; see internal/config/SCHEMA.md")
	default:
		return nil, nil, fmt.Errorf("unsupported config extension %q", ext)
	}
}

// decodeYAML decodes a YAML config body. It double-decodes: once into
// the canonical v2 [Config], once into the [legacyConfig] shim. The
// shim's `hasLegacyKeys` gate decides whether to run the migration
// pass.
func decodeYAML(body []byte) (*Config, []Warning, error) {
	if len(body) == 0 {
		return NewDefault(), nil, nil
	}

	// Decode v2 canonical form. Unknown keys are silently dropped
	// because yaml.v3 doesn't enforce KnownFields by default.
	cfg := &Config{}
	if err := yaml.Unmarshal(body, cfg); err != nil {
		return nil, nil, fmt.Errorf("yaml decode: %w", err)
	}

	// Decode v1 legacy shim for migration.
	legacy := &legacyConfig{}
	if err := yaml.Unmarshal(body, legacy); err != nil {
		// Legacy decode shouldn't fail when v2 succeeded — but if it
		// does, treat it as non-fatal and skip migration.
		return finalize(cfg, nil), nil, nil
	}

	// Also decode into a generic map for unknown-key detection in
	// the future (not yet wired; reserved for warn-on-unknown-keys).
	var raw map[string]any
	_ = yaml.Unmarshal(body, &raw)

	var warnings []Warning
	if legacy.hasLegacyKeys() {
		warnings = migrateLegacy(cfg, legacy, raw)
	}
	return finalize(cfg, warnings), warnings, nil
}

// decodeJSON decodes a JSON config body. JSON configs are uncommon but
// upstream supports them; we mirror by reading through the same Config
// struct (JSON tags mirror the YAML keys).
//
// Legacy migration runs against JSON too — same shim, same warnings.
func decodeJSON(body []byte) (*Config, []Warning, error) {
	if len(body) == 0 {
		return NewDefault(), nil, nil
	}
	cfg := &Config{}
	if err := json.Unmarshal(body, cfg); err != nil {
		return nil, nil, fmt.Errorf("json decode: %w", err)
	}

	// Re-encode to YAML and re-decode the legacy shim from there.
	// JSON keys match YAML's, so the round-trip is byte-stable for
	// the keys we care about.
	asYAML, err := yaml.Marshal(rawJSONToYAML(body))
	if err != nil {
		return finalize(cfg, nil), nil, nil
	}
	legacy := &legacyConfig{}
	if err := yaml.Unmarshal(asYAML, legacy); err != nil {
		return finalize(cfg, nil), nil, nil
	}
	var raw map[string]any
	_ = yaml.Unmarshal(asYAML, &raw)

	var warnings []Warning
	if legacy.hasLegacyKeys() {
		warnings = migrateLegacy(cfg, legacy, raw)
	}
	return finalize(cfg, warnings), warnings, nil
}

// rawJSONToYAML decodes raw JSON into a generic Go value so it can be
// re-emitted as YAML for the legacy shim's second pass. Returns nil on
// decode failure (the caller skips migration in that case).
func rawJSONToYAML(body []byte) any {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	return v
}

// finalize applies post-decode defaults that upstream's Loader.Load
// sets (and that aren't expressible as struct zero-value defaults):
//   - linters.exclusions.generated defaults to "strict"
//   - per-linter settings defaults (see [applyLinterSettingsDefaults])
//     fill in upstream's compiled-in non-zero defaults for any field
//     the YAML doesn't override.
func finalize(cfg *Config, _ []Warning) *Config {
	if cfg.Linters.Exclusions.Generated == "" {
		cfg.Linters.Exclusions.Generated = GeneratedModeStrict
	}
	applyLinterSettingsDefaults(&cfg.Linters.Settings)
	return cfg
}

// LoadDirs walks the supplied directories looking for `.golangci.*`
// files. The first match wins, mirroring upstream's viper-based search.
//
// Search order per upstream's `BaseLoader.getConfigSearchPaths`:
//
//   1. The current working directory (passed as the first dir entry).
//   2. Each ancestor up to filesystem root.
//   3. The user's $HOME (passed last; for global configs).
//
// Within a single directory, the file extension preference is YAML →
// TOML → JSON. TOML is parsed-failed today (see [Decode]) so an
// existing `.golangci.toml` short-circuits to an error rather than
// being silently skipped — fail-fast matches LEARN-FGL-006.
//
// Returns (cfg, warnings, "") when no file is found; the caller can
// then build a default config from CLI flags.
func LoadDirs(dirs []string) (*Config, []Warning, string, error) {
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		for _, ext := range supportedExtensions {
			path := filepath.Join(dir, supportedBasename+ext)
			if _, err := os.Stat(path); err == nil {
				cfg, warnings, err := Load(path)
				return cfg, warnings, path, err
			} else if !os.IsNotExist(err) {
				return nil, nil, path, fmt.Errorf("config: stat %q: %w", path, err)
			}
		}
	}
	return NewDefault(), nil, "", nil
}

// DiscoverDirs returns the directory list [LoadDirs] should walk when
// looking for a config file from startDir. The list starts at
// startDir, walks up to filesystem root, then appends the user's home
// directory at the end (for a global config fallback).
//
// Mirrors upstream's `BaseLoader.getConfigSearchPaths` excluding the
// leading "./" entry — callers should already be in absolute form.
func DiscoverDirs(startDir string) []string {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		abs = filepath.Clean(startDir)
	}

	var dirs []string
	cur := abs
	for {
		dirs = append(dirs, cur)
		parent := filepath.Dir(cur)
		if parent == cur || parent == "" {
			break
		}
		cur = parent
	}

	if home, err := os.UserHomeDir(); err == nil {
		// Append home only if it's not already in the chain
		// (some users' workdirs are inside $HOME).
		already := false
		for _, d := range dirs {
			if d == home {
				already = true
				break
			}
		}
		if !already {
			dirs = append(dirs, home)
		}
	}
	return dirs
}

// remarshalInto routes a freshly-decoded raw map through the yaml
// codec into a typed target struct. Used by the legacy migrator to
// hoist `linters-settings:` (a free-form map at decode time) into the
// typed [LintersSettings].
func remarshalInto(src any, dst any) error {
	b, err := yaml.Marshal(src)
	if err != nil {
		return fmt.Errorf("remarshal: marshal: %w", err)
	}
	if err := yaml.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("remarshal: unmarshal: %w", err)
	}
	return nil
}
