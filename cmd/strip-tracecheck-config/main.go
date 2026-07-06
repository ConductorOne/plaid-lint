// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
//
// strip-tracecheck-config rewrites a golangci-lint v2 config so it can
// be loaded without the tracecheck custom Go-plugin. The dev env does
// not produce the tracecheck .so; golangci-lint refuses to start when
// it can't load referenced custom plugins, so the comparison harness
// needs a cleaned copy of the config.
//
// Equivalent to the python heredoc previously inlined in
// scripts/compare-against-c1.sh:
//
//   - remove linters.settings.custom
//   - drop "tracecheck" from linters.enable and linters.disable
//   - drop "tracecheck" from any linters.exclusions.rules[*].linters list
//
// Uses yaml.v3's Node API to preserve mapping-key order so the output
// stays as close as practical to the source layout.
package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const targetLinter = "tracecheck"

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <src> <dst>\n", os.Args[0])
		os.Exit(2)
	}
	src, dst := os.Args[1], os.Args[2]
	if err := run(src, dst); err != nil {
		fmt.Fprintf(os.Stderr, "strip-tracecheck-config: %v\n", err)
		os.Exit(1)
	}
}

func run(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(in, &root); err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	strip(&root)
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer out.Close()
	enc := yaml.NewEncoder(out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return fmt.Errorf("encode %s: %w", dst, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close encoder: %w", err)
	}
	return nil
}

// strip applies the tracecheck-removal transforms to the parsed YAML
// document tree. Mirrors the python heredoc:
//
//	doc["linters"]["settings"].pop("custom", None)
//	for k in ("enable","disable"): linters[k] = [x for x in linters[k] if x != "tracecheck"]
//	for r in linters["exclusions"]["rules"]: r["linters"] = [x for x in r["linters"] if x != "tracecheck"]
func strip(root *yaml.Node) {
	doc := documentRoot(root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return
	}
	linters := mapValue(doc, "linters")
	if linters == nil || linters.Kind != yaml.MappingNode {
		return
	}
	if settings := mapValue(linters, "settings"); settings != nil && settings.Kind == yaml.MappingNode {
		mapDelete(settings, "custom")
	}
	for _, key := range []string{"enable", "disable"} {
		if list := mapValue(linters, key); list != nil && list.Kind == yaml.SequenceNode {
			list.Content = filterScalarSeq(list.Content, targetLinter)
		}
	}
	if exclusions := mapValue(linters, "exclusions"); exclusions != nil && exclusions.Kind == yaml.MappingNode {
		if rules := mapValue(exclusions, "rules"); rules != nil && rules.Kind == yaml.SequenceNode {
			for _, rule := range rules.Content {
				if rule.Kind != yaml.MappingNode {
					continue
				}
				if rl := mapValue(rule, "linters"); rl != nil && rl.Kind == yaml.SequenceNode {
					rl.Content = filterScalarSeq(rl.Content, targetLinter)
				}
			}
		}
	}
}

// documentRoot unwraps a DocumentNode to its single child, or returns
// node as-is if it's already a mapping.
func documentRoot(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		return node.Content[0]
	}
	return node
}

// mapValue returns the value node for key in a MappingNode, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mapDelete removes the key/value pair for key from a MappingNode.
func mapDelete(m *yaml.Node, key string) {
	if m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// filterScalarSeq returns content with any scalar nodes whose Value
// equals drop removed. Non-scalar entries (unlikely for these lists)
// are preserved.
func filterScalarSeq(content []*yaml.Node, drop string) []*yaml.Node {
	out := content[:0]
	for _, n := range content {
		if n.Kind == yaml.ScalarNode && n.Value == drop {
			continue
		}
		out = append(out, n)
	}
	return out
}
