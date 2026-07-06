// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package exclusion

import "github.com/conductorone/plaid-lint/internal/config"

// presetRules returns the compiled-in v2 exclusion rules for the given
// preset name. An unknown name returns nil — Config.Validate should
// have already rejected unknown presets before we get here.
//
// Mirrors golangci-lint v2 master commit 72798d3
// (pkg/result/processors/exclusion_presets.go::LinterExclusionPresets).
//
// IMPORTANT: keep this table in sync with upstream. Each rule's
// `Text` is a regex that matches against the diagnostic message, and
// `Linters` constrains the rule to those linters. Both must match for
// the rule to fire.
func presetRules(name string) []config.ExcludeRule {
	switch name {
	case config.ExclusionPresetComments:
		return []config.ExcludeRule{
			rule(`(ST1000|ST1020|ST1021|ST1022)`, "staticcheck"),
			rule(`exported (.+) should have comment( \(or a comment on this block\))? or be unexported`, "revive"),
			rule(`package comment should be of the form "(.+)..."`, "revive"),
			rule(`comment on exported (.+) should be of the form "(.+)..."`, "revive"),
			rule(`should have a package comment`, "revive"),
		}
	case config.ExclusionPresetStdErrorHandling:
		return []config.ExcludeRule{
			rule(`(?i)Error return value of .((os\.)?std(out|err)\..*|.*Close|.*Flush|os\.Remove(All)?|.*print(f|ln)?|os\.(Un)?Setenv). is not checked`, "errcheck"),
		}
	case config.ExclusionPresetCommonFalsePositives:
		return []config.ExcludeRule{
			rule(`G103: Use of unsafe calls should be audited`, "gosec"),
			rule(`G204: Subprocess launched with variable`, "gosec"),
			rule(`G304: Potential file inclusion via variable`, "gosec"),
		}
	case config.ExclusionPresetLegacy:
		return []config.ExcludeRule{
			rule(`(possible misuse of unsafe.Pointer|should have signature)`, "govet"),
			rule(`SA4011`, "staticcheck"),
			rule(`G104`, "gosec"),
			rule(`(G301|G302|G307): Expect (directory permissions to be 0750|file permissions to be 0600) or less`, "gosec"),
		}
	}
	return nil
}

func rule(text string, linters ...string) config.ExcludeRule {
	return config.ExcludeRule{
		BaseRule: config.BaseRule{
			Text:    text,
			Linters: linters,
		},
	}
}
