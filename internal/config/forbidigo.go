// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// UnmarshalYAML decodes a [ForbidigoPattern] from either a bare string
// (the legacy `forbid: ["pattern1", "pattern2"]` form) or a mapping
// `{p|pattern, pkg, msg}`. Upstream uses an encoding.TextUnmarshaler
// hook for the same effect.
func (p *ForbidigoPattern) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		// Bare string: the entire value is the pattern.
		p.Pattern = node.Value
		return nil
	case yaml.MappingNode:
		// Mapping shape: walk the keys explicitly so we can accept
		// upstream's mapstructure tag (`pattern`) alongside our yaml
		// canonical (`p`).
		raw := struct {
			P       string `yaml:"p"`
			Pattern string `yaml:"pattern"`
			Pkg     string `yaml:"pkg"`
			Msg     string `yaml:"msg"`
		}{}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("forbidigo.forbid: %w", err)
		}
		switch {
		case raw.P != "" && raw.Pattern != "":
			return fmt.Errorf("forbidigo.forbid: %q and %q are aliases — supply only one", "p", "pattern")
		case raw.P != "":
			p.Pattern = raw.P
		default:
			p.Pattern = raw.Pattern
		}
		p.Package = raw.Pkg
		p.Msg = raw.Msg
		return nil
	default:
		return fmt.Errorf("forbidigo.forbid: unsupported YAML kind %d (want scalar or mapping)", node.Kind)
	}
}
