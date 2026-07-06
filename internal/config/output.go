// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
	"slices"
	"strings"
)

// Output mirrors v2's `output:` block. The v1 single `format` string is
// migrated into [Formats] by the legacy decoder; see SCHEMA.md.
type Output struct {
	Formats    Formats  `yaml:"formats,omitempty" json:"formats,omitempty"`
	SortOrder  []string `yaml:"sort-order,omitempty" json:"sort-order,omitempty"`
	ShowStats  bool     `yaml:"show-stats,omitempty" json:"show-stats,omitempty"`
	PathPrefix string   `yaml:"path-prefix,omitempty" json:"path-prefix,omitempty"`
	PathMode   string   `yaml:"path-mode,omitempty" json:"path-mode,omitempty"`
}

// Validate checks output sort order names and path mode.
func (o *Output) Validate() error {
	if err := o.validateSortOrder(); err != nil {
		return err
	}
	return o.validatePathMode()
}

func (o *Output) validateSortOrder() error {
	validOrders := []string{"linter", "file", "severity"}
	all := strings.Join(o.SortOrder, " ")
	for _, order := range o.SortOrder {
		if strings.Count(all, order) > 1 {
			return fmt.Errorf("output.sort-order: name %q is repeated", order)
		}
		if !slices.Contains(validOrders, order) {
			return fmt.Errorf("output.sort-order: %q is not one of (linter|file|severity)", order)
		}
	}
	return nil
}

func (o *Output) validatePathMode() error {
	switch o.PathMode {
	case "", "abs":
		return nil
	default:
		return fmt.Errorf("output.path-mode: %q is not one of (\"\"|abs)", o.PathMode)
	}
}

// Formats holds the per-named-format output configuration. Upstream
// names: text, json, tab, html, checkstyle, code-climate, junit-xml,
// teamcity, sarif. SARIF is a v2-era addition; everything else is
// shared with v1 (post-migration).
type Formats struct {
	Text        Text         `yaml:"text,omitempty" json:"text,omitempty"`
	JSON        SimpleFormat `yaml:"json,omitempty" json:"json,omitempty"`
	Tab         Tab          `yaml:"tab,omitempty" json:"tab,omitempty"`
	HTML        SimpleFormat `yaml:"html,omitempty" json:"html,omitempty"`
	Checkstyle  SimpleFormat `yaml:"checkstyle,omitempty" json:"checkstyle,omitempty"`
	CodeClimate SimpleFormat `yaml:"code-climate,omitempty" json:"code-climate,omitempty"`
	JUnitXML    JUnitXML     `yaml:"junit-xml,omitempty" json:"junit-xml,omitempty"`
	TeamCity    SimpleFormat `yaml:"teamcity,omitempty" json:"teamcity,omitempty"`
	Sarif       SimpleFormat `yaml:"sarif,omitempty" json:"sarif,omitempty"`
}

// IsEmpty reports whether any named format has a Path set. An empty
// Formats means "no explicit output config".
func (f *Formats) IsEmpty() bool {
	return f.Text.Path == "" && f.JSON.Path == "" && f.Tab.Path == "" &&
		f.HTML.Path == "" && f.Checkstyle.Path == "" && f.CodeClimate.Path == "" &&
		f.JUnitXML.Path == "" && f.TeamCity.Path == "" && f.Sarif.Path == ""
}

// SimpleFormat is the common per-format shape: a single output path.
type SimpleFormat struct {
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
}

// Text is the `text` printer config. Extends SimpleFormat with v1-era
// extras (`print-issued-lines`, `print-linter-name`).
type Text struct {
	Path            string `yaml:"path,omitempty" json:"path,omitempty"`
	PrintLinterName bool   `yaml:"print-linter-name,omitempty" json:"print-linter-name,omitempty"`
	PrintIssuedLine bool   `yaml:"print-issued-lines,omitempty" json:"print-issued-lines,omitempty"`
	Colors          bool   `yaml:"colors,omitempty" json:"colors,omitempty"`
}

// Tab is the `tab` printer config. Same shape as Text minus
// PrintIssuedLine.
type Tab struct {
	Path            string `yaml:"path,omitempty" json:"path,omitempty"`
	PrintLinterName bool   `yaml:"print-linter-name,omitempty" json:"print-linter-name,omitempty"`
	Colors          bool   `yaml:"colors,omitempty" json:"colors,omitempty"`
}

// JUnitXML is the `junit-xml` printer config.
type JUnitXML struct {
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Extended bool   `yaml:"extended,omitempty" json:"extended,omitempty"`
}
