// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"
	"slices"
)

// Formatters mirrors v2's `formatters:` block. v1 callers don't have
// this section; the legacy decoder mirrors
// `linters-settings.{gofmt,goimports,gofumpt,gci,golines}` into
// [Formatters.Settings] when present.
type Formatters struct {
	Enable     []string            `yaml:"enable,omitempty" json:"enable,omitempty"`
	Settings   FormatterSettings   `yaml:"settings,omitempty" json:"settings,omitempty"`
	Exclusions FormatterExclusions `yaml:"exclusions,omitempty" json:"exclusions,omitempty"`
}

// Validate rejects names in Enable that aren't real formatters.
func (f *Formatters) Validate() error {
	for _, n := range f.Enable {
		if !slices.Contains(allFormatterNames, n) {
			return fmt.Errorf("formatters.enable: %s is not a formatter", n)
		}
	}
	return nil
}

// FormatterExclusions mirrors v2's `formatters.exclusions:` block.
type FormatterExclusions struct {
	Generated  string   `yaml:"generated,omitempty" json:"generated,omitempty"`
	Paths      []string `yaml:"paths,omitempty" json:"paths,omitempty"`
	WarnUnused bool     `yaml:"warn-unused,omitempty" json:"warn-unused,omitempty"`
}

// FormatterSettings groups the per-formatter configuration. Mirrors
// upstream's struct verbatim (yaml-tagged).
type FormatterSettings struct {
	Gci       GciSettings       `yaml:"gci,omitempty" json:"gci,omitempty"`
	GoFmt     GoFmtSettings     `yaml:"gofmt,omitempty" json:"gofmt,omitempty"`
	GoFumpt   GoFumptSettings   `yaml:"gofumpt,omitempty" json:"gofumpt,omitempty"`
	GoImports GoImportsSettings `yaml:"goimports,omitempty" json:"goimports,omitempty"`
	GoLines   GoLinesSettings   `yaml:"golines,omitempty" json:"golines,omitempty"`
}

// GciSettings configures the `gci` formatter.
type GciSettings struct {
	Sections         []string `yaml:"sections,omitempty" json:"sections,omitempty"`
	NoInlineComments bool     `yaml:"no-inline-comments,omitempty" json:"no-inline-comments,omitempty"`
	NoPrefixComments bool     `yaml:"no-prefix-comments,omitempty" json:"no-prefix-comments,omitempty"`
	CustomOrder      bool     `yaml:"custom-order,omitempty" json:"custom-order,omitempty"`
	NoLexOrder       bool     `yaml:"no-lex-order,omitempty" json:"no-lex-order,omitempty"`
}

// GoFmtSettings configures the `gofmt` formatter.
type GoFmtSettings struct {
	Simplify     bool               `yaml:"simplify,omitempty" json:"simplify,omitempty"`
	RewriteRules []GoFmtRewriteRule `yaml:"rewrite-rules,omitempty" json:"rewrite-rules,omitempty"`
}

// GoFmtRewriteRule is a single gofmt -r pattern/replacement pair.
type GoFmtRewriteRule struct {
	Pattern     string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Replacement string `yaml:"replacement,omitempty" json:"replacement,omitempty"`
}

// GoFumptSettings configures the `gofumpt` formatter.
type GoFumptSettings struct {
	ModulePath string `yaml:"module-path,omitempty" json:"module-path,omitempty"`
	ExtraRules bool   `yaml:"extra-rules,omitempty" json:"extra-rules,omitempty"`
	// LangVersion is upstream-internal; populated from run.go at use-time
	// by the engine. Not user-settable in YAML.
	LangVersion string `yaml:"-" json:"-"`
}

// GoImportsSettings configures the `goimports` formatter.
type GoImportsSettings struct {
	LocalPrefixes []string `yaml:"local-prefixes,omitempty" json:"local-prefixes,omitempty"`
}

// GoLinesSettings configures the `golines` formatter.
type GoLinesSettings struct {
	MaxLen          int  `yaml:"max-len,omitempty" json:"max-len,omitempty"`
	TabLen          int  `yaml:"tab-len,omitempty" json:"tab-len,omitempty"`
	ShortenComments bool `yaml:"shorten-comments,omitempty" json:"shorten-comments,omitempty"`
	ReformatTags    bool `yaml:"reformat-tags,omitempty" json:"reformat-tags,omitempty"`
	ChainSplitDots  bool `yaml:"chain-split-dots,omitempty" json:"chain-split-dots,omitempty"`
}
