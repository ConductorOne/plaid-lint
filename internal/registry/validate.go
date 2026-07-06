// Copyright 2026 The plaid-lint Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package registry

import (
	"fmt"
	"sort"

	"github.com/conductorone/plaid-lint/internal/config"
)

// Validate returns one error per unknown / mis-configured entry in
// cfg's `linters.enable[]` / `linters.disable[]` lists. Each error
// names the offending entry and, when possible, suggests the
// closest known linter via a Levenshtein-style did-you-mean
// heuristic.
//
// Validate also rejects `linters.default` values that aren't one of
// `none` / `standard` / `fast` / `all`.
//
// Validate is the registry-aware counterpart to T2.1's
// [config.Validate], which doesn't know which linter names exist.
// Run [config.Validate] first to catch schema-level errors, then
// [Validate] to catch unknown linter names.
func Validate(cfg *config.Config) []error {
	if cfg == nil {
		return nil
	}
	return validateAgainstCatalog(cfg, defaultCatalog)
}

func validateAgainstCatalog(cfg *config.Config, cat *catalog) []error {
	var errs []error

	// linters.default: must be one of the four group names (or "").
	switch cfg.Linters.Default {
	case "", config.GroupNone, config.GroupStandard, config.GroupFast, config.GroupAll:
		// ok
	default:
		errs = append(errs, fmt.Errorf("linters.default: %q is not one of (none|standard|fast|all)",
			cfg.Linters.Default))
	}

	// linters.enable[]: every name must be a known linter or formatter.
	for i, n := range cfg.Linters.Enable {
		if _, ok := cat.resolve(n); !ok {
			suggest := suggestion(cat, n)
			if suggest != "" {
				errs = append(errs, fmt.Errorf(
					"linters.enable[%d]: unknown linter %q; did you mean %q?", i, n, suggest))
			} else {
				errs = append(errs, fmt.Errorf(
					"linters.enable[%d]: unknown linter %q", i, n))
			}
		}
	}

	// linters.disable[]: same.
	for i, n := range cfg.Linters.Disable {
		if _, ok := cat.resolve(n); !ok {
			suggest := suggestion(cat, n)
			if suggest != "" {
				errs = append(errs, fmt.Errorf(
					"linters.disable[%d]: unknown linter %q; did you mean %q?", i, n, suggest))
			} else {
				errs = append(errs, fmt.Errorf(
					"linters.disable[%d]: unknown linter %q", i, n))
			}
		}
	}

	return errs
}

// suggestion returns the closest catalog name to the user's typo, or
// "" if no name is close enough to be worth suggesting. Closeness is
// measured via Levenshtein distance with a cutoff of 3 (so
// `errchek` → `errcheck` but `foobarbaz` → "").
//
// Tie-break: alphabetical (so the suggestion is deterministic across
// runs).
func suggestion(cat *catalog, want string) string {
	const maxDistance = 3
	names := cat.names()

	type cand struct {
		name string
		dist int
	}
	var best cand
	best.dist = maxDistance + 1

	for _, n := range names {
		d := levenshtein(want, n)
		if d < best.dist || (d == best.dist && n < best.name) {
			best = cand{name: n, dist: d}
		}
	}

	if best.dist > maxDistance {
		return ""
	}
	return best.name
}

// levenshtein returns the classic edit distance between a and b.
// Allocations are kept to a single row since the input strings are
// short (linter names are <30 chars).
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Use byte iteration; linter names are ASCII so byte-level edit
	// distance equals rune-level.
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(
				prev[j]+1,      // deletion
				cur[j-1]+1,     // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func minInt(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

// _ keeps sort imported in case future validation rules need it.
var _ = sort.Strings
